package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"ariadne/internal/codec"
	ariadnepb "ariadne/internal/gen/ariadne"
	"ariadne/internal/geo"
	"ariadne/internal/pipeline"
	"ariadne/internal/taskstore"
)

const queueKey = "tasks:stream" // должен совпадать с taskstore/queue.go

func testStore(t *testing.T) (*taskstore.Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	store := taskstore.New(mr.Addr(), 0, "", time.Hour)
	// Как в бою: очередь создаётся на старте сервиса, до первой задачи.
	require.NoError(t, store.EnsureQueue(context.Background()))
	return store, mr
}

func newHandler(t *testing.T) (*Handler, *taskstore.Store, *miniredis.Miniredis) {
	t.Helper()
	store, mr := testStore(t)
	return NewHandler(store), store, mr
}

func testRoute(t *testing.T) string {
	t.Helper()
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	points := []geo.Point{
		{Time: t0, Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(10 * time.Second), Lon: 37.617400, Lat: 55.755900},
		{Time: t0.Add(20 * time.Second), Lon: 37.617500, Lat: 55.756000},
		{Time: t0.Add(30 * time.Second), Lon: 37.617600, Lat: 55.756100},
	}
	encoded, err := codec.Encode(points)
	require.NoError(t, err)
	return encoded
}

func saveCard(t *testing.T, store *taskstore.Store, card *taskstore.Task) {
	t.Helper()
	ok, err := store.SaveNew(context.Background(), card)
	require.NoError(t, err)
	require.True(t, ok)
}

// grpcCode достаёт gRPC-код из ошибки (и проверяет, что ошибка вообще есть).
func grpcCode(t *testing.T, err error) codes.Code {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	return st.Code()
}

// --- SubmitTask ---

// валидный submit: taskKey есть, в Redis карточка pending с нашим входом, ключ в очереди.
func TestSubmitTask_Valid(t *testing.T) {
	h, store, mr := newHandler(t)
	route := testRoute(t)

	resp, err := h.SubmitTask(context.Background(), &ariadnepb.SubmitTaskRequest{RouteCompressed: route})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetTaskKey())

	card, err := store.Get(context.Background(), resp.GetTaskKey())
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusPending, card.Status)
	assert.Equal(t, route, card.Input)
	assert.False(t, card.CreatedAt.IsZero())

	list := queuedKeys(t, mr)
	assert.Contains(t, list, resp.GetTaskKey())
}

// пустой вход → InvalidArgument и НИЧЕГО не записано.
func TestSubmitTask_Empty(t *testing.T) {
	h, _, mr := newHandler(t)
	_, err := h.SubmitTask(context.Background(), &ariadnepb.SubmitTaskRequest{RouteCompressed: ""})
	assert.Equal(t, codes.InvalidArgument, grpcCode(t, err))
	assert.Empty(t, taskKeys(mr), "при отказе в Redis не должно появиться ключей задач")
}

// мусорный непустой вход принимается (submit не декодит) — станет failed у воркера.
func TestSubmitTask_GarbageAccepted(t *testing.T) {
	h, store, _ := newHandler(t)
	resp, err := h.SubmitTask(context.Background(), &ariadnepb.SubmitTaskRequest{RouteCompressed: "@@@ не base64 @@@"})
	require.NoError(t, err)

	card, err := store.Get(context.Background(), resp.GetTaskKey())
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusPending, card.Status)
	assert.Equal(t, "@@@ не base64 @@@", card.Input)
}

// два submit → разные ключи, оба в очереди.
func TestSubmitTask_UniqueKeys(t *testing.T) {
	h, _, mr := newHandler(t)
	route := testRoute(t)

	r1, err := h.SubmitTask(context.Background(), &ariadnepb.SubmitTaskRequest{RouteCompressed: route})
	require.NoError(t, err)
	r2, err := h.SubmitTask(context.Background(), &ariadnepb.SubmitTaskRequest{RouteCompressed: route})
	require.NoError(t, err)
	require.NotEqual(t, r1.GetTaskKey(), r2.GetTaskKey())

	list := queuedKeys(t, mr)
	assert.Len(t, list, 2)
}

// --- GetTask ---

func TestGetTask_Pending(t *testing.T) {
	h, store, _ := newHandler(t)
	saveCard(t, store, &taskstore.Task{Key: "p1", Status: taskstore.StatusPending, Input: "in"})

	resp, err := h.GetTask(context.Background(), &ariadnepb.GetTaskRequest{TaskKey: "p1"})
	require.NoError(t, err)
	assert.Equal(t, "p1", resp.GetTaskKey())
	assert.Equal(t, string(taskstore.StatusPending), resp.GetStatus())
	assert.Empty(t, resp.GetRouteCompressed())
	assert.Zero(t, resp.GetLengthMeters())
}

func TestGetTask_Done(t *testing.T) {
	h, store, _ := newHandler(t)
	saveCard(t, store, &taskstore.Task{Key: "d1", Status: taskstore.StatusDone, Input: "orig", Result: "cleaned", LengthMeters: 99.5})

	resp, err := h.GetTask(context.Background(), &ariadnepb.GetTaskRequest{TaskKey: "d1"})
	require.NoError(t, err)
	assert.Equal(t, string(taskstore.StatusDone), resp.GetStatus())
	assert.Equal(t, "cleaned", resp.GetRouteCompressed())
	assert.Equal(t, 99.5, resp.GetLengthMeters())
}

// АДВЕРСАРИАЛЬНО: done отдаёт Result, а НЕ Input.
func TestGetTask_DoneReturnsResultNotInput(t *testing.T) {
	h, store, _ := newHandler(t)
	saveCard(t, store, &taskstore.Task{Key: "d2", Status: taskstore.StatusDone, Input: "INPUT", Result: "RESULT"})

	resp, err := h.GetTask(context.Background(), &ariadnepb.GetTaskRequest{TaskKey: "d2"})
	require.NoError(t, err)
	assert.Equal(t, "RESULT", resp.GetRouteCompressed())
	assert.NotEqual(t, "INPUT", resp.GetRouteCompressed())
}

func TestGetTask_Failed(t *testing.T) {
	h, store, _ := newHandler(t)
	saveCard(t, store, &taskstore.Task{Key: "f1", Status: taskstore.StatusFailed, Error: "decode: boom"})

	resp, err := h.GetTask(context.Background(), &ariadnepb.GetTaskRequest{TaskKey: "f1"})
	require.NoError(t, err)
	assert.Equal(t, string(taskstore.StatusFailed), resp.GetStatus())
	assert.Equal(t, "decode: boom", resp.GetError())
	assert.Empty(t, resp.GetRouteCompressed())
}

func TestGetTask_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	_, err := h.GetTask(context.Background(), &ariadnepb.GetTaskRequest{TaskKey: "nope"})
	assert.Equal(t, codes.NotFound, grpcCode(t, err))
}

func TestGetTask_EmptyKey(t *testing.T) {
	h, _, _ := newHandler(t)
	_, err := h.GetTask(context.Background(), &ariadnepb.GetTaskRequest{TaskKey: ""})
	assert.Equal(t, codes.InvalidArgument, grpcCode(t, err))
}

// --- GetTaskDebug ---

func TestGetTaskDebug_Done(t *testing.T) {
	h, store, _ := newHandler(t)
	saveCard(t, store, &taskstore.Task{
		Key: "g1", Status: taskstore.StatusDone,
		Debug: []pipeline.StageStats{{Name: "dedup", PointsBefore: 100, PointsAfter: 90}},
	})

	resp, err := h.GetTaskDebug(context.Background(), &ariadnepb.GetTaskDebugRequest{TaskKey: "g1"})
	require.NoError(t, err)
	require.Len(t, resp.GetDebug(), 1)
	assert.Equal(t, "dedup", resp.GetDebug()[0].GetName())
	assert.Equal(t, int32(100), resp.GetDebug()[0].GetPointsBefore())
}

func TestGetTaskDebug_NotFound(t *testing.T) {
	h, _, _ := newHandler(t)
	_, err := h.GetTaskDebug(context.Background(), &ariadnepb.GetTaskDebugRequest{TaskKey: "nope"})
	assert.Equal(t, codes.NotFound, grpcCode(t, err))
}

// debug у pending (stats ещё нет) → пусто, без паники.
func TestGetTaskDebug_PendingEmpty(t *testing.T) {
	h, store, _ := newHandler(t)
	saveCard(t, store, &taskstore.Task{Key: "g2", Status: taskstore.StatusPending})

	resp, err := h.GetTaskDebug(context.Background(), &ariadnepb.GetTaskDebugRequest{TaskKey: "g2"})
	require.NoError(t, err)
	assert.Empty(t, resp.GetDebug())
}

// Та же оговорка к километражу в gRPC. Проверяется отдельно от REST не для
// симметрии: маппинг в gRPC пишется руками, и поле, забытое здесь, останется
// нулевым — сборка не сломается, и никто не заметит. Так уже терялись `Extra`
// и `Error` в разборе по стадиям.
func TestGetTask_DoneCarriesDegradedAndWarnings(t *testing.T) {
	h, store, _ := newHandler(t)
	saveCard(t, store, &taskstore.Task{
		Key: "dg", Status: taskstore.StatusDone,
		Result: "cleaned", LengthMeters: 971034.2,
		Degraded: true,
		Warnings: []string{"fill_gaps: budget spent — mileage understated"},
	})

	resp, err := h.GetTask(context.Background(), &ariadnepb.GetTaskRequest{TaskKey: "dg"})
	require.NoError(t, err)

	assert.Equal(t, "done", resp.GetStatus(), "статус обязан остаться прежним")
	assert.True(t, resp.GetDegraded())
	require.Len(t, resp.GetWarnings(), 1)
	assert.Contains(t, resp.GetWarnings()[0], "mileage understated")
}

// taskKeys — ключи, появившиеся ИЗ-ЗА запроса.
//
// Ключ очереди (`tasks:stream`) сюда не входит: он создаётся на старте сервиса,
// до всяких запросов, и его наличие ничего не говорит о том, записали мы
// что-то по отказанному запросу или нет.
func taskKeys(mr *miniredis.Miniredis) []string {
	var out []string
	for _, k := range mr.Keys() {
		if k == queueKey {
			continue
		}
		out = append(out, k)
	}
	return out
}

// queuedKeys — номерки, лежащие в очереди.
//
// Очередь теперь поток, а не список: `mr.List` на нём отвечает «нет такого
// ключа». Значение номерка лежит в поле `key` записи — так его кладёт
// `taskstore.Enqueue`.
func queuedKeys(t *testing.T, mr *miniredis.Miniredis) []string {
	t.Helper()
	entries, err := mr.Stream(queueKey)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		for i := 0; i+1 < len(e.Values); i += 2 {
			if e.Values[i] == "key" {
				out = append(out, e.Values[i+1])
			}
		}
	}
	return out
}
