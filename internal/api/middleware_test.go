package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoverCatchesPanic(t *testing.T) {
	logger := slog.Default()

	handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("something went wrong")
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", w.Code)
	}

	var payload ErrorPayload
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.Error.Code != CodeInternal {
		t.Errorf("want code %s, got %s", CodeInternal, payload.Error.Code)
	}
}

func TestRecoverNoPanic(t *testing.T) {
	logger := slog.Default()

	handler := Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
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

	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	if w.Body.String() != "small body" {
		t.Errorf("want body passed through, got %q", w.Body.String())
	}
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
	if w.Body.Len() >= 20 {
		t.Errorf("expected body to be truncated, got %d bytes", w.Body.Len())
	}
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

	if w.Code != http.StatusCreated {
		t.Errorf("want 201, got %d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("want body 'ok', got %q", w.Body.String())
	}
}

func TestRequestIDAddsHeader(t *testing.T) {
	logger := slog.Default()

	handler := RequestID(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	id := w.Header().Get("X-Request-ID")
	if id == "" {
		t.Error("expected X-Request-ID header, got empty")
	}
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

	if id1 == id2 {
		t.Errorf("expected unique IDs, got same: %s", id1)
	}
}
