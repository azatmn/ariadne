package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"ariadne/internal/geo"
	"ariadne/internal/osrm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Тесты дорисовки дыр.
//
// Дорисовка — единственная стадия, которая ДОБАВЛЯЕТ точки. Поэтому здесь
// важнее обычного: что она добавляет только там, где имеет право, что времена
// внутри дорисованного монотонны, и что стоянки не сдвигаются к дороге.

// fakeRoutes — источник путей по дорогам.
type fakeRoutes struct {
	mu    sync.Mutex
	asked []Pair2
	// route возвращает путь между парой; nil означает «проезда нет».
	route func(a, b geo.Point) *osrm.Route
}

// Pair2 — пара, о которой спросили (для проверок «лишнего не спрашиваем»).
type Pair2 struct{ A, B geo.Point }

func (f *fakeRoutes) RouteGeometry(_ context.Context, a, b geo.Point) (*osrm.Route, bool) {
	f.mu.Lock()
	f.asked = append(f.asked, Pair2{a, b})
	f.mu.Unlock()

	if f.route == nil {
		return nil, false
	}
	r := f.route(a, b)
	if r == nil {
		return nil, false
	}
	return r, true
}

// roadRoute — путь по дорогам: длина ×detour, mid промежуточных вершин,
// концы посажены на дорогу со сдвигом snapShift.
//
// Геометрия ИЗОГНУТА дугой: настоящая дорога не повторяет прямую, а идеально
// прямую упрощение снимет целиком, и проверять будет нечего.
func roadRoute(a, b geo.Point, detour float64, mid int, snapShift float64) *osrm.Route {
	straight := geo.Haversine(a, b)
	const bulgeDeg = 0.002 // ~220 м вбок: заметно выше порога упрощения

	coords := make([][2]float64, 0, mid+2)
	coords = append(coords, [2]float64{a.Lon + snapShift, a.Lat})
	for i := 1; i <= mid; i++ {
		f := float64(i) / float64(mid+1)
		coords = append(coords, [2]float64{
			a.Lon + (b.Lon-a.Lon)*f,
			a.Lat + (b.Lat-a.Lat)*f + bulgeDeg*f*(1-f)*4,
		})
	}
	coords = append(coords, [2]float64{b.Lon + snapShift, b.Lat})

	return &osrm.Route{
		Distance: straight * detour,
		Duration: straight * detour / 20,
		Coords:   coords,
		SnapA:    [2]float64{a.Lon + snapShift, a.Lat},
		SnapB:    [2]float64{b.Lon + snapShift, b.Lat},
		HasSnapA: true,
		HasSnapB: true,
	}
}

// gapTrack — две точки, между которыми дыра: расстояние m метров, время sec.
func gapTrack(m float64, sec int) []geo.Point {
	return []geo.Point{
		at2(0, 10.0, 0),
		at2(sec, 10.0+m/111195.0802335329, 0),
	}
}

// routesWith — источник, отвечающий одинаковым крюком на всё.
func routesWith(detour float64, mid int) *fakeRoutes {
	return &fakeRoutes{route: func(a, b geo.Point) *osrm.Route {
		return roadRoute(a, b, detour, mid, 0)
	}}
}

// ------------------------------------------------------------------ основное

func TestFill_Name(t *testing.T) {
	assert.Equal(t, "fill_gaps", FillGaps{}.Name())
}

func TestFill_NoRouteSourceIsPassthrough(t *testing.T) {
	// Без источника путей дорисовка обязана пропустить трек насквозь и сказать
	// об этом: недосчитанный километраж — это ошибка, о которой надо знать.
	pts := gapTrack(5000, 300)

	got, warns, err := FillGaps{}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, pts, got)
	assert.NotEmpty(t, warns)
}

func TestFill_DrawsRoadThroughGap(t *testing.T) {
	// Дыра в пять километров за пять минут, дорога чуть длиннее прямой —
	// принимается, и в трек ложится её геометрия.
	pts := gapTrack(5000, 300)
	src := routesWith(1.1, 4)
	st := &RunState{}

	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	require.Len(t, src.asked, 1, "спросили ровно про одну дыру")
	assert.Len(t, got, len(pts)+4, "четыре промежуточные вершины легли в трек")
	assert.Equal(t, 1, st.Fill.Gaps)
	assert.Equal(t, 1, st.Fill.Filled)
	assert.Greater(t, geo.TotalLength(got), geo.TotalLength(pts), "километраж обязан вырасти")
}

func TestFill_ShortAndFastTransitionsAreNotGaps(t *testing.T) {
	// Дыра — это расстояние И время. Короткий шаг дорисовывать нечего, а
	// полкилометра за пару секунд — не переезд, а дрожание стоящей машины.
	cases := []struct {
		name string
		pts  []geo.Point
	}{
		{"слишком близко", gapTrack(GapMinM-1, 300)},
		{"слишком быстро", gapTrack(5000, int(GapMinSec.Seconds())-1)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := routesWith(1.1, 4)
			got, _, err := FillGaps{Routes: src, State: &RunState{}}.Apply(context.Background(), c.pts)
			require.NoError(t, err)
			assert.Equal(t, c.pts, got)
			assert.Empty(t, src.asked, "спрашивать не о чем")
		})
	}
}

func TestFill_GapThresholdsAreInclusive(t *testing.T) {
	// Пороги проверяем на самом условии, а не через геометрию: расстояние
	// считается гаверсинусом, и попасть им РОВНО в 500 метров нельзя —
	// последние биты всегда шумят. Условие же сравнивает числа буквально.
	sec := GapMinSec

	assert.True(t, isGap(GapMinM, sec), "ровно на пороге расстояния — уже дыра")
	assert.False(t, isGap(GapMinM-1e-9, sec), "чуть ближе — нет")

	assert.True(t, isGap(GapMinM, GapMinSec), "ровно на пороге времени — уже дыра")
	assert.False(t, isGap(GapMinM, GapMinSec-time.Nanosecond), "на наносекунду быстрее — нет")

	// И то же самое через стадию, чтобы условие точно применялось.
	src := routesWith(1.1, 2)
	_, _, err := FillGaps{Routes: src, State: &RunState{}}.Apply(
		context.Background(), gapTrack(GapMinM-1, 300))
	require.NoError(t, err)
	assert.Empty(t, src.asked, "ближе порога спрашивать не о чем")

	src = routesWith(1.1, 2)
	_, _, err = FillGaps{Routes: src, State: &RunState{}}.Apply(
		context.Background(), gapTrack(GapMinM+1, 300))
	require.NoError(t, err)
	assert.Len(t, src.asked, 1, "дальше порога — дыра")
}

// ------------------------------------------------------------ предохранители

func TestFill_FreeDetourAcceptsWithoutOtherChecks(t *testing.T) {
	// Дорога почти повторяет прямую — добавка не больше 30 %, ошибиться не на
	// чем. Проверка временем здесь не применяется вовсе: фура идёт 92–108 км/ч
	// по GPS, и порог 90 бил бы по честной трассе. Пользователь видел это на
	// карте как прямые в 37.8 км у Венёва.
	//
	// Расстояние 10 км за 340 секунд — это 106 км/ч, физика бы отклонила.
	pts := gapTrack(10000, 340)
	src := routesWith(1.05, 3)
	st := &RunState{}

	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Fill.Filled, "свободный крюк принимает без прочих проверок")
	assert.Greater(t, len(got), len(pts))
}

func TestFill_TooBigDetourRejected(t *testing.T) {
	// Крюк выше верхнего порога отклоняем всегда. Это закрывает дыру, которую
	// проверка временем не видит по построению: 44 км по дорогам за 31 минуту
	// — законные 84 км/ч, хотя точки стоят в полутора километрах.
	pts := gapTrack(1500, 1860)
	src := routesWith(MaxDetour+0.1, 5)
	st := &RunState{}

	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.Equal(t, pts, got, "прямая остаётся как есть")
	assert.Zero(t, st.Fill.Filled)
	assert.Equal(t, 1, st.Fill.Reasons["крюк"])
}

func TestFill_DetourThresholdsAreInclusive(t *testing.T) {
	// Ровно свободный порог — принимаем; ровно верхний — ещё не отклоняем.
	for _, c := range []struct {
		name   string
		detour float64
		filled int
	}{
		{"ровно свободный", FreeDetour, 1},
		{"ровно верхний", MaxDetour, 1},
		{"чуть выше верхнего", MaxDetour + 1e-9, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			// Время берём с запасом, чтобы решал именно крюк, а не физика.
			pts := gapTrack(5000, 3600)
			st := &RunState{}
			_, _, err := FillGaps{Routes: routesWith(c.detour, 3), State: st}.Apply(
				context.Background(), pts)
			require.NoError(t, err)
			assert.Equal(t, c.filled, st.Fill.Filled)
		})
	}
}

func TestFill_PhysicsRejectsImpossibleRoute(t *testing.T) {
	// Средняя полоса: крюк не свободный и не запредельный, решает время.
	// 20 км по дорогам за 300 секунд — это 240 км/ч, фура так не едет.
	pts := gapTrack(10000, 300)
	src := routesWith(2.0, 3)
	st := &RunState{}

	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.Equal(t, pts, got)
	assert.Equal(t, 1, st.Fill.Reasons["физика"])
}

func TestFill_SlackCoversShortGaps(t *testing.T) {
	// Допуск обязателен: полкилометра за двадцать секунд — уже 90 км/ч, и
	// дорога на сотню метров длиннее прямой объявлялась бы невозможной.
	// На `749dc894` так отклонялось 157 дыр из 594.
	pts := gapTrack(600, 24) // 90 км/ч ровно
	src := routesWith(1.5, 2)
	st := &RunState{}

	_, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, 1, st.Fill.Filled, "допуск обязан покрыть погрешность")
}

func TestFill_NoRouteLeavesStraight(t *testing.T) {
	// Проезда нет — оставляем прямую, то есть ведём себя ровно так, как если
	// бы дорисовки не было вовсе.
	pts := gapTrack(5000, 300)
	src := &fakeRoutes{route: func(geo.Point, geo.Point) *osrm.Route { return nil }}
	st := &RunState{}

	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.Equal(t, pts, got)
	assert.Equal(t, 1, st.Fill.Reasons["нет пути"])
}

// ------------------------------------------------------ времена и пометки

func TestFill_InterpolatedTimesAreMonotone(t *testing.T) {
	// Точной раскладки времени внутри дорисовки не существует, но
	// монотонность обязана сохраниться: по времени идут все проверки после.
	pts := gapTrack(20000, 1800)
	src := routesWith(1.2, 8)

	got, _, err := FillGaps{Routes: src, State: &RunState{}}.Apply(context.Background(), pts)
	require.NoError(t, err)
	require.Greater(t, len(got), len(pts))

	for i := 1; i < len(got); i++ {
		assert.False(t, got[i].Time.Before(got[i-1].Time),
			"время пошло назад на точке %d", i)
	}
	assert.Equal(t, pts[0].Time, got[0].Time, "начало дыры не двигаем")
	assert.Equal(t, pts[1].Time, got[len(got)-1].Time, "и конец тоже")
}

func TestFill_MarksSyntheticPoints(t *testing.T) {
	// Дорисованные точки обязаны быть помечены: их не было во входе, и
	// потребитель должен уметь их отличить.
	pts := gapTrack(5000, 300)
	src := routesWith(1.1, 4)
	st := &RunState{}

	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)
	require.Len(t, st.Synthetic, len(got), "пометок столько же, сколько точек")

	real, drawn := 0, 0
	for i, syn := range st.Synthetic {
		if syn {
			drawn++
			continue
		}
		real++
		assert.Contains(t, pts, got[i], "непомеченная точка обязана быть из входа")
	}
	assert.Equal(t, len(pts), real)
	assert.Equal(t, len(got)-len(pts), drawn)
}

func TestFill_ShortGeometryAddsNothing(t *testing.T) {
	// Геометрия из двух вершин — это сами концы, добавлять между ними нечего.
	pts := gapTrack(5000, 300)
	src := routesWith(1.1, 0)
	st := &RunState{}

	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Len(t, got, len(pts))
	assert.Equal(t, 1, st.Fill.Filled, "дыра всё равно считается дорисованной")
}

// ------------------------------------------------------------- стоянки

func TestFill_StopPointsAreNotSnapped(t *testing.T) {
	// Точку стоянки на дорогу НЕ сажаем. Стоянка на базе, складе или в поле
	// честно стоит вне дорожного графа, и сдвиг к ближайшей дороге стёр бы
	// само место — то, ради чего стоянка и хранится.
	pts := gapTrack(5000, 300)
	shift := 0.01 // ~1.1 км в сторону

	src := &fakeRoutes{route: func(a, b geo.Point) *osrm.Route {
		return roadRoute(a, b, 1.1, 2, shift)
	}}
	st := &RunState{Must: map[PointKey]struct{}{KeyOf(pts[0]): {}}}

	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.InDelta(t, pts[0].Lon, got[0].Lon, 1e-12, "стоянку двигать нельзя")
	assert.InDelta(t, pts[1].Lon+shift, got[len(got)-1].Lon, 1e-12,
		"обычный конец, наоборот, сажаем на дорогу")
}

func TestFill_OrdinaryEndsAreSnapped(t *testing.T) {
	// Дорисовка начинается с дороги, поэтому концы дыры к ней и подтягиваются:
	// иначе на стыке остаётся ступенька в снэп-расстояние.
	pts := gapTrack(5000, 300)
	shift := 0.001

	src := &fakeRoutes{route: func(a, b geo.Point) *osrm.Route {
		return roadRoute(a, b, 1.1, 2, shift)
	}}

	got, _, err := FillGaps{Routes: src, State: &RunState{}}.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.InDelta(t, pts[0].Lon+shift, got[0].Lon, 1e-12)
	assert.InDelta(t, pts[1].Lon+shift, got[len(got)-1].Lon, 1e-12)
}

// --------------------------------------------------------- вырожденное

func TestFill_DegenerateInput(t *testing.T) {
	for _, c := range []struct {
		name string
		pts  []geo.Point
	}{
		{"пусто", nil},
		{"одна точка", gapTrack(5000, 300)[:1]},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := routesWith(1.1, 3)
			got, _, err := FillGaps{Routes: src, State: &RunState{}}.Apply(context.Background(), c.pts)
			require.NoError(t, err)
			assert.Equal(t, c.pts, got)
			assert.Empty(t, src.asked)
		})
	}
}

func TestFill_NilStateIsFine(t *testing.T) {
	pts := gapTrack(5000, 300)
	assert.NotPanics(t, func() {
		got, _, err := FillGaps{Routes: routesWith(1.1, 3)}.Apply(context.Background(), pts)
		require.NoError(t, err)
		assert.Greater(t, len(got), len(pts))
	})
}

func TestFill_CancelledContext(t *testing.T) {
	pts := gapTrack(5000, 300)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := FillGaps{Routes: routesWith(1.1, 3), State: &RunState{}}.Apply(ctx, pts)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFill_DoesNotMutateInput(t *testing.T) {
	pts := gapTrack(5000, 300)
	before := append([]geo.Point{}, pts...)

	_, _, err := FillGaps{Routes: routesWith(1.1, 4), State: &RunState{}}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, before, pts)
}

// ----------------------------------------------- много дыр и повторяемость

// multiGapTrack — n перегонов, между каждым дыра.
func multiGapTrack(n int) []geo.Point {
	out := make([]geo.Point, 0, n)
	for i := range n {
		out = append(out, at2(i*600, 10.0+float64(i)*0.1, 0))
	}
	return out
}

func TestFill_ManyGapsAreDeterministic(t *testing.T) {
	// Запросы к маршрутизатору идут параллельно, и это НЕ должно влиять на
	// результат: километраж, по которому считают деньги, обязан быть одним и
	// тем же от прогона к прогону.
	pts := multiGapTrack(30)

	var want []geo.Point
	for range 8 {
		st := &RunState{}
		got, _, err := FillGaps{Routes: routesWith(1.2, 3), State: st}.Apply(context.Background(), pts)
		require.NoError(t, err)
		if want == nil {
			want = got
			require.Greater(t, len(got), len(pts))
			continue
		}
		assert.Equal(t, want, got, "результат обязан быть один и тот же")
	}
}

func TestFill_AsksEachGapOnce(t *testing.T) {
	// Спрашиваем ровно по разу на дыру: у боевого сервера потолок 75 запросов
	// в секунду, и лишние вопросы стоят времени задачи.
	pts := multiGapTrack(30)
	src := routesWith(1.2, 3)

	_, _, err := FillGaps{Routes: src, State: &RunState{}}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Len(t, src.asked, len(pts)-1)
}

func TestFill_ReportCountsEverything(t *testing.T) {
	// Отчёт уходит в debug-ручку, и по нему разбирают спорные маршруты.
	pts := multiGapTrack(10)
	st := &RunState{}

	got, _, err := FillGaps{Routes: routesWith(1.2, 3), State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.Equal(t, len(pts)-1, st.Fill.Gaps)
	assert.Equal(t, len(pts)-1, st.Fill.Filled)
	assert.Positive(t, st.Fill.AddedM, "прибавка к километражу обязана быть учтена")
	assert.Equal(t, len(got)-len(pts), st.Fill.AddedPts)
}

func BenchmarkFill(b *testing.B) {
	pts := multiGapTrack(500)
	src := routesWith(1.2, 20)
	b.ReportAllocs()
	for b.Loop() {
		FillGaps{Routes: src, State: &RunState{}}.Apply(context.Background(), pts)
	}
}

// ---------------------------------------- упрощение дорисованного

// zigzagRoute — путь с ЛИШНИМИ вершинами: половина из них лежит ровно на
// прямой между соседями, и упрощение обязано их снять.
func zigzagRoute(a, b geo.Point) *osrm.Route {
	const mid = 40
	coords := make([][2]float64, 0, mid+2)
	coords = append(coords, [2]float64{a.Lon, a.Lat})
	for i := 1; i <= mid; i++ {
		f := float64(i) / float64(mid+1)
		lat := a.Lat + (b.Lat-a.Lat)*f
		if i%4 == 0 { // изгиб лишь у каждой четвёртой
			lat += 0.002
		}
		coords = append(coords, [2]float64{a.Lon + (b.Lon-a.Lon)*f, lat})
	}
	coords = append(coords, [2]float64{b.Lon, b.Lat})

	return &osrm.Route{
		Distance: geo.Haversine(a, b) * 1.1,
		Coords:   coords,
		SnapA:    coords[0],
		SnapB:    coords[len(coords)-1],
		HasSnapA: true,
		HasSnapB: true,
	}
}

func TestFill_DrawnGeometryIsSimplified(t *testing.T) {
	// Маршрутизатор отдаёт путь до каждой вершины дороги и раздувает трек
	// втрое. Лишнее снимаем, но пометки обязаны остаться на своих точках —
	// иначе потребитель примет выдуманную точку за наблюдение.
	pts := gapTrack(20000, 1800)
	src := &fakeRoutes{route: func(a, b geo.Point) *osrm.Route { return zigzagRoute(a, b) }}
	st := &RunState{}

	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	require.Len(t, st.Synthetic, len(got))
	assert.Greater(t, len(got), len(pts), "что-то дорисовано")
	assert.Less(t, len(got), 42, "лишние вершины обязаны быть сняты")
	assert.Equal(t, len(got)-len(pts), st.Fill.AddedPts,
		"счётчик добавленных точек обязан учитывать упрощение")

	// Наблюдения на месте и не помечены выдуманными.
	assert.False(t, st.Synthetic[0])
	assert.False(t, st.Synthetic[len(got)-1])
	assert.Equal(t, pts[0].Time, got[0].Time)
	assert.Equal(t, pts[1].Time, got[len(got)-1].Time)

	for i, syn := range st.Synthetic {
		if syn {
			assert.NotContains(t, pts, got[i], "наблюдение помечено выдуманным")
		}
	}
}

// cancelRoutes отменяет контекст на первом же запросе: так проверяется, что
// дедлайн виден ВНУТРИ опроса дыр, а не только на входе в стадию.
type cancelRoutes struct {
	cancel context.CancelFunc
	inner  *fakeRoutes
}

func (c *cancelRoutes) RouteGeometry(ctx context.Context, a, b geo.Point) (*osrm.Route, bool) {
	c.cancel()
	return c.inner.RouteGeometry(ctx, a, b)
}

func TestFill_CancelledWhileAsking(t *testing.T) {
	// Дедлайн истёк посреди опроса. Отдавать «что успели» нельзя: часть дыр
	// осталась бы прямыми, и километраж вышел бы недосчитанным без всякого
	// признака, что это не окончательный ответ.
	pts := multiGapTrack(50)

	ctx, cancel := context.WithCancel(context.Background())
	src := &cancelRoutes{cancel: cancel, inner: routesWith(1.2, 3)}

	_, _, err := FillGaps{Routes: src, State: &RunState{}}.Apply(ctx, pts)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFill_CancelStopsAsking(t *testing.T) {
	// Отменённая задача обязана ПЕРЕСТАТЬ работать, а не досчитать до конца и
	// только потом сообщить об отмене. У боевого маршрутизатора потолок 75
	// запросов в секунду, и продолжать долбить его после отмены — значит
	// отнимать этот потолок у живых задач.
	pts := multiGapTrack(50)
	gaps := len(pts) - 1

	ctx, cancel := context.WithCancel(context.Background())
	src := &cancelRoutes{cancel: cancel, inner: routesWith(1.2, 3)}

	_, _, err := FillGaps{Routes: src, State: &RunState{}}.Apply(ctx, pts)
	require.ErrorIs(t, err, context.Canceled)

	src.inner.mu.Lock()
	asked := len(src.inner.asked)
	src.inner.mu.Unlock()

	assert.Less(t, asked, gaps, "после отмены спрошено %d дыр из %d", asked, gaps)
	t.Logf("спрошено %d дыр из %d", asked, gaps)
}

func TestFill_DrawnCopyOfObservationIsRemoved(t *testing.T) {
	// Дорисованный путь начинается ровно там же, где кончается наблюдение:
	// маршрутизатор отдаёт первой вершиной посаженный на дорогу конец, а мы
	// этот же конец в наблюдение и записываем. Получается точка-двойник —
	// то же время, те же координаты, но помеченная выдуманной.
	//
	// Её надо снять. Защищать наблюдения ПО ЗНАЧЕНИЮ здесь нельзя: двойник
	// неотличим от оригинала, попадает под защиту и остаётся в треке
	// дубликатом. Найдено сверкой с прототипом на `5f5dd0f1`.
	pts := gapTrack(20000, 1800)

	src := &fakeRoutes{route: func(a, b geo.Point) *osrm.Route {
		// Первая ВНУТРЕННЯЯ вершина совпадает с началом пути.
		coords := [][2]float64{
			{a.Lon, a.Lat},
			{a.Lon, a.Lat}, // двойник наблюдения
			{(a.Lon + b.Lon) / 2, a.Lat + 0.01},
			{b.Lon, b.Lat},
		}
		return &osrm.Route{
			Distance: geo.Haversine(a, b) * 1.2,
			Coords:   coords,
			SnapA:    coords[0],
			SnapB:    coords[len(coords)-1],
			HasSnapA: true,
			HasSnapB: true,
		}
	}}

	st := &RunState{}
	got, _, err := FillGaps{Routes: src, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	seen := map[PointKey]int{}
	for _, p := range got {
		seen[KeyOf(p)]++
	}
	for k, n := range seen {
		assert.Equal(t, 1, n, "точка %v попала в трек %d раза", k, n)
	}
	assert.False(t, st.Synthetic[0], "первая точка — наблюдение")
}

// ----------------------------------------------------------- бюджет времени

// slowRoutes — источник путей, съедающий время на каждом вопросе.
type slowRoutes struct {
	each time.Duration
	in   *fakeRoutes
}

func (s *slowRoutes) RouteGeometry(ctx context.Context, a, b geo.Point) (*osrm.Route, bool) {
	select {
	case <-time.After(s.each):
	case <-ctx.Done():
		return nil, false
	}
	return s.in.RouteGeometry(ctx, a, b)
}

func TestFill_OutOfBudgetKeepsWhatItDrew(t *testing.T) {
	// Бюджет кончился посреди опроса. Недоспрошенные дыры остаются прямыми —
	// то есть километраж занижен, но ровно так же он был занижен и без
	// дорисовки вовсе. Это ограниченная и понятная потеря, поэтому отдаём
	// что успели, а не валим задачу целиком.
	pts := multiGapTrack(40)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	src := &slowRoutes{each: 15 * time.Millisecond, in: routesWith(1.2, 3)}
	st := &RunState{}

	got, warns, err := FillGaps{Routes: src, State: st}.Apply(ctx, pts)

	require.NoError(t, err, "исчерпанный бюджет — не повод не отвечать")
	assert.NotEmpty(t, got)
	assert.True(t, st.Fill.Degraded, "о неполноте надо сказать")
	assert.Less(t, st.Fill.Filled, st.Fill.Gaps, "часть дыр осталась прямыми")

	joined := ""
	for _, w := range warns {
		joined += w
	}
	assert.Contains(t, joined, "бюджет", "предупреждение обязано быть внятным")
}

func TestFill_CancelStillFails(t *testing.T) {
	// Отмена — не исчерпанный бюджет: ждать уже некому.
	pts := multiGapTrack(50)

	ctx, cancel := context.WithCancel(context.Background())
	src := &cancelRoutes{cancel: cancel, inner: routesWith(1.2, 3)}

	_, _, err := FillGaps{Routes: src, State: &RunState{}}.Apply(ctx, pts)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFill_NotDegradedOnCleanRun(t *testing.T) {
	pts := multiGapTrack(10)
	st := &RunState{}

	_, _, err := FillGaps{Routes: routesWith(1.2, 3), State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.False(t, st.Fill.Degraded)
}
