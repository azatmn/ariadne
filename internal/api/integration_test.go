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
)

func TestIntegrationRealRoute(t *testing.T) {
	routeData, err := os.ReadFile("testdata/route_3016.txt")
	if err != nil {
		t.Fatalf("cannot read test route: %v", err)
	}

	cfg := &config.Config{
		DedupDistanceMeters:  2.0,
		DedupTimeGap:         60_000_000_000,
		MaxPoints:            50_000,
		IntersectMaxIter:     10_000,
		MaxSpeedKmh:          150,
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
	if resp.PointsCount == 0 {
		t.Error("pointsCount should be > 0")
	}
	if resp.LengthMeters <= 0 {
		t.Error("lengthMeters should be > 0")
	}
	if resp.PointsCount >= 3016 {
		t.Errorf("expected fewer points after cleanup, got %d", resp.PointsCount)
	}
	if resp.RemovedPointsCount == 0 {
		t.Error("expected some points to be removed")
	}

	if id := w.Header().Get("X-Request-ID"); id == "" {
		t.Error("expected X-Request-ID header")
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("want Content-Type application/json, got %s", ct)
	}
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

			if w.Code != http.StatusOK {
				t.Errorf("want 200, got %d", w.Code)
			}
			if w.Body.String() != "ok" {
				t.Errorf("want body 'ok', got %q", w.Body.String())
			}
		})
	}
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

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}
