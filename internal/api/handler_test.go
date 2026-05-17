package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ariadne/internal/codec"
	"ariadne/internal/config"
	"ariadne/internal/geo"
)

func testLogger() *slog.Logger {
	return slog.Default()
}

func testConfig() *config.Config {
	return &config.Config{
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
		SimplifyMinMeters:   2.0,
		MaxPoints:           50000,
		IntersectMaxIter:    100,
		MaxSpeedKmh:         150,
		MaxLoopMeters:       100,
		MaxLoopSeconds:      10,
	}
}

func testHandler() http.HandlerFunc {
	logger := slog.Default()
	h := NewHandler(testConfig(), logger)
	return ErrorMiddleware(logger)(h.HandleResolve)
}

func testHandlerWithConfig(cfg *config.Config) http.HandlerFunc {
	logger := slog.Default()
	h := NewHandler(cfg, logger)
	return ErrorMiddleware(logger)(h.HandleResolve)
}

func testRoute(t *testing.T) string {
	t.Helper()
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(10 * time.Second), Lon: 37.617400, Lat: 55.755900},
		{Time: t0.Add(20 * time.Second), Lon: 37.617500, Lat: 55.756000},
		{Time: t0.Add(30 * time.Second), Lon: 37.617600, Lat: 55.756100},
	}
	encoded, err := codec.Encode(points)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func testRouteWithLoop(t *testing.T) string {
	t.Helper()
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617300, Lat: 55.756100},
		{Time: t0.Add(2 * time.Second), Lon: 37.617600, Lat: 55.755950},
		{Time: t0.Add(3 * time.Second), Lon: 37.617000, Lat: 55.755950},
		{Time: t0.Add(4 * time.Second), Lon: 37.617000, Lat: 55.756300},
	}
	encoded, err := codec.Encode(points)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestHandlerHappyPath(t *testing.T) {
	handler := testHandler()

	body := `{"routeCompressed":"` + testRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ResolveResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.RouteCompressed == "" {
		t.Error("routeCompressed is empty")
	}
	if resp.LengthMeters <= 0 {
		t.Error("lengthMeters should be > 0")
	}
	if resp.PointsCount <= 0 {
		t.Error("pointsCount should be > 0")
	}
}

func TestHandlerInvalidJSON(t *testing.T) {
	handler := testHandler()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	w := httptest.NewRecorder()

	handler(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}

	var payload ErrorPayload
	json.NewDecoder(w.Body).Decode(&payload)
	if payload.Error.Code != CodeInvalidRequest {
		t.Errorf("want code %s, got %s", CodeInvalidRequest, payload.Error.Code)
	}
}

func TestHandlerEmptyRoute(t *testing.T) {
	handler := testHandler()

	body := `{"routeCompressed":""}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandlerInvalidRoute(t *testing.T) {
	handler := testHandler()

	body := `{"routeCompressed":"not-valid-base64!!!"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}

	var payload ErrorPayload
	json.NewDecoder(w.Body).Decode(&payload)
	if payload.Error.Code != CodeInvalidRouteFormat {
		t.Errorf("want code %s, got %s", CodeInvalidRouteFormat, payload.Error.Code)
	}
}

func TestHandlerTooManyPoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPoints = 2
	handler := testHandlerWithConfig(cfg)

	body := `{"routeCompressed":"` + testRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, r)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("want 413, got %d", w.Code)
	}

	var payload ErrorPayload
	json.NewDecoder(w.Body).Decode(&payload)
	if payload.Error.Code != CodeRouteTooLarge {
		t.Errorf("want code %s, got %s", CodeRouteTooLarge, payload.Error.Code)
	}
}

func TestHandlerWarningsInResponse(t *testing.T) {
	cfg := testConfig()
	cfg.IntersectMaxIter = 0
	handler := testHandlerWithConfig(cfg)

	body := `{"routeCompressed":"` + testRouteWithLoop(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ResolveResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Warnings) == 0 {
		t.Error("expected warnings in response (IntersectMaxIter=0), got none")
	}
}

func TestHandlerNoWarningsWhenClean(t *testing.T) {
	handler := testHandler()

	body := `{"routeCompressed":"` + testRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp ResolveResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Warnings) != 0 {
		t.Errorf("expected no warnings for clean route, got %v", resp.Warnings)
	}
}

func TestMiddlewareUnexpectedError(t *testing.T) {
	logger := slog.Default()

	handler := ErrorMiddleware(logger)(func(w http.ResponseWriter, r *http.Request) error {
		return fmt.Errorf("something unexpected")
	})

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()

	handler(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", w.Code)
	}

	var payload ErrorPayload
	json.NewDecoder(w.Body).Decode(&payload)
	if payload.Error.Code != CodeInternal {
		t.Errorf("want code %s, got %s", CodeInternal, payload.Error.Code)
	}
}
