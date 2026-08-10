package debugapi

import (
	"ariadne/internal/osrm"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ariadne/internal/api"
	"ariadne/internal/codec"
	"ariadne/internal/config"
	"ariadne/internal/geo"
	"ariadne/internal/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testConfig() *config.Config {
	return &config.Config{
		DedupDistanceMeters:  2.0,
		DedupTimeGap:         60 * time.Second,
		SimplifyMinMeters:    2.0,
		MaxPoints:            50000,
		StopRadiusMeters:     50,
		StopMinPoints:        5,
		MaxBodyBytes:         10 << 20,
		MaxDecompressedBytes: 100 << 20,
		ResolveTimeout:       25 * time.Second,
	}
}

func testHandler() http.HandlerFunc {
	logger := slog.Default()
	cfg := testConfig()
	h := NewHandler(service.New(cfg, nil), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	return api.ErrorMiddleware(logger)(h.HandleResolve)
}

func testHandlerWithConfig(cfg *config.Config) http.HandlerFunc {
	logger := slog.Default()
	h := NewHandler(service.New(cfg, nil), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	return api.ErrorMiddleware(logger)(h.HandleResolve)
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

// longRoute — обычный перегон: сорок точек через минуту, около километра шаг.
func longRoute(t *testing.T) string {
	t.Helper()
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	points := make([]geo.Point, 40)
	for i := range points {
		points[i] = geo.Point{
			Time: t0.Add(time.Duration(i) * time.Minute),
			Lon:  37.6173 + float64(i)*0.01,
			Lat:  55.7558,
		}
	}
	encoded, err := codec.Encode(points)
	require.NoError(t, err)
	return encoded
}

// sameSpotRoute — маршрут, все точки которого стоят на одном месте.
// После схлопывания дублей от него остаётся одна точка, то есть маршрута нет.
func sameSpotRoute(t *testing.T) string {
	t.Helper()
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	points := make([]geo.Point, 4)
	for i := range points {
		points[i] = geo.Point{
			Time: t0.Add(time.Duration(i) * time.Second),
			Lon:  37.617300,
			Lat:  55.755800,
		}
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
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.NotEmpty(t, resp.RouteCompressed)
	assert.Greater(t, resp.LengthMeters, 0.0)
	assert.Greater(t, resp.PointsCount, 0)
}

func TestHandlerInvalidJSON(t *testing.T) {
	handler := testHandler()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	handler(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var payload api.ErrorPayload
	require.NoError(t, json.NewDecoder(w.Body).Decode(&payload))
	assert.Equal(t, api.CodeInvalidRequest, payload.Error.Code)
}

func TestHandlerEmptyRoute(t *testing.T) {
	handler := testHandler()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"routeCompressed":""}`))
	w := httptest.NewRecorder()
	handler(w, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandlerInvalidRoute(t *testing.T) {
	handler := testHandler()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"routeCompressed":"not-valid-base64!!!"}`))
	w := httptest.NewRecorder()
	handler(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var payload api.ErrorPayload
	require.NoError(t, json.NewDecoder(w.Body).Decode(&payload))
	assert.Equal(t, api.CodeInvalidRouteFormat, payload.Error.Code)
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
	var payload api.ErrorPayload
	require.NoError(t, json.NewDecoder(w.Body).Decode(&payload))
	assert.Equal(t, api.CodeRouteTooLarge, payload.Error.Code)
}

// stubRouter — настроенный маршрутизатор, который ничего не знает о дорогах.
//
// Нужен, чтобы конвейер был ПОЛНЫМ: с nil-маршрутизатором чистка и дорисовка
// честно предупреждают, что их пропустили, и проверять «предупреждений нет»
// на таком составе бессмысленно.
type stubRouter struct{}

func (stubRouter) Snap(_ context.Context, pts []geo.Point) ([]float64, []bool, []string) {
	snaps, ok := make([]float64, len(pts)), make([]bool, len(pts))
	for i := range pts {
		snaps[i], ok[i] = 5, true
	}
	return snaps, ok, nil
}

func (stubRouter) PairDistance(_ context.Context, pairs []osrm.Pair) ([]float64, []bool, []string) {
	return make([]float64, len(pairs)), make([]bool, len(pairs)), nil
}

func (stubRouter) RouteGeometry(context.Context, geo.Point, geo.Point) (*osrm.Route, bool) {
	return nil, false
}

func TestHandlerNoWarningsWhenClean(t *testing.T) {
	// Маршрутизатор настроен, маршрут обычный — предупреждать не о чем.
	//
	// Трек длиннее прежнего намеренно: на четырёх точках правила по цепочке
	// срабатывают на вырожденности (каждый кусок формально огрызок), и тест
	// мерил бы не то.
	cfg := testConfig()
	h := NewHandler(service.New(cfg, stubRouter{}), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	handler := api.ErrorMiddleware(slog.Default())(h.HandleResolve)
	body := `{"routeCompressed":"` + longRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/v1/routes/resolve-collisions", strings.NewReader(body))
	w := httptest.NewRecorder()
	handler(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var resp ResolveResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Empty(t, resp.Warnings)
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
	var payload api.ErrorPayload
	require.NoError(t, json.NewDecoder(w.Body).Decode(&payload))
	assert.Equal(t, api.CodeInternal, payload.Error.Code)
	assert.Equal(t, "processing timeout", payload.Error.Message)
}

func TestHandlerTooFewPointsAfterPipeline(t *testing.T) {
	// Маршрут из точек, стоящих на одном месте: дедуп схлопывает их в одну, и
	// отдавать нечего. Прежде здесь занижался порог скорости, но фильтра
	// скорости в составе больше нет.
	cfg := testConfig()
	handler := testHandlerWithConfig(cfg)

	body := `{"routeCompressed":"` + sameSpotRoute(t) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	handler(w, r)

	require.Equal(t, http.StatusUnprocessableEntity, w.Code, "response body: %s", w.Body.String())
	var payload api.ErrorPayload
	require.NoError(t, json.NewDecoder(w.Body).Decode(&payload))
	assert.Equal(t, api.CodeUnprocessableRoute, payload.Error.Code)
}

func TestHandlerMaxBytesError(t *testing.T) {
	logger := slog.Default()
	cfg := testConfig()
	h := NewHandler(service.New(cfg, nil), cfg.MaxDecompressedBytes, cfg.ResolveTimeout)
	handler := api.LimitBody(50)(api.ErrorMiddleware(logger)(h.HandleResolve))

	bigBody := `{"routeCompressed":"` + strings.Repeat("x", 100) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(bigBody))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	require.Equal(t, http.StatusBadRequest, w.Code, "response body: %s", w.Body.String())
	var payload api.ErrorPayload
	require.NoError(t, json.NewDecoder(w.Body).Decode(&payload))
	assert.Equal(t, api.CodeInvalidRequest, payload.Error.Code)
	assert.Equal(t, "request body too large", payload.Error.Message)
}
