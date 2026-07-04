package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ariadne/internal/pipeline"
	"ariadne/internal/taskstore"
)

// --- хелперы ---

const queueKey = "tasks:queue" // должен совпадать с taskstore/queue.go

// taskEnv поднимает miniredis, стор на него и роутер с тремя async-ручками.
// Возвращает и сырой miniredis, чтобы проверять, что реально записалось в Redis
// (а не верить ответу хендлера на слово).
func taskEnv(t *testing.T) (http.Handler, *taskstore.Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	store := taskstore.New(mr.Addr(), 0, "", time.Hour)
	h := NewHandler(store)

	errMw := ErrorMiddleware(testLogger())
	r := chi.NewRouter()
	r.Post("/v1/tasks", errMw(h.HandleSubmit))
	r.Get("/v1/tasks/{taskKey}", errMw(h.HandleStatus))
	r.Get("/v1/tasks/{taskKey}/debug", errMw(h.HandleDebug))
	return r, store, mr
}

func serve(t *testing.T, router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) ErrorPayload {
	t.Helper()
	var p ErrorPayload
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	return p
}

// --- submit ---

// валидный submit: 202 + taskKey, и в Redis РЕАЛЬНО лежит карточка pending
// с нашим входом, и ключ РЕАЛЬНО попал в очередь.
func TestSubmit_Valid(t *testing.T) {
	router, store, mr := taskEnv(t)
	route := testRoute(t)

	rec := serve(t, router, http.MethodPost, "/v1/tasks", `{"routeCompressed":"`+route+`"}`)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp SubmitResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.TaskKey)

	// карточка существует, pending, с нашим входом и проставленным временем
	card, err := store.Get(context.Background(), resp.TaskKey)
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusPending, card.Status)
	assert.Equal(t, route, card.Input)
	assert.False(t, card.CreatedAt.IsZero(), "CreatedAt должен быть проставлен")
	assert.Empty(t, card.Result)

	// ключ реально в очереди
	list, err := mr.List(queueKey)
	require.NoError(t, err)
	assert.Contains(t, list, resp.TaskKey)
}

// пустой routeCompressed → 400 и НИЧЕГО не записано/не поставлено в очередь.
func TestSubmit_EmptyRoute(t *testing.T) {
	router, _, mr := taskEnv(t)

	rec := serve(t, router, http.MethodPost, "/v1/tasks", `{"routeCompressed":""}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, CodeInvalidRequest, decodeErr(t, rec).Error.Code)
	assert.Empty(t, mr.Keys(), "при отказе в Redis не должно появиться ключей")
}

// поле вообще отсутствует ({}) → тоже 400, ничего не записано.
func TestSubmit_MissingField(t *testing.T) {
	router, _, mr := taskEnv(t)

	rec := serve(t, router, http.MethodPost, "/v1/tasks", `{}`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, mr.Keys())
}

// битый JSON → 400, ничего не записано.
func TestSubmit_InvalidJSON(t *testing.T) {
	router, _, mr := taskEnv(t)

	rec := serve(t, router, http.MethodPost, "/v1/tasks", `{ not json `)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, mr.Keys())
}

// ВАЖНО (фиксируем решение): submit НЕ декодит вход. Заведомо нечитаемый, но
// непустой routeCompressed принимается (202) — мусор станет failed уже у воркера.
func TestSubmit_GarbageRouteAccepted(t *testing.T) {
	router, store, _ := taskEnv(t)

	rec := serve(t, router, http.MethodPost, "/v1/tasks", `{"routeCompressed":"@@@ не base64 zlib @@@"}`)

	require.Equal(t, http.StatusAccepted, rec.Code)
	var resp SubmitResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	card, err := store.Get(context.Background(), resp.TaskKey)
	require.NoError(t, err)
	assert.Equal(t, taskstore.StatusPending, card.Status)
	assert.Equal(t, "@@@ не base64 zlib @@@", card.Input)
}

// два submit'а → РАЗНЫЕ taskKey, обе карточки есть, обе в очереди.
func TestSubmit_UniqueKeys(t *testing.T) {
	router, store, mr := taskEnv(t)
	route := testRoute(t)
	body := `{"routeCompressed":"` + route + `"}`

	rec1 := serve(t, router, http.MethodPost, "/v1/tasks", body)
	rec2 := serve(t, router, http.MethodPost, "/v1/tasks", body)
	require.Equal(t, http.StatusAccepted, rec1.Code)
	require.Equal(t, http.StatusAccepted, rec2.Code)

	var r1, r2 SubmitResponse
	require.NoError(t, json.Unmarshal(rec1.Body.Bytes(), &r1))
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &r2))
	require.NotEqual(t, r1.TaskKey, r2.TaskKey, "taskKey не должны повторяться")

	_, err := store.Get(context.Background(), r1.TaskKey)
	require.NoError(t, err)
	_, err = store.Get(context.Background(), r2.TaskKey)
	require.NoError(t, err)

	list, err := mr.List(queueKey)
	require.NoError(t, err)
	assert.Len(t, list, 2)
	assert.Contains(t, list, r1.TaskKey)
	assert.Contains(t, list, r2.TaskKey)
}

// --- status ---

func saveCard(t *testing.T, store *taskstore.Store, card *taskstore.Task) {
	t.Helper()
	ok, err := store.SaveNew(context.Background(), card)
	require.NoError(t, err)
	require.True(t, ok)
}

// pending: 200, статус pending, без результата/длины.
func TestStatus_Pending(t *testing.T) {
	router, store, _ := taskEnv(t)
	saveCard(t, store, &taskstore.Task{Key: "k1", Status: taskstore.StatusPending, Input: "in"})

	rec := serve(t, router, http.MethodGet, "/v1/tasks/k1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	var resp StatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "k1", resp.TaskKey)
	assert.Equal(t, string(taskstore.StatusPending), resp.Status)
	assert.Empty(t, resp.RouteCompressed)
	assert.Zero(t, resp.LengthMeters)
	assert.Empty(t, resp.Error)
}

// done: 200, отдаёт ОЧИЩЕННЫЙ результат и длину.
func TestStatus_Done(t *testing.T) {
	router, store, _ := taskEnv(t)
	saveCard(t, store, &taskstore.Task{
		Key: "k2", Status: taskstore.StatusDone,
		Input: "original", Result: "cleaned", LengthMeters: 1234.5,
	})

	rec := serve(t, router, http.MethodGet, "/v1/tasks/k2", "")

	require.Equal(t, http.StatusOK, rec.Code)
	var resp StatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, string(taskstore.StatusDone), resp.Status)
	assert.Equal(t, "cleaned", resp.RouteCompressed)
	assert.Equal(t, 1234.5, resp.LengthMeters)
}

// АДВЕРСАРИАЛЬНО (C-M2): у done-задачи с длиной РОВНО 0 поле lengthMeters всё
// равно должно присутствовать в JSON, а не пропадать (было omitempty → пропадало,
// и клиент не мог отличить «длина 0» от «поля нет»).
func TestStatus_DoneZeroLengthPresent(t *testing.T) {
	router, store, _ := taskEnv(t)
	saveCard(t, store, &taskstore.Task{
		Key: "k5", Status: taskstore.StatusDone,
		Input: "in", Result: "cleaned", LengthMeters: 0,
	})

	rec := serve(t, router, http.MethodGet, "/v1/tasks/k5", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"lengthMeters"`,
		"lengthMeters должно быть в done-ответе даже при длине 0")
}

// АДВЕРСАРИАЛЬНО: done-ответ отдаёт Result (очищенный), а НЕ Input (исходный).
// Защита от опечатки, когда вернули не то поле.
func TestStatus_DoneDoesNotLeakInput(t *testing.T) {
	router, store, _ := taskEnv(t)
	saveCard(t, store, &taskstore.Task{
		Key: "k3", Status: taskstore.StatusDone,
		Input: "INPUT_ORIGINAL", Result: "RESULT_CLEANED", LengthMeters: 1,
	})

	rec := serve(t, router, http.MethodGet, "/v1/tasks/k3", "")

	var resp StatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "RESULT_CLEANED", resp.RouteCompressed)
	assert.NotEqual(t, "INPUT_ORIGINAL", resp.RouteCompressed)
}

// failed: 200, статус failed, есть error, нет результата.
func TestStatus_Failed(t *testing.T) {
	router, store, _ := taskEnv(t)
	saveCard(t, store, &taskstore.Task{
		Key: "k4", Status: taskstore.StatusFailed, Error: "decode: boom",
	})

	rec := serve(t, router, http.MethodGet, "/v1/tasks/k4", "")

	require.Equal(t, http.StatusOK, rec.Code)
	var resp StatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, string(taskstore.StatusFailed), resp.Status)
	assert.Equal(t, "decode: boom", resp.Error)
	assert.Empty(t, resp.RouteCompressed)
}

// несуществующий ключ → 404 NOT_FOUND.
func TestStatus_NotFound(t *testing.T) {
	router, _, _ := taskEnv(t)

	rec := serve(t, router, http.MethodGet, "/v1/tasks/nope", "")

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, CodeNotFound, decodeErr(t, rec).Error.Code)
}

// --- debug ---

// done со stats: 200, debug отдаётся.
func TestDebug_Done(t *testing.T) {
	router, store, _ := taskEnv(t)
	saveCard(t, store, &taskstore.Task{
		Key: "d1", Status: taskstore.StatusDone,
		Debug: []pipeline.StageStats{{Name: "dedup", PointsBefore: 100, PointsAfter: 90}},
	})

	rec := serve(t, router, http.MethodGet, "/v1/tasks/d1/debug", "")

	require.Equal(t, http.StatusOK, rec.Code)
	var resp DebugResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Debug, 1)
	assert.Equal(t, "dedup", resp.Debug[0].Name)
}

// debug несуществующей задачи → 404.
func TestDebug_NotFound(t *testing.T) {
	router, _, _ := taskEnv(t)

	rec := serve(t, router, http.MethodGet, "/v1/tasks/nope/debug", "")

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, CodeNotFound, decodeErr(t, rec).Error.Code)
}

// debug у pending-задачи (stats ещё нет) → 200 без паники, debug пустой.
func TestDebug_PendingEmpty(t *testing.T) {
	router, store, _ := taskEnv(t)
	saveCard(t, store, &taskstore.Task{Key: "d2", Status: taskstore.StatusPending})

	rec := serve(t, router, http.MethodGet, "/v1/tasks/d2/debug", "")

	require.Equal(t, http.StatusOK, rec.Code)
	var resp DebugResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp.Debug)
}
