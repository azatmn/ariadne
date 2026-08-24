package osrm

import (
	"context"
	"os"
	"testing"
	"time"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Проверка против НАСТОЯЩЕГО OSRM. Включается переменной окружения, поэтому
// в CI молчит и сети не требует:
//
//	ARIADNE_OSRM_LIVE=http://localhost:5001 go test ./internal/osrm/ -run Live -v
//
// Тесты на фальшивом сервере проверяют наше поведение, но не проверяют, верно
// ли мы поняли настоящие ответы: порядок «долгота, широта», как выглядит
// диагональ матрицы, сами ли приходят снэпнутые концы. Это выясняется только
// у живого сервиса.
func liveClient(t *testing.T, tweak func(*Config)) *Client {
	t.Helper()
	url := os.Getenv("ARIADNE_OSRM_LIVE")
	if url == "" {
		t.Skip("ARIADNE_OSRM_LIVE не задан — живая проверка пропущена")
	}
	cfg := Config{
		BaseURL:        url,
		MaxParallel:    8,
		BatchPoints:    400,
		RequestTimeout: 30 * time.Second,
		Retries:        1,
		UseTable:       TableAuto,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	c, err := New(cfg)
	require.NoError(t, err)
	return c
}

// liveTrack — настоящие точки из трека 573f42bf (индекс 8494), Ростовская
// область. Проверено: OSRM сажает их на дорогу с ошибкой 2–7 метров, то есть
// машина шла по существующей дороге, а не по полю.
func liveTrack() []geo.Point {
	t0 := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	raw := [][2]float64{
		{39.71071, 46.93966},
		{39.71048, 46.93866},
		{39.71017, 46.93771},
		{39.70985, 46.93677},
		{39.70956, 46.93578},
		{39.70923, 46.93473},
	}
	pts := make([]geo.Point, len(raw))
	for i, c := range raw {
		pts[i] = geo.Point{
			Time: t0.Add(time.Duration(i) * 5 * time.Second),
			Lon:  c[0],
			Lat:  c[1],
		}
	}
	return pts
}

// Самый первый живой тест: настоящий OSRM вообще отвечает и граф у него
// загружен. Если падает он, остальные живые тесты разбирать бессмысленно.
func TestLive_Ping(t *testing.T) {
	c := liveClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, c.Ping(ctx))
}

// Точки взяты С ДОРОГИ, значит расстояние до дороги обязано быть малым.
//
// На фальшивом сервере это не проверить: там снэп придумываем мы сами. Тест
// сторожит то, что подделать нельзя, — что мы шлём координаты в правильном
// порядке и в правильном виде. Порог 20 метров с запасом: на настоящем графе
// снэпы с дороги выходят 2–7 метров.
//
// Именно так поймалось, что «точки трассы», выдуманные для теста, на самом
// деле лежали в полях: снэп доходил до 2.7 км.
func TestLive_SnapOnRoadIsSmall(t *testing.T) {
	c := liveClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	snap, ok, warns := c.Snap(ctx, liveTrack())
	assert.Empty(t, warns)
	for i := range ok {
		require.True(t, ok[i], "точка %d осталась без снэпа", i)
		assert.Less(t, snap[i], 20.0,
			"точка %d взята с дороги, снэп обязан быть малым, получили %.1f м", i, snap[i])
	}
	t.Logf("снэпы: %.1f", snap)
}

// Главная живая проверка: матрица и попарные запросы обязаны давать ОДНО И ТО ЖЕ.
//
// Это единственный способ убедиться, что диагональ матрицы достаётся правильно.
// Ошибись мы в индексах на единицу — расстояния разъедутся, и заметить это на
// фальшивом сервере нельзя: там мы сами придумали ответ.
func TestLive_TableAndRouteAgree(t *testing.T) {
	byTable := liveClient(t, func(cfg *Config) { cfg.UseTable = TableOn })
	byRoute := liveClient(t, func(cfg *Config) { cfg.UseTable = TableOff })

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pts := liveTrack()
	// Пары не только соседние, но и разнесённые — так проверяются разные
	// клетки матрицы, а не только ближайшая к диагонали.
	var pairs []Pair
	for i := range pts {
		for j := i + 1; j < len(pts); j++ {
			pairs = append(pairs, Pair{A: pts[i], B: pts[j]})
		}
	}

	dt, okT, _ := byTable.PairDistance(ctx, pairs)
	if !byTable.UsesTable() {
		t.Skip("на этом сервере /table закрыт — сверять не с чем")
	}
	dr, okR, _ := byRoute.PairDistance(ctx, pairs)

	for i := range pairs {
		require.Equal(t, okR[i], okT[i], "пара %d: способы разошлись в самом наличии пути", i)
		if !okT[i] {
			continue
		}
		assert.InDelta(t, dr[i], dt[i], 1.0,
			"пара %d: матрица дала %.1f м, попарный запрос %.1f м", i, dt[i], dr[i])
	}
	t.Logf("сверено пар: %d", len(pairs))
}

// Геометрия маршрута с настоящего графа — три проверки в одной.
//
// Первая: линия подробнее прямой, то есть мы получили именно дорогу, а не
// отрезок между концами. Вторая: снэпнутые концы приходят тем же ответом —
// на боевом /nearest закрыт, и другого способа посадить концы дыры на дорогу
// нет. Третья: сумма звеньев сходится с заявленной длиной, а это значит, что
// координаты читаются в порядке «долгота, широта», а не наоборот.
//
// Последнее на фальшивом сервере не ловится вовсе: перепутав порядок, мы
// сравнивали бы свою же выдумку с собственной ошибкой.
func TestLive_RouteGeometryFollowsRoad(t *testing.T) {
	c := liveClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pts := liveTrack()
	r, ok := c.RouteGeometry(ctx, pts[0], pts[len(pts)-1])
	require.True(t, ok)

	assert.Greater(t, len(r.Coords), 2, "геометрия обязана быть подробнее прямой")
	require.True(t, r.HasSnapA)
	require.True(t, r.HasSnapB)

	// Концы приходят тем же ответом — это важно, потому что /nearest на боевом
	// закрыт, и другого способа посадить концы на дорогу у нас нет.
	assert.InDelta(t, pts[0].Lon, r.SnapA[0], 0.01, "снэпнутый конец рядом с исходной точкой")
	assert.InDelta(t, pts[0].Lat, r.SnapA[1], 0.01)

	// Сумма звеньев геометрии сходится с заявленной длиной — значит мы читаем
	// координаты в правильном порядке (долгота, широта), а не наоборот.
	var sum float64
	for i := range len(r.Coords) - 1 {
		a := geo.Point{Lon: r.Coords[i][0], Lat: r.Coords[i][1]}
		b := geo.Point{Lon: r.Coords[i+1][0], Lat: r.Coords[i+1][1]}
		sum += geo.Haversine(a, b)
	}
	assert.InEpsilon(t, r.Distance, sum, 0.05,
		"длина по геометрии %.0f м против заявленной %.0f м", sum, r.Distance)
}

// Большой трек: проверяем, что батчи с перехлёстом и дробление работают на
// настоящих данных, а не только на выдуманных.
func TestLive_SnapLargeTrack(t *testing.T) {
	c := liveClient(t, func(cfg *Config) { cfg.BatchPoints = 50 })
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 300 точек по дуге в Ростовской области — заведомо больше одного батча.
	pts := make([]geo.Point, 300)
	t0 := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	for i := range pts {
		pts[i] = geo.Point{
			Time: t0.Add(time.Duration(i) * 30 * time.Second),
			Lon:  39.71 - float64(i)*0.002,
			Lat:  46.94 - float64(i)*0.001,
		}
	}

	snap, ok, warns := c.Snap(ctx, pts)
	answered := 0
	for i := range ok {
		if ok[i] {
			answered++
		}
	}
	t.Logf("отвечено %d из %d, предупреждения: %v", answered, len(pts), warns)
	assert.Greater(t, answered, len(pts)*9/10,
		"на девять десятых точек снэп обязан прийти")
	assert.Len(t, snap, len(pts))
}
