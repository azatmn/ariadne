package callback

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// в тестах не тормозим на бэкоффе между ретраями.
func init() { baseBackoff = time.Millisecond }

// успех: ровно один POST, плейсхолдер подставлен, тело с taskKey+status, nil.
func TestNotify_Success(t *testing.T) {
	var calls int32
	var gotMethod, gotPath, gotCT string
	var gotBody payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL+"/{taskKey}/done", 3, time.Second, testLogger())
	require.NoError(t, c.Notify(context.Background(), "abc123", "done"))

	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
	assert.Equal(t, http.MethodPost, gotMethod)
	assert.Equal(t, "/abc123/done", gotPath, "плейсхолдер {taskKey} должен подставиться")
	assert.Equal(t, "application/json", gotCT)
	assert.Equal(t, "abc123", gotBody.TaskKey)
	assert.Equal(t, "done", gotBody.Status)
}

// 5xx дважды, потом 200 → ретраит, в итоге успех (3 захода).
func TestNotify_RetriesOn5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL+"/{taskKey}", 3, time.Second, testLogger())
	require.NoError(t, c.Notify(context.Background(), "k", "done"))
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls))
}

// всегда 5xx → исчерпывает попытки (1 + retries) и возвращает ошибку.
func TestNotify_GivesUpAfterRetries(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := New(srv.URL+"/{taskKey}", 2, time.Second, testLogger())
	require.Error(t, c.Notify(context.Background(), "k", "done"))
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "1 попытка + 2 ретрая")
}

// 4xx → НЕ ретраим (Laravel явно отверг), сразу ошибка, один заход.
func TestNotify_NoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := New(srv.URL+"/{taskKey}", 5, time.Second, testLogger())
	require.Error(t, c.Notify(context.Background(), "k", "done"))
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "на 4xx ретраев быть не должно")
}

// исход failed тоже долетает, статус в теле = failed.
func TestNotify_FailedStatus(t *testing.T) {
	var gotStatus string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p payload
		_ = json.NewDecoder(r.Body).Decode(&p)
		gotStatus = p.Status
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL+"/{taskKey}", 3, time.Second, testLogger())
	require.NoError(t, c.Notify(context.Background(), "k", "failed"))
	assert.Equal(t, "failed", gotStatus)
}

// пустой CALLBACK_URL → коллбэки выключены: nil без запросов.
func TestNotify_EmptyURLDisabled(t *testing.T) {
	c := New("", 3, time.Second, testLogger())
	assert.NoError(t, c.Notify(context.Background(), "k", "done"))
}

// отменённый контекст → быстрый выход с ошибкой, без шквала запросов.
func TestNotify_ContextCancelled(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменён до вызова

	c := New(srv.URL+"/{taskKey}", 5, time.Second, testLogger())
	require.Error(t, c.Notify(ctx, "k", "done"))
	assert.LessOrEqual(t, atomic.LoadInt32(&calls), int32(1), "при отменённом ctx не должно быть долбёжки")
}
