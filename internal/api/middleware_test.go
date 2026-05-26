package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoverCatchesPanic(t *testing.T) {
	logger := slog.Default()

	handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something went wrong")
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode response")
	assert.Equal(t, CodeInternal, payload.Error.Code)
}

func TestRecoverNoPanic(t *testing.T) {
	logger := slog.Default()

	handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestLimitBodyAllows(t *testing.T) {
	handler := LimitBody(100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	}))

	// 10 байт — проходит
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small body"))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "small body", w.Body.String())
}

func TestLimitBodyRejects(t *testing.T) {
	handler := LimitBody(10)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	}))

	// 20 байт — превышает лимит в 10
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("this body is too big"))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	// handler получит обрезанное body (http.MaxBytesReader)
	// или ошибку при чтении
	assert.Less(t, w.Body.Len(), 20, "expected body to be truncated")
}

func TestStatusWriterUnwrap(t *testing.T) {
	inner := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: inner}

	assert.Equal(t, inner, sw.Unwrap(), "Unwrap should return the original ResponseWriter")
}

func TestLoggerPassesThrough(t *testing.T) {
	logger := slog.Default()

	handler := Logger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))

	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}

func TestRequestIDAddsHeader(t *testing.T) {
	logger := slog.Default()

	handler := RequestID(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.NotEmpty(t, w.Header().Get("X-Request-ID"), "expected X-Request-ID header")
}

func TestRequestIDUnique(t *testing.T) {
	logger := slog.Default()

	handler := RequestID(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, r1)

	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, r2)

	id1 := w1.Header().Get("X-Request-ID")
	id2 := w2.Header().Get("X-Request-ID")

	assert.NotEqual(t, id1, id2, "expected unique IDs")
}
