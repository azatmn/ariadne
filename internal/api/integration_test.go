package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	_ "ariadne/swagger"

	"github.com/stretchr/testify/assert"
)

// Хендлеру для health/swagger маршрутов store не нужен (они его не трогают).

func TestIntegrationHealthEndpoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBodyBytes = 1 << 20
	logger := testLogger()
	router := NewRouter(NewHandler(nil), logger, cfg.MaxBodyBytes, false, nil)

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
	router := NewRouter(NewHandler(nil), logger, cfg.MaxBodyBytes, true, nil)

	r := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
}
