package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ariadne/internal/codec"
	"ariadne/internal/config"
	"ariadne/internal/geo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Общие хелперы для async-тестов (tasks_test.go) и интеграционных (health/swagger).

func testLogger() *slog.Logger {
	return slog.Default()
}

func testConfig() *config.Config {
	return &config.Config{
		DedupDistanceMeters:  2.0,
		DedupTimeGap:         60 * time.Second,
		SimplifyMinMeters:    2.0,
		MaxPoints:            50000,
		StopRadiusMeters:     50,
		StopMinPoints:        5,
		MaxDecompressedBytes: 100 << 20,
		ResolveTimeout:       25 * time.Second,
	}
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

// Тест общего error-middleware (не про конкретный хендлер).
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
