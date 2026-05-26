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
	"ariadne/internal/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.Default()
}

func testConfig() *config.Config {
	return &config.Config{
		DedupDistanceMeters:  2.0,
		DedupTimeGap:         60 * time.Second,
		SimplifyMinMeters:    2.0,
		MaxPoints:            50000,
		IntersectMaxIter:     100,
		MaxSpeedKmh:          150,
		MaxLoopMeters:        100,
		MaxLoopSeconds:       10,
		MaxDecompressedBytes: 100 << 20,
		ResolveTimeout:       25 * time.Second,
	}
}

func testHandler() http.HandlerFunc {
	logger := slog.Default()
	cfg := testConfig()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	return ErrorMiddleware(logger)(h.HandleResolve)
}

func testHandlerWithConfig(cfg *config.Config) http.HandlerFunc {
	logger := slog.Default()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
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
	require.NoError(t, err)
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
	require.NoError(t, err)
	return encoded
}

func TestHandlerHappyPath(t *testing.T) {
	handler := testHandler()

	body := `{"routeCompressed":"` + testRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code, "response body: %s", w.Body.String())

	var resp ResolveResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "failed to decode response")

	assert.NotEmpty(t, resp.RouteCompressed, "routeCompressed is empty")
	assert.Greater(t, resp.LengthMeters, 0.0, "lengthMeters should be > 0")
	assert.Greater(t, resp.PointsCount, 0, "pointsCount should be > 0")
}

func TestHandlerInvalidJSON(t *testing.T) {
	handler := testHandler()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	w := httptest.NewRecorder()

	handler(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode error response")
	assert.Equal(t, CodeInvalidRequest, payload.Error.Code)
}

func TestHandlerEmptyRoute(t *testing.T) {
	handler := testHandler()

	body := `{"routeCompressed":""}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlerInvalidRoute(t *testing.T) {
	handler := testHandler()

	body := `{"routeCompressed":"not-valid-base64!!!"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode error response")
	assert.Equal(t, CodeInvalidRouteFormat, payload.Error.Code)
}

func TestHandlerTooManyPoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPoints = 2
	handler := testHandlerWithConfig(cfg)

	body := `{"routeCompressed":"` + testRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, r)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode error response")
	assert.Equal(t, CodeRouteTooLarge, payload.Error.Code)
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

	require.Equal(t, http.StatusOK, w.Code, "response body: %s", w.Body.String())

	var resp ResolveResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "failed to decode response")

	assert.NotEmpty(t, resp.Warnings, "expected warnings in response (IntersectMaxIter=0), got none")
}

func TestHandlerNoWarningsWhenClean(t *testing.T) {
	handler := testHandler()

	body := `{"routeCompressed":"` + testRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code, "response body: %s", w.Body.String())

	var resp ResolveResponse
	err := json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "failed to decode response")

	assert.Empty(t, resp.Warnings, "expected no warnings for clean route")
}

func TestHandlerTimeout(t *testing.T) {
	cfg := testConfig()
	cfg.ResolveTimeout = 1 * time.Nanosecond
	handler := testHandlerWithConfig(cfg)

	body := `{"routeCompressed":"` + testRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	time.Sleep(1 * time.Millisecond)
	handler(w, r)

	require.Equal(t, http.StatusInternalServerError, w.Code, "response body: %s", w.Body.String())

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode error response")
	assert.Equal(t, CodeInternal, payload.Error.Code)
	assert.Equal(t, "processing timeout", payload.Error.Message)
}

func TestHandlerTooFewPointsAfterPipeline(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSpeedKmh = 0.001
	handler := testHandlerWithConfig(cfg)

	body := `{"routeCompressed":"` + testRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()

	handler(w, r)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "response body: %s", w.Body.String())

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode error response")
	assert.Equal(t, CodeUnprocessableRoute, payload.Error.Code)
}

func TestHandlerMaxBytesError(t *testing.T) {
	logger := slog.Default()
	cfg := testConfig()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	handler := LimitBody(50)(ErrorMiddleware(logger)(h.HandleResolve))

	bigBody := `{"routeCompressed":"` + strings.Repeat("x", 100) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(bigBody))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	require.Equal(t, http.StatusBadRequest, w.Code, "response body: %s", w.Body.String())

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode error")
	assert.Equal(t, CodeInvalidRequest, payload.Error.Code)
	assert.Equal(t, "request body too large", payload.Error.Message)
}

func TestMiddlewareUnexpectedError(t *testing.T) {
	logger := slog.Default()

	handler := ErrorMiddleware(logger)(func(w http.ResponseWriter, r *http.Request) error {
		return fmt.Errorf("something unexpected")
	})

	r := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()

	handler(w, r)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var payload ErrorPayload
	err := json.NewDecoder(w.Body).Decode(&payload)
	require.NoError(t, err, "failed to decode error response")
	assert.Equal(t, CodeInternal, payload.Error.Code)
}
