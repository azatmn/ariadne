package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ariadne/internal/config"
	"ariadne/internal/service"
	_ "ariadne/swagger"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRealRoute(t *testing.T) {
	routeData, err := os.ReadFile("testdata/route_3016.txt")
	require.NoError(t, err, "cannot read test route")

	cfg := &config.Config{
		DedupDistanceMeters:  2.0,
		DedupTimeGap:         60_000_000_000,
		MaxPoints:            50_000,
		IntersectMaxIter:     10_000,
		MaxSpeedKmh:          150,
		MaxAccelKmhPerSec:    30,
		SimplifyMinMeters:    5.0,
		MaxLoopMeters:        100,
		MaxLoopSeconds:       10,
		MaxBodyBytes:         10 << 20,
		MaxDecompressedBytes: 100 << 20,
		ResolveTimeout:       25 * time.Second,
	}
	logger := testLogger()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	router := NewRouter(h, logger, cfg.MaxBodyBytes, false)

	body := `{"routeCompressed":"` + strings.TrimSpace(string(routeData)) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code, "response body: %s", w.Body.String())

	var resp ResolveResponse
	err = json.NewDecoder(w.Body).Decode(&resp)
	require.NoError(t, err, "failed to decode response")

	assert.NotEmpty(t, resp.RouteCompressed, "routeCompressed is empty")
	assert.Greater(t, resp.PointsCount, 0, "pointsCount should be > 0")
	assert.Greater(t, resp.LengthMeters, 0.0, "lengthMeters should be > 0")
	assert.Less(t, resp.PointsCount, 3016, "expected fewer points after cleanup")
	assert.Greater(t, resp.RemovedPointsCount, 0, "expected some points to be removed")

	assert.NotEmpty(t, w.Header().Get("X-Request-ID"), "expected X-Request-ID header")
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}

func TestIntegrationHealthEndpoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBodyBytes = 1 << 20
	logger := testLogger()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	router := NewRouter(h, logger, cfg.MaxBodyBytes, false)

	tests := []struct {
		name string
		path string
	}{
		{"healthz", "/healthz"},
		{"readyz", "/readyz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()

			router.ServeHTTP(w, r)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "ok", w.Body.String())
		})
	}
}

func TestIntegrationSwaggerRoute(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBodyBytes = 1 << 20
	logger := testLogger()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	router := NewRouter(h, logger, cfg.MaxBodyBytes, true)

	r := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestIntegrationMethodNotAllowed(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBodyBytes = 1 << 20
	logger := testLogger()
	h := NewHandler(service.New(cfg), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	router := NewRouter(h, logger, cfg.MaxBodyBytes, false)

	r := httptest.NewRequest(http.MethodGet, "/v1/routes/resolve-collisions", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}
