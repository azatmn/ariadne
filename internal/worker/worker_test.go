package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(store, r, logger, 1, taskTimeout, 20<<20)
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
