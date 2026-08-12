package osrm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Тесты поднимают фальшивый OSRM на локальном порту. В настоящую сеть не ходят:
// нам важно поведение клиента (повторы, дробление, отмена), а не то, как считает
// настоящий движок.

// newClient — клиент, смотрящий на переданный тестовый сервер.
func newClient(t *testing.T, srv *httptest.Server, tweak func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		BaseURL:        srv.URL,
		MaxParallel:    4,
		BatchPoints:    4,
		RequestTimeout: 2 * time.Second,
		Retries:        2,
		UseTable:       TableAuto,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	c, err := New(cfg)
	require.NoError(t, err)
	return c
}

// track — n точек по прямой, время идёт с шагом в минуту.
func track(n int) []geo.Point {
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	pts := make([]geo.Point, n)
	for i := range pts {
		pts[i] = geo.Point{
			Time: t0.Add(time.Duration(i) * time.Minute),
			Lon:  37.6 + float64(i)*0.01,
			Lat:  55.7,
		}
	}
	return pts
}

// waypointsJSON — ответ /route с нужным числом точек и заданным снэпом.
func waypointsJSON(n int, dist float64) string {
	var b strings.Builder
	b.WriteString(`{"code":"Ok","waypoints":[`)
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"distance":%g,"location":[37.6,55.7]}`, dist)
	}
	b.WriteString(`],"routes":[{"distance":1000,"duration":60,`)
	b.WriteString(`"geometry":{"coordinates":[[37.6,55.7],[37.7,55.7]]}}]}`)
	return b.String()
}

// countCoords — сколько координат в пути запроса (по числу точек с запятой).
func countCoords(path string) int {
	i := strings.Index(path, "/driving/")
	if i < 0 {
		return 0
	}
	rest := path[i+len("/driving/"):]
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		rest = rest[:q]
	}
	return strings.Count(rest, ";") + 1
}

// ---------------------------------------------------------------- конструктор

func TestNew_RequiresBaseURL(t *testing.T) {
	_, err := New(Config{})
	assert.ErrorIs(t, err, ErrNotConfigured)
}

func TestNew_FillsSaneDefaults(t *testing.T) {
	c, err := New(Config{BaseURL: "http://example"})
	require.NoError(t, err)
	assert.Equal(t, 400, c.BatchPoints(), "батч по умолчанию как на боевом")
	assert.True(t, c.UsesTable(), "в режиме по умолчанию матрицы пробуем")
}

func TestNew_TableOffDisablesMatrix(t *testing.T) {
	c, err := New(Config{BaseURL: "http://example", UseTable: TableOff})
	require.NoError(t, err)
	assert.False(t, c.UsesTable())
}

// ------------------------------------------------------------------- снэпы

func TestSnap_ReturnsDistancePerPoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(waypointsJSON(countCoords(r.URL.Path), 7)))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.BatchPoints = 100 })
	snap, ok, warns := c.Snap(context.Background(), track(5))

	require.Len(t, snap, 5)
	for i := range snap {
		assert.Equal(t, 7.0, snap[i])
		assert.True(t, ok[i], "точка %d должна быть отвечена", i)
	}
	assert.Empty(t, warns)
}

func TestSnap_BatchesOverlapByOnePoint(t *testing.T) {
	var sizes []int
	var mu atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := countCoords(r.URL.Path)
		mu.Add(1)
		sizes = append(sizes, n)
		_, _ = w.Write([]byte(waypointsJSON(n, 3)))
	}))
	defer srv.Close()

	// 7 точек батчами по 4 → [0,4), [3,7): стык проверяется дважды.
	c := newClient(t, srv, func(cfg *Config) { cfg.BatchPoints = 4; cfg.MaxParallel = 1 })
	snap, ok, _ := c.Snap(context.Background(), track(7))

	assert.Equal(t, int64(2), mu.Load(), "семь точек батчами по четыре — это два запроса")
	for i := range ok {
		assert.True(t, ok[i], "точка %d осталась без ответа", i)
		assert.Equal(t, 3.0, snap[i])
	}
}

func TestSnap_SplitsFailingBatchInHalf(t *testing.T) {
	// Сервер отказывает, пока в запросе больше двух точек. Клиент обязан
	// дробить батч, пока не найдёт рабочие куски.
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if countCoords(r.URL.Path) > 2 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"NoRoute"}`))
			return
		}
		_, _ = w.Write([]byte(waypointsJSON(countCoords(r.URL.Path), 5)))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.BatchPoints = 8; cfg.Retries = 0 })
	snap, ok, _ := c.Snap(context.Background(), track(5))

	for i := range ok {
		assert.True(t, ok[i], "точка %d должна быть добыта дроблением", i)
		assert.Equal(t, 5.0, snap[i])
	}
	assert.Greater(t, calls.Load(), int64(1), "дробление обязано было случиться")
}

func TestSnap_MarksUnansweredPointsInsteadOfZero(t *testing.T) {
	// Сервер не отвечает никогда. Ноль в снэпе означал бы «точка на дороге» —
	// сильный довод ЗА неё; молчание не довод ни за, ни против.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0 })
	snap, ok, warns := c.Snap(context.Background(), track(4))

	for i := range ok {
		assert.False(t, ok[i], "точка %d не отвечена — признак обязан быть false", i)
		assert.Zero(t, snap[i])
	}
	assert.NotEmpty(t, warns, "молчание сервера обязано попасть в предупреждения")
}

func TestSnap_ShrinksBatchOn414(t *testing.T) {
	// Боевой сервер принимает 400 точек и отвечает 414 на 1000 — упирается
	// в длину адреса. Лимит на разных установках свой, поэтому клиент
	// подбирает его сам, а не хранит зашитым.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if countCoords(r.URL.Path) > 100 {
			w.WriteHeader(http.StatusRequestURITooLong)
			return
		}
		_, _ = w.Write([]byte(waypointsJSON(countCoords(r.URL.Path), 2)))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.BatchPoints = 256; cfg.Retries = 0 })
	snap, ok, _ := c.Snap(context.Background(), track(300))

	assert.Less(t, c.BatchPoints(), 256, "после 414 батч обязан уменьшиться")
	// И дробление всё равно доводит дело до конца: точки добыты.
	for i := range ok {
		require.True(t, ok[i], "точка %d осталась без ответа", i)
		assert.Equal(t, 2.0, snap[i])
	}
}

func TestShrinkBatch_StopsAtFloor(t *testing.T) {
	// Ниже полусотни точек дробить нечего: выигрыш от батча пропадает,
	// а число запросов растёт.
	c := newClient(t, httptest.NewServer(http.NotFoundHandler()), func(cfg *Config) {
		cfg.BatchPoints = 64
	})
	for range 10 {
		c.shrinkBatch()
	}
	assert.GreaterOrEqual(t, c.BatchPoints(), 32, "падать до единиц клиент не должен")
}

func TestSnap_TooFewPoints(t *testing.T) {
	c := newClient(t, httptest.NewServer(http.NotFoundHandler()), nil)
	for _, pts := range [][]geo.Point{nil, {}, track(1)} {
		snap, ok, warns := c.Snap(context.Background(), pts)
		assert.Len(t, snap, len(pts))
		assert.Len(t, ok, len(pts))
		assert.Empty(t, warns)
	}
}

// -------------------------------------------------------------- расстояния

func TestPairDistance_TakesShorterDirection(t *testing.T) {
	// Одно направление 71 км (разворот через развязку), обратное 23.8 км.
	// Берём меньшее — иначе честный переход объявляется невозможным.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/table/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		d := 23800.0
		if strings.HasPrefix(strings.TrimPrefix(r.URL.Path, "/route/v1/driving/"), "37.60000") {
			d = 71100.0
		}
		fmt.Fprintf(w, `{"code":"Ok","routes":[{"distance":%g,"duration":60}],`+
			`"waypoints":[{"location":[37.6,55.7]},{"location":[37.7,55.7]}]}`, d)
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	pts := track(2)
	dist, ok, _ := c.PairDistance(context.Background(), []Pair{{A: pts[0], B: pts[1]}})

	require.True(t, ok[0])
	assert.Equal(t, 23800.0, dist[0], "берём меньшее из двух направлений")
}

func TestPairDistance_FallsBackWhenTableClosed(t *testing.T) {
	// Ровно случай боевого сервера: /table отвечает 404, /route работает.
	var tableCalls, routeCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/table/") {
			tableCalls.Add(1)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		routeCalls.Add(1)
		_, _ = w.Write([]byte(`{"code":"Ok","routes":[{"distance":1500,"duration":60}],` +
			`"waypoints":[{"location":[37.6,55.7]},{"location":[37.7,55.7]}]}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	require.True(t, c.UsesTable(), "начинаем с попытки матриц")

	pts := track(3)
	dist, ok, warns := c.PairDistance(context.Background(),
		[]Pair{{A: pts[0], B: pts[1]}, {A: pts[1], B: pts[2]}})

	require.True(t, ok[0])
	require.True(t, ok[1])
	assert.Equal(t, 1500.0, dist[0])
	assert.False(t, c.UsesTable(), "закрытую ручку клиент обязан запомнить")
	assert.Positive(t, routeCalls.Load())
	assert.NotEmpty(t, warns)

	// Второй заход не должен снова стучаться в закрытую дверь.
	before := tableCalls.Load()
	_, _, _ = c.PairDistance(context.Background(), []Pair{{A: pts[0], B: pts[1]}})
	assert.Equal(t, before, tableCalls.Load(), "в закрытую ручку больше не стучимся")
}

func TestPairDistance_UsesTableMatrix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/table/")
		// Две пары: матрица 4×4, диагональ начал и концов даёт 100 и 200.
		_, _ = w.Write([]byte(`{"code":"Ok","distances":[` +
			`[0,0,100,999],` +
			`[0,0,999,200],` +
			`[100,999,0,0],` +
			`[999,200,0,0]]}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	pts := track(3)
	dist, ok, _ := c.PairDistance(context.Background(),
		[]Pair{{A: pts[0], B: pts[1]}, {A: pts[1], B: pts[2]}})

	require.True(t, ok[0])
	require.True(t, ok[1])
	assert.Equal(t, 100.0, dist[0])
	assert.Equal(t, 200.0, dist[1])
}

func TestPairDistance_NoRouteIsNotZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/table/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"NoRoute"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0 })
	pts := track(2)
	dist, ok, warns := c.PairDistance(context.Background(), []Pair{{A: pts[0], B: pts[1]}})

	assert.False(t, ok[0], "нет проезда — это не нулевое расстояние")
	assert.Zero(t, dist[0])
	assert.NotEmpty(t, warns)
}

func TestPairDistance_Empty(t *testing.T) {
	c := newClient(t, httptest.NewServer(http.NotFoundHandler()), nil)
	dist, ok, warns := c.PairDistance(context.Background(), nil)
	assert.Empty(t, dist)
	assert.Empty(t, ok)
	assert.Empty(t, warns)
}

// ------------------------------------------------------------- геометрия

func TestRouteGeometry_ParsesPathAndSnappedEnds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.RawQuery, "geometries=geojson")
		assert.Contains(t, r.URL.RawQuery, "overview=full")
		_, _ = w.Write([]byte(`{"code":"Ok","routes":[{"distance":2500,"duration":180,` +
			`"geometry":{"coordinates":[[37.60,55.70],[37.65,55.71],[37.70,55.72]]}}],` +
			`"waypoints":[{"location":[37.601,55.701]},{"location":[37.699,55.719]}]}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	pts := track(2)
	r, ok := c.RouteGeometry(context.Background(), pts[0], pts[1])

	require.True(t, ok)
	assert.Equal(t, 2500.0, r.Distance)
	assert.Equal(t, 180.0, r.Duration)
	assert.Len(t, r.Coords, 3)
	require.True(t, r.HasSnapA)
	require.True(t, r.HasSnapB)
	assert.Equal(t, 37.601, r.SnapA[0], "концы приходят тем же ответом, /nearest не нужен")
	assert.Equal(t, 55.719, r.SnapB[1])
}

func TestRouteGeometry_NoRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"NoRoute"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0 })
	pts := track(2)
	r, ok := c.RouteGeometry(context.Background(), pts[0], pts[1])
	assert.False(t, ok)
	assert.Nil(t, r)
}

// --------------------------------------------------------- повторы и отмена

func TestGet_RetriesOnServerError(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(waypointsJSON(2, 1)))
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	_, ok, _ := c.Snap(context.Background(), track(2))

	assert.Equal(t, int64(3), calls.Load(), "две неудачи и успех с третьей попытки")
	assert.True(t, ok[0])
}

func TestGet_DoesNotRetryPermanentErrors(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusRequestURITooLong} {
		var calls atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(code)
		}))

		c := newClient(t, srv, func(cfg *Config) { cfg.BatchPoints = 100 })
		pts := track(2)
		_, _ = c.RouteGeometry(context.Background(), pts[0], pts[1])

		assert.Equal(t, int64(1), calls.Load(),
			"код %d повторять бессмысленно, ответ не изменится", code)
		srv.Close()
	}
}

func TestGet_RespectsCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(waypointsJSON(2, 1)))
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, ok, warns := c.Snap(ctx, track(2))

	assert.Less(t, time.Since(start), time.Second,
		"истёкший бюджет обязан прерывать ожидание, а не досиживать до таймаута запроса")
	assert.False(t, ok[0])
	assert.NotEmpty(t, warns)
}

func TestGet_SendsFiveDecimalCoordinates(t *testing.T) {
	// Точность координат — часть поведения алгоритма: снэп считается от того,
	// что мы отправили. Пять знаков это около 1.1 метра, как в прототипе.
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = w.Write([]byte(waypointsJSON(countCoords(r.URL.Path), 1)))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.BatchPoints = 100 })
	_, _, _ = c.Snap(context.Background(), []geo.Point{
		{Lon: 37.123456789, Lat: 55.987654321},
		{Lon: 38.1, Lat: 56.2},
	})

	assert.Contains(t, got, "37.12346,55.98765", "координаты округляются до пяти знаков")
	assert.Contains(t, got, "38.10000,56.20000", "и дописываются нулями до пяти знаков")
}

// ------------------------------------------- проверка связи и негодные ответы

func TestPing_OkWhenServerRoutes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":"Ok","routes":[{"distance":1200,"duration":90,` +
			`"geometry":{"coordinates":[[37.61,55.75],[37.62,55.76]]}}],` +
			`"waypoints":[{"location":[37.61,55.75]},{"location":[37.62,55.76]}]}`))
	}))
	defer srv.Close()

	require.NoError(t, newClient(t, srv, nil).Ping(context.Background()))
}

func TestPing_FailsWhenServerSilent(t *testing.T) {
	// Проверка связи нужна на старте: сервис, который не достучался до OSRM,
	// обязан сказать об этом сразу в логе, а не выяснять это на первой задаче.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	err := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0 }).Ping(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), srv.URL, "в ошибке должен быть адрес, куда не достучались")
}

func TestPing_FailsOnUnreachableHost(t *testing.T) {
	c, err := New(Config{BaseURL: "http://127.0.0.1:1", RequestTimeout: time.Second})
	require.NoError(t, err)
	assert.Error(t, c.Ping(context.Background()))
}

func TestSnap_RejectsMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":"Ok","waypoints":[{"distance":`))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0; cfg.BatchPoints = 100 })
	_, ok, warns := c.Snap(context.Background(), track(4))
	assert.False(t, ok[0], "на обрезанном ответе снэпов быть не может")
	assert.NotEmpty(t, warns)
}

func TestSnap_RejectsNonOkCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":"NoSegment","waypoints":[]}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0; cfg.BatchPoints = 100 })
	_, ok, warns := c.Snap(context.Background(), track(4))
	assert.False(t, ok[0])
	assert.NotEmpty(t, warns)
}

func TestSnap_RejectsWrongWaypointCount(t *testing.T) {
	// Сервер ответил успехом, но точек в ответе меньше, чем послали. Молча
	// разложить их по индексам значило бы приписать снэпы чужим точкам.
	//
	// Отвечаем на одну точку меньше запрошенного, а не фиксированным числом:
	// при фиксированном клиент дробит пачку пополам и рано или поздно попадает
	// в размер, где ответ случайно сходится, — и тест перестаёт проверять то,
	// ради чего написан.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(waypointsJSON(countCoords(r.URL.Path)-1, 5)))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0; cfg.BatchPoints = 100 })
	_, ok, warns := c.Snap(context.Background(), track(6))
	assert.False(t, ok[0], "несовпадение числа точек — отказ, а не догадки")
	assert.NotEmpty(t, warns)
}

func TestPairDistance_RejectsMalformedTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/table/") {
			_, _ = w.Write([]byte(`{"code":"Ok","distances":`)) // обрезано
			return
		}
		_, _ = w.Write([]byte(`{"code":"Ok","routes":[{"distance":700,"duration":60}],` +
			`"waypoints":[{"location":[37.6,55.7]},{"location":[37.7,55.7]}]}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	pts := track(2)
	dist, ok, warns := c.PairDistance(context.Background(), []Pair{{A: pts[0], B: pts[1]}})
	assert.NotEmpty(t, warns, "негодный ответ матрицы обязан попасть в предупреждения")
	// Матрица не удалась, но ручка не закрыта (не 404) — значит и запасного
	// пути нет: пара остаётся без ответа.
	assert.False(t, ok[0])
	assert.Zero(t, dist[0])
}

func TestPairDistance_RejectsTruncatedMatrix(t *testing.T) {
	// Матрица меньше, чем должна быть: брать из неё диагональ — читать чужие
	// клетки.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/table/")
		_, _ = w.Write([]byte(`{"code":"Ok","distances":[[0,100]]}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	pts := track(3)
	_, ok, warns := c.PairDistance(context.Background(),
		[]Pair{{A: pts[0], B: pts[1]}, {A: pts[1], B: pts[2]}})
	assert.False(t, ok[0])
	assert.NotEmpty(t, warns)
}

// Матрица нужной ВЫСОТЫ, но строки короткие.
//
// Так ответит балансировщик, отдающий чужой ответ, или сборка OSRM, которую
// позвали с `sources`/`destinations`: строк столько, сколько просили, а чисел
// в каждой — по числу целей, а не источников. Проверка высоты такое пропускает,
// и обращение `distances[i][m+i]` читает за концом строки.
//
// Цена ошибки — не порченый ответ, а упавший сервис: паника происходит в
// дочерней горутине, и `recover` воркера, который обязан превращать панику в
// упавшую задачу, до неё не достаёт.
func TestPairDistance_RejectsShortMatrixRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/table/")
		_, _ = w.Write([]byte(`{"code":"Ok","distances":[[0],[0],[0],[0]]}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	pts := track(3)
	dist, ok, warns := c.PairDistance(context.Background(),
		[]Pair{{A: pts[0], B: pts[1]}, {A: pts[1], B: pts[2]}})

	assert.False(t, ok[0], "короткая строка — это не ответ")
	assert.False(t, ok[1])
	assert.Zero(t, dist[0])
	assert.NotEmpty(t, warns, "негодная форма матрицы обязана попасть в предупреждения")
}

// Рваная матрица: высота верная, первые строки полные, одна короче.
//
// Отдельный случай от предыдущего: проверять надо КАЖДУЮ нужную строку, а не
// первую и не самую длинную. Иначе ошибка проявится только на некоторых треках
// и будет выглядеть случайной.
func TestPairDistance_RejectsRaggedMatrixRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/table/")
		_, _ = w.Write([]byte(`{"code":"Ok","distances":[` +
			`[0,0,100,999],` +
			`[0,0,999,200],` +
			`[100,999,0,0],` +
			`[999,200]]}`)) // последняя строка обрезана
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	pts := track(3)
	_, ok, warns := c.PairDistance(context.Background(),
		[]Pair{{A: pts[0], B: pts[1]}, {A: pts[1], B: pts[2]}})

	assert.False(t, ok[0])
	assert.False(t, ok[1])
	assert.NotEmpty(t, warns)
}

func TestRouteGeometry_RejectsMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":"Ok","routes":[{"distance":`))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0 })
	pts := track(2)
	r, ok := c.RouteGeometry(context.Background(), pts[0], pts[1])
	assert.False(t, ok)
	assert.Nil(t, r)
}

func TestRouteGeometry_EmptyRoutesList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":"Ok","routes":[]}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, nil)
	pts := track(2)
	_, ok := c.RouteGeometry(context.Background(), pts[0], pts[1])
	assert.False(t, ok, "успех без маршрутов — не успех")
}

func TestPairDistance_CancelledBeforeStart(t *testing.T) {
	// Бюджет задачи истёк ещё до похода в сеть. Клиент обязан вернуться
	// сразу, ничего не спросив.
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := newClient(t, srv, func(cfg *Config) { cfg.UseTable = TableOff })
	pts := track(3)
	_, ok, _ := c.PairDistance(ctx, []Pair{{A: pts[0], B: pts[1]}, {A: pts[1], B: pts[2]}})
	assert.False(t, ok[0])
	assert.Zero(t, calls.Load(), "с отменённым бюджетом в сеть ходить нельзя")
}

func TestSnap_CancelledBeforeStart(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, ok, warns := newClient(t, srv, nil).Snap(ctx, track(10))
	assert.False(t, ok[0])
	assert.NotEmpty(t, warns)
	assert.Zero(t, calls.Load())
}

// ------------------------------------------------------------- minValid

func TestMinValid(t *testing.T) {
	// В матрице «нет пути» приходит как null, что в Go даёт ноль. Отличить его
	// от настоящего нуля нельзя, поэтому ноль здесь считаем отсутствием:
	// расстояние между двумя разными точками нулевым не бывает.
	cases := []struct {
		a, b    float64
		want    float64
		wantHas bool
		why     string
	}{
		{100, 200, 100, true, "оба направления известны — берём меньшее"},
		{200, 100, 100, true, "порядок не важен"},
		{100, 0, 100, true, "обратное направление недоступно"},
		{0, 100, 100, true, "прямое направление недоступно"},
		{0, 0, 0, false, "ни одного направления"},
		{-1, 50, 50, true, "отрицательное значение — тоже отсутствие"},
	}
	for _, c := range cases {
		got, has := minValid(c.a, c.b)
		assert.Equal(t, c.wantHas, has, c.why)
		assert.Equal(t, c.want, got, c.why)
	}
}

// Счёт точек без снэпа обязан сходиться: «N из M», где N ≤ M.
//
// Раньше складывались размеры провалившихся кусков, а куски при делении
// ПЕРЕКРЫВАЮТСЯ (`[lo, mid+1]` и `[mid, hi]` делят точку mid). На живом
// прогоне это дало «не удалось получить снэп для 4736 точек из 2369» — число,
// которое само себя опровергает и обесценивает всю строку.
func TestSnap_FailedCountNeverExceedsTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"NoSegment"}`))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0; cfg.BatchPoints = 4 })
	pts := track(20)

	_, ok, warns := c.Snap(context.Background(), pts)

	answered := 0
	for _, o := range ok {
		if o {
			answered++
		}
	}
	require.Zero(t, answered, "сервер отказал на всё — отвечено не должно быть ни про одну точку")
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], "for 20 of 20 points",
		"счёт обязан сходиться, а не складывать перекрывающиеся куски: %q", warns[0])
}

// Часть точек ответилась — в счёте ровно те, что нет.
func TestSnap_FailedCountMatchesUnanswered(t *testing.T) {
	// Счётчик атомарный: обработчик зовётся из нескольких горутин сразу, и
	// обычный int здесь — гонка. Ошибка обычная и тихая: без -race тест
	// проходит и врёт.
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 { // первый батч валим целиком, остальные отвечают
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"NoSegment"}`))
			return
		}
		_, _ = w.Write([]byte(waypointsJSON(countCoords(r.URL.Path), 5)))
	}))
	defer srv.Close()

	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0; cfg.BatchPoints = 4 })
	pts := track(12)

	_, ok, warns := c.Snap(context.Background(), pts)

	missing := 0
	for _, o := range ok {
		if !o {
			missing++
		}
	}
	if missing == 0 {
		require.Empty(t, warns, "все точки отвечены — жаловаться не на что")
		return
	}
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], fmt.Sprintf("for %d of %d points", missing, len(pts)),
		"в жалобе обязано стоять реальное число неотвеченных: %q", warns[0])
}

// В тексте ошибки не должно быть километрового адреса.
//
// Живой прогон: одна строка лога уехала на несколько килобайт, потому что в
// неё попал URL со всеми координатами батча. Такой лог не читается, занимает
// место и вываливает наружу сам трек.
func TestSnap_ErrorTextStaysShort(t *testing.T) {
	// Сервер закрыт: соединение не устанавливается, и Go кладёт в текст ошибки
	// ВЕСЬ адрес запроса — то есть все координаты батча.
	srv := httptest.NewServer(http.NotFoundHandler())
	c := newClient(t, srv, func(cfg *Config) { cfg.Retries = 0; cfg.BatchPoints = 200 })
	srv.Close()
	_, _, warns := c.Snap(context.Background(), track(200))

	require.Len(t, warns, 1)
	assert.Less(t, len(warns[0]), 300,
		"жалоба на %d символов — в неё уехал адрес целиком: %q", len(warns[0]), warns[0])
}
