package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ariadne/internal/codec"
	"ariadne/internal/config"
	"ariadne/internal/geo"
	"ariadne/internal/service"
	"ariadne/internal/taskstore"
)

// --- фейковые notifier'ы ---

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, string, string) error { return nil }

type notifyCall struct{ taskKey, status string }

// recordingNotifier запоминает вызовы Notify; err — что вернуть.
type recordingNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
	err   error
}

func (r *recordingNotifier) Notify(_ context.Context, taskKey, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, notifyCall{taskKey, status})
	return r.err
}

func (r *recordingNotifier) snapshot() []notifyCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]notifyCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// --- фейковый resolver: управляемая задержка / ошибка / паника ---

type fakeResolver struct {
	delay       time.Duration
	err         error
	shouldPanic bool
}

func (f fakeResolver) Resolve(ctx context.Context, points []geo.Point) (*service.Result, error) {
	if f.shouldPanic {
		panic("boom in resolve")
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done(): // уважаем таймаут обработки
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &service.Result{Points: points, LengthMeters: 42}, nil
}

// --- хелперы ---

func testConfig() *config.Config {
	return &config.Config{
		MaxPoints:             50000,
		DedupDistanceMeters:   2.0,
		DedupTimeGap:          60 * time.Second,
		MaxSpeedKmh:           150,
		MaxAccelKmhPerSec:     20,
		TeleportJumpMeters:    15000,
		TeleportReturnMeters:  2000,
		TeleportMaxSpanMeters: 5000,
		StopRadiusMeters:      50,
		StopMinPoints:         5,
		MaxLoopMeters:         100,
		MaxLoopSeconds:        10,
		SimplifyMinMeters:     5.0,
		IntersectMaxIter:      10000,
	}
}

func newTestStore(t *testing.T) *taskstore.Store {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	return taskstore.New(mr.Addr(), 0, "", time.Hour)
}

func newPool(t *testing.T, store *taskstore.Store, r resolver, taskTimeout time.Duration) *Pool {
	t.Helper()
	return newPoolN(t, store, r, noopNotifier{}, taskTimeout)
}

func newPoolN(t *testing.T, store *taskstore.Store, r resolver, n notifier, taskTimeout time.Duration) *Pool {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(store, r, n, logger, 1, taskTimeout, 20<<20)
}

// validInput — корректный сжатый маршрут (несколько точек, переживут чистку).
func validInput(t *testing.T) string {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pts := []geo.Point{
		{Time: base, Lon: 37.6000, Lat: 55.7500},
		{Time: base.Add(60 * time.Second), Lon: 37.6100, Lat: 55.7560},
		{Time: base.Add(120 * time.Second), Lon: 37.6220, Lat: 55.7520},
		{Time: base.Add(180 * time.Second), Lon: 37.6300, Lat: 55.7600},
	}
	enc, err := codec.Encode(pts)
	require.NoError(t, err)
	return enc
}

func saveTask(t *testing.T, store *taskstore.Store, key, input string) {
	t.Helper()
	ok, err := store.SaveNew(context.Background(), &taskstore.Task{
		Key: key, Status: taskstore.StatusPending, Input: input,
	})
	require.NoError(t, err)
	require.True(t, ok)
}

// --- тесты ---

// happy path: валидный маршрут → done + результат.
func TestProcess_Done(t *testing.T) {
	store := newTestStore(t)
	pool := newPool(t, store, service.New(testConfig()), 10*time.Second)
	saveTask(t, store, "ok", validInput(t))

	pool.process("ok")

	got, err := store.Get(context.Background(), "ok")
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusDone, got.Status)
	assert.NotEmpty(t, got.Result)
	assert.Greater(t, got.LengthMeters, 0.0)
	assert.Empty(t, got.Error)
}

// битый Input → задача failed, ошибка про decode.
func TestProcess_DecodeError_Failed(t *testing.T) {
	store := newTestStore(t)
	pool := newPool(t, store, service.New(testConfig()), 10*time.Second)
	saveTask(t, store, "bad", "!!! не base64 zlib !!!")

	pool.process("bad")

	got, err := store.Get(context.Background(), "bad")
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusFailed, got.Status)
	assert.Contains(t, got.Error, "decode")
}

// ошибка обработки → failed, ошибка про resolve.
func TestProcess_ResolveError_Failed(t *testing.T) {
	store := newTestStore(t)
	pool := newPool(t, store, fakeResolver{err: errors.New("kaboom")}, 10*time.Second)
	saveTask(t, store, "re", validInput(t))

	pool.process("re")

	got, err := store.Get(context.Background(), "re")
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusFailed, got.Status)
	assert.Contains(t, got.Error, "resolve")
}

// таймаут обработки → failed (procCtx убивает медленную обработку).
func TestProcess_ProcessingTimeout_Failed(t *testing.T) {
	store := newTestStore(t)
	pool := newPool(t, store, fakeResolver{delay: time.Second}, 10*time.Millisecond)
	saveTask(t, store, "to", validInput(t))

	pool.process("to")

	got, err := store.Get(context.Background(), "to")
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusFailed, got.Status)
	assert.NotEmpty(t, got.Error)
}

// РЕГРЕССИЯ (баг, найденный пользователем): долгая обработка не должна «съедать»
// время у контекста записи. writeCtx создаётся ПОСЛЕ обработки, поэтому статус
// записывается, даже если обработка шла дольше ioTimeout.
func TestProcess_SlowProcessing_StillWritesResult(t *testing.T) {
	oldIO := ioTimeout
	ioTimeout = 20 * time.Millisecond
	t.Cleanup(func() { ioTimeout = oldIO })

	store := newTestStore(t)
	// обработка 50мс > ioTimeout(20мс), но < таймаута обработки(2с)
	pool := newPool(t, store, fakeResolver{delay: 50 * time.Millisecond}, 2*time.Second)
	saveTask(t, store, "slow", validInput(t))

	pool.process("slow")

	got, err := store.Get(context.Background(), "slow")
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusDone, got.Status)
	assert.NotEmpty(t, got.Result)
}

// карточки нет (Get → ErrNotFound) → не паникуем, ничего не создаём.
func TestProcess_GetMissing_NoPanic(t *testing.T) {
	store := newTestStore(t)
	pool := newPool(t, store, service.New(testConfig()), 10*time.Second)

	assert.NotPanics(t, func() { pool.process("nope") })

	_, err := store.Get(context.Background(), "nope")
	assert.ErrorIs(t, err, taskstore.ErrNotFound)
}

// паника в обработке не должна валить воркер (recover в process).
// Текущее поведение: при панике статус остаётся pending (результат не пишется).
func TestProcess_PanicRecovered(t *testing.T) {
	store := newTestStore(t)
	pool := newPool(t, store, fakeResolver{shouldPanic: true}, 10*time.Second)
	saveTask(t, store, "p", validInput(t))

	assert.NotPanics(t, func() { pool.process("p") })

	got, err := store.Get(context.Background(), "p")
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusPending, got.Status)
}

// полный цикл: Start → задача в очереди → воркер её разобрал → done → Shutdown.
func TestPool_ProcessesQueuedTask(t *testing.T) {
	store := newTestStore(t)
	pool := newPool(t, store, service.New(testConfig()), 10*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)

	saveTask(t, store, "q", validInput(t))
	require.NoError(t, store.Enqueue(context.Background(), "q"))

	require.Eventually(t, func() bool {
		got, err := store.Get(context.Background(), "q")
		return err == nil && got.Status == taskstore.StatusDone
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	shCtx, shCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shCancel()
	pool.Shutdown(shCtx)
}

// --- коллбэк ---

// done → коллбэк дёрнут ровно один раз со статусом done.
func TestProcess_NotifiesOnDone(t *testing.T) {
	store := newTestStore(t)
	rec := &recordingNotifier{}
	pool := newPoolN(t, store, service.New(testConfig()), rec, 10*time.Second)
	saveTask(t, store, "n1", validInput(t))

	pool.process("n1")

	calls := rec.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "n1", calls[0].taskKey)
	assert.Equal(t, "done", calls[0].status)
}

// failed → коллбэк тоже дёрнут, со статусом failed.
func TestProcess_NotifiesOnFailed(t *testing.T) {
	store := newTestStore(t)
	rec := &recordingNotifier{}
	pool := newPoolN(t, store, fakeResolver{err: errors.New("boom")}, rec, 10*time.Second)
	saveTask(t, store, "n2", validInput(t))

	pool.process("n2")

	calls := rec.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "n2", calls[0].taskKey)
	assert.Equal(t, "failed", calls[0].status)
}

// коллбэк упал — задача всё равно done, воркер не падает (ошибку только логируем).
func TestProcess_NotifyErrorDoesNotBreak(t *testing.T) {
	store := newTestStore(t)
	rec := &recordingNotifier{err: errors.New("laravel down")}
	pool := newPoolN(t, store, service.New(testConfig()), rec, 10*time.Second)
	saveTask(t, store, "n3", validInput(t))

	assert.NotPanics(t, func() { pool.process("n3") })

	got, err := store.Get(context.Background(), "n3")
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusDone, got.Status)
}

// карточки нет (Get → ошибка) → коллбэк НЕ шлём (нечего сообщать).
func TestProcess_NoNotifyWhenTaskMissing(t *testing.T) {
	store := newTestStore(t)
	rec := &recordingNotifier{}
	pool := newPoolN(t, store, service.New(testConfig()), rec, 10*time.Second)

	pool.process("ghost")

	assert.Empty(t, rec.snapshot(), "коллбэка по несуществующей задаче быть не должно")
}
