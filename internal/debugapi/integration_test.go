package debugapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ariadne/internal/api"
	"ariadne/internal/config"
	"ariadne/internal/service"
)

// debugRouter собирает роутер как в cmd/debugserver (та же обвязка middleware).
func debugRouter(cfg *config.Config) http.Handler {
	logger := slog.Default()
	h := NewHandler(service.New(cfg, nil), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)

	r := chi.NewRouter()
	r.Use(api.Recover(logger))
	r.Use(api.RequestID(logger))
	r.Use(api.LimitBody(cfg.MaxBodyBytes))
	errMw := api.ErrorMiddleware(logger)
	r.Get("/healthz", api.Healthz)
	r.Post("/v1/routes/resolve-collisions", errMw(h.HandleResolve))
	return r
}

func TestIntegrationRealRoute(t *testing.T) {
	routeData, err := os.ReadFile("../api/testdata/route_3016.txt")
	require.NoError(t, err, "cannot read test route")

	router := debugRouter(testConfig())

	body := `{"routeCompressed":"` + strings.TrimSpace(string(routeData)) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code, "response body: %s", w.Body.String())

	var resp ResolveResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotEmpty(t, resp.RouteCompressed)
	assert.Greater(t, resp.PointsCount, 0)
	assert.Greater(t, resp.LengthMeters, 0.0)
	assert.Less(t, resp.PointsCount, 3016, "expected fewer points after cleanup")
	assert.Greater(t, resp.RemovedPointsCount, 0)
	assert.NotEmpty(t, w.Header().Get("X-Request-ID"))
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}

func TestIntegrationMethodNotAllowed(t *testing.T) {
	router := debugRouter(testConfig())

	r := httptest.NewRequest(http.MethodGet, "/v1/routes/resolve-collisions", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}
