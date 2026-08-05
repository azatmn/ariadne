package core

import (
	"context"
	"sync"
	"testing"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRoads — подставной источник дорожных расстояний.
//
// Настоящий OSRM в тестах не нужен: проверяется не то, как он считает, а то,
// как мы поступаем с его ответами. Заодно видно, о чём именно мы спросили —
// на этом держатся проверки «лишнего не спрашиваем».
type fakeRoads struct {
	mu    sync.Mutex
	asked []Pair
	// dist задаёт ответ по расстоянию между точками пары; nil = «нет пути».
	dist func(a, b geo.Point) *float64
}

func (f *fakeRoads) PairDistance(_ context.Context, pairs []Pair) ([]float64, []bool, []string) {
	f.mu.Lock()
	f.asked = append(f.asked, pairs...)
	f.mu.Unlock()

	out := make([]float64, len(pairs))
	ok := make([]bool, len(pairs))
	for i, p := range pairs {
		if f.dist == nil {
			continue
		}
		if d := f.dist(p.A, p.B); d != nil {
			out[i], ok[i] = *d, true
		}
	}
	return out, ok, nil
}

func (f *fakeRoads) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

// roadsReturning — источник, отвечающий одним и тем же расстоянием.
func roadsReturning(m float64) *fakeRoads {
	return &fakeRoads{dist: func(_, _ geo.Point) *float64 { return &m }}
}

// roadsByFactor — дорога длиннее прямой во столько-то раз.
func roadsByFactor(k float64) *fakeRoads {
	return &fakeRoads{dist: func(a, b geo.Point) *float64 {
		d := geo.Haversine(a, b) * k
		return &d
	}}
}

func chainOf(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// ------------------------------------------------------------ CheckByRoad

func TestCheckByRoad_HonestTrackGetsNoBans(t *testing.T) {
	// Дорога чуть длиннее прямой, скорость нормальная — запрещать нечего.
	pts := drive(20, 600, 10.0, 0.0, 0.05, 0) // 5.6 км за 10 минут = 33 км/ч
	st := NewRoadState()

	added := CheckByRoad(context.Background(), roadsByFactor(1.2), pts, chainOf(20), st)

	assert.Zero(t, added)
	assert.Empty(t, st.Banned)
	for i, p := range st.Penalty {
		assert.Zero(t, p, "точка %d наказана без причины", i)
	}
}

func TestCheckByRoad_BansImpossibleTransition(t *testing.T) {
	// По прямой переход выглядит законным, но по дорогам туда 200 км —
	// за десять минут не проехать. Ровно этот случай прямая проверка
	// пропускает по построению.
	pts := drive(3, 600, 10.0, 0.0, 0.05, 0)
	st := NewRoadState()

	added := CheckByRoad(context.Background(), roadsReturning(200000), pts, chainOf(3), st)

	assert.Positive(t, added, "непроходимый переход обязан быть запрещён")
	assert.NotEmpty(t, st.Banned)

	// Обе точки перехода получают штраф: если точка ложная, переходы к ней не
	// сойдутся ни от кого, штраф накопится и цепочка обойдёт её стороной.
	assert.Positive(t, st.Penalty[0])
	assert.Positive(t, st.Penalty[1])
}

func TestCheckByRoad_BanStoresRequiredTime(t *testing.T) {
	// В запрете лежит не «нельзя», а сколько времени нужно, чтобы проехать
	// это по дорогам законно.
	pts := drive(2, 600, 10.0, 0.0, 0.05, 0)
	st := NewRoadState()
	CheckByRoad(context.Background(), roadsReturning(200000), pts, chainOf(2), st)

	require.Len(t, st.Banned, 1)
	for _, need := range st.Banned {
		// 200 км при пределе 100 км/ч — не меньше двух часов.
		assert.InDelta(t, 200000/(RoadVmaxKmh/3.6), need, 1.0)
	}
}

func TestCheckByRoad_SkipsShortTransitions(t *testing.T) {
	// Переходы короче полукилометра не проверяем: там дорога почти равна
	// прямой, а ошибка привязки к дороге перевешивает всё остальное.
	pts := drive(20, 60, 10.0, 0.0, 0.002, 0) // шаг 222 м
	roads := roadsByFactor(50)                // дорога абсурдно длинная

	added := CheckByRoad(context.Background(), roads, pts, chainOf(20), NewRoadState())
	assert.Zero(t, added, "короткие переходы не проверяются")
	assert.Zero(t, roads.count(), "и о них даже не спрашиваем")
}

func TestCheckByRoad_SkipsDividedHighwayTrap(t *testing.T) {
	// Ловушка разделённых трасс: две точки на встречных сторонах в 615 метрах
	// друг от друга дают по дорогам 19.5 км — маршрутизатор строит разворот
	// через развязку. На коротком переходе, проходимом по прямой, дорожному
	// ответу верить нельзя.
	pts := []geo.Point{
		at(0, 10.0, 0.0),
		at(60, 10.0055, 0.0), // ~612 м за минуту = 37 км/ч, вполне законно
	}
	roads := roadsReturning(19500)

	added := CheckByRoad(context.Background(), roads, pts, chainOf(2), NewRoadState())
	assert.Zero(t, added, "короткий проходимый переход не судим по дороге")
}

func TestCheckByRoad_LongTrapIsStillChecked(t *testing.T) {
	// А вот если прямая длиннее двух километров, ловушка разворота уже не
	// объясняет разницу — такой переход проверяем как обычно.
	pts := []geo.Point{
		at(0, 10.0, 0.0),
		at(60, 10.05, 0.0), // 5.6 км за минуту
	}
	added := CheckByRoad(context.Background(), roadsReturning(50000), pts, chainOf(2), NewRoadState())
	assert.Positive(t, added)
}

func TestCheckByRoad_NoRouteIsNotJudged(t *testing.T) {
	// Проезда нет вовсе. Судить не берёмся: это может быть дыра в карте,
	// а не подделка.
	pts := drive(3, 600, 10.0, 0.0, 0.05, 0)
	st := NewRoadState()
	roads := &fakeRoads{dist: func(_, _ geo.Point) *float64 { return nil }}

	added := CheckByRoad(context.Background(), roads, pts, chainOf(3), st)
	assert.Zero(t, added)
	assert.Empty(t, st.Banned)
}

func TestCheckByRoad_AsksEachPairOnce(t *testing.T) {
	// Один и тот же переход не спрашиваем дважды: на большом треке это
	// тысячи лишних запросов.
	pts := drive(10, 600, 10.0, 0.0, 0.05, 0)
	roads := roadsByFactor(1.1)
	st := NewRoadState()

	CheckByRoad(context.Background(), roads, pts, chainOf(10), st)
	first := roads.count()
	require.Positive(t, first)

	CheckByRoad(context.Background(), roads, pts, chainOf(10), st)
	assert.Equal(t, first, roads.count(), "повторный проход не должен спрашивать заново")
}

func TestCheckByRoad_NeverAsksSamePairTwice(t *testing.T) {
	// Ни один переход не спрашивается дважды за всё время — ни в одном проходе,
	// ни между проходами. Новые пары появляться могут: после первого прохода
	// наказанные точки становятся горячими, и вокруг них проверяются
	// разнесённые пары, которых раньше не было. А вот повторов быть не должно.
	pts := drive(6, 600, 10.0, 0.0, 0.05, 0)
	roads := roadsReturning(200000)
	st := NewRoadState()

	for range 3 {
		CheckByRoad(context.Background(), roads, pts, chainOf(6), st)
	}

	roads.mu.Lock()
	defer roads.mu.Unlock()
	seen := make(map[BanID]int, len(roads.asked))
	for _, p := range roads.asked {
		seen[BanKey(p.A, p.B)]++
	}
	for key, n := range seen {
		assert.Equal(t, 1, n, "переход %v спрошен %d раз", key, n)
	}
}

func TestCheckByRoad_ConvergesToZero(t *testing.T) {
	// Цикл обязан сходиться: рано или поздно новых запретов не появляется,
	// и внешний цикл проходов на этом останавливается.
	pts := drive(10, 600, 10.0, 0.0, 0.05, 0)
	roads := roadsReturning(200000)
	st := NewRoadState()

	var last int
	for pass := range 12 {
		last = CheckByRoad(context.Background(), roads, pts, chainOf(10), st)
		if last == 0 {
			t.Logf("сошлось за %d проходов", pass+1)
			break
		}
	}
	assert.Zero(t, last, "проверка обязана сойтись, а не запрещать бесконечно")
}

func TestCheckByRoad_ChecksSpacedPairsAroundHotSpots(t *testing.T) {
	// Запрещать саму пару мало: цепочка находит обход через соседнюю точку
	// того же облака, и так по кругу. Поэтому вокруг уже найденного дефекта
	// проверяются и РАЗНЕСЁННЫЕ пары — накопленный обход они вскрывают.
	pts := drive(60, 600, 10.0, 0.0, 0.05, 0)
	st := NewRoadState()
	st.Penalty[30] = 1.0 // здесь на прошлом проходе нашлась беда

	roads := roadsByFactor(1.1)
	CheckByRoad(context.Background(), roads, pts, chainOf(60), st)

	var spaced int
	roads.mu.Lock()
	for _, p := range roads.asked {
		if geo.Haversine(p.A, p.B) > 6*5560 { // дальше шести шагов
			spaced++
		}
	}
	roads.mu.Unlock()
	assert.Positive(t, spaced, "вокруг горячего места обязаны проверяться разнесённые пары")
}

func TestCheckByRoad_TeleportNeighbourhoodIsHot(t *testing.T) {
	// Сам скачок цепочка обходит: она добирается до облака по его же точкам,
	// каждый шаг короткий и законный. Но время, потраченное на обход, остаётся
	// тем же, что было у скачка, — и на разнесённых парах это вскрывается.
	var pts []geo.Point
	pts = append(pts, drive(20, 60, 10.0, 0.0, 0.002, 0)...)
	pts = append(pts, at(1260, 10.5, 0.0)) // скачок 55 км за минуту
	pts = append(pts, drive(20, 60, 10.5, 0.0, 0.002, 1320)...)

	roads := roadsByFactor(1.1)
	CheckByRoad(context.Background(), roads, pts, chainOf(len(pts)), NewRoadState())

	var spaced int
	roads.mu.Lock()
	for _, p := range roads.asked {
		if geo.Haversine(p.A, p.B) > 3000 {
			spaced++
		}
	}
	roads.mu.Unlock()
	assert.Positive(t, spaced, "рядом с телепортом проверяются разнесённые пары")
}

func TestCheckByRoad_PenaltyAccumulatesAcrossPasses(t *testing.T) {
	// Штраф копится по проходам, пока вес точки не уйдёт в минус и цепочка
	// не обойдёт её стороной.
	pts := drive(3, 600, 10.0, 0.0, 0.05, 0)
	st := NewRoadState()

	CheckByRoad(context.Background(), roadsReturning(200000), pts, chainOf(3), st)
	after1 := st.Penalty[1]
	require.Positive(t, after1)

	// Новый проход по другой цепочке — те же точки, другие пары.
	st2 := NewRoadState()
	st2.Penalty = st.Penalty
	CheckByRoad(context.Background(), roadsReturning(200000), pts, []int{0, 2}, st2)
	assert.GreaterOrEqual(t, st2.Penalty[1], after1, "штраф не должен обнуляться")
}

func TestCheckByRoad_EmptyAndTinyChains(t *testing.T) {
	pts := drive(5, 600, 10.0, 0.0, 0.05, 0)
	for _, chain := range [][]int{nil, {}, {0}} {
		assert.NotPanics(t, func() {
			CheckByRoad(context.Background(), roadsByFactor(1.1), pts, chain, NewRoadState())
		})
	}
}

func TestCheckByRoad_CancelledContext(t *testing.T) {
	pts := drive(20, 600, 10.0, 0.0, 0.05, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	roads := roadsByFactor(1.1)
	added := CheckByRoad(ctx, roads, pts, chainOf(20), NewRoadState())
	assert.Zero(t, added)
	assert.Zero(t, roads.count(), "с истёкшим бюджетом в сеть не ходим")
}

func TestCheckByRoad_NilClientIsSafe(t *testing.T) {
	pts := drive(10, 600, 10.0, 0.0, 0.05, 0)
	assert.NotPanics(t, func() {
		added := CheckByRoad(context.Background(), nil, pts, chainOf(10), NewRoadState())
		assert.Zero(t, added)
	})
}

func TestRoadState_FreshHasNoBansOrPenalty(t *testing.T) {
	st := NewRoadState()
	assert.Empty(t, st.Banned)
	assert.Empty(t, st.Penalty)
	assert.Empty(t, st.asked)
}
