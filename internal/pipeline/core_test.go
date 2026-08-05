package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"ariadne/internal/core"
	"ariadne/internal/geo"
	"ariadne/internal/osrm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Тесты стадии ядра.
//
// Сама чистка проверена в пакете `core` и сверена с прототипом. Здесь
// проверяется только стыковка: перевод индексов в точки, общий блокнот для
// следующих стадий и переходники к клиенту OSRM.

var stageT0 = time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)

// road — прямой перегон: n точек через каждые stepSec секунд и stepDeg градусов.
func road(n, stepSec int, lon, lat, stepDeg float64, startSec int) []geo.Point {
	out := make([]geo.Point, n)
	for i := range out {
		out[i] = geo.Point{
			Time: stageT0.Add(time.Duration(startSec+i*stepSec) * time.Second),
			Lon:  lon + float64(i)*stepDeg,
			Lat:  lat,
		}
	}
	return out
}

// flatSnapper — один и тот же снэп на все точки.
type flatSnapper struct{ v float64 }

func (f flatSnapper) Snap(_ context.Context, pts []geo.Point) ([]float64, []bool, []string) {
	snaps, ok := make([]float64, len(pts)), make([]bool, len(pts))
	for i := range pts {
		snaps[i], ok[i] = f.v, true
	}
	return snaps, ok, nil
}

// silentRoads — маршрутизатор, который ни про что не знает: проверка переходов
// тогда молчит, и остаётся чистый выбор цепочки по весам.
type silentRoads struct{ asked int }

func (r *silentRoads) PairDistance(_ context.Context, pairs []core.Pair) ([]float64, []bool, []string) {
	r.asked += len(pairs)
	return make([]float64, len(pairs)), make([]bool, len(pairs)), nil
}

func engine() *core.Core {
	return &core.Core{Snap: flatSnapper{v: 5}, Road: &silentRoads{}}
}

// ------------------------------------------------------------------ основное

func TestCoreStage_Name(t *testing.T) {
	assert.Equal(t, "core", Core{}.Name())
}

func TestCoreStage_ReturnsKeptPoints(t *testing.T) {
	// Ядро отдаёт индексы, стадия обязана превратить их в точки — в том же
	// порядке и без потерь.
	pts := road(40, 60, 10, 0, 0.01, 0)
	st := &RunState{}

	got, warns, err := Core{Engine: engine(), State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Empty(t, warns)
	require.NotEmpty(t, got)

	// Каждая отданная точка обязана быть точкой из входа, и по возрастанию времени.
	seen := make(map[geo.Point]struct{}, len(pts))
	for _, p := range pts {
		seen[p] = struct{}{}
	}
	prev := time.Time{}
	for i, p := range got {
		assert.Contains(t, seen, p, "точка %d выдумана", i)
		assert.False(t, p.Time.Before(prev), "время пошло назад на точке %d", i)
		prev = p.Time
	}
}

func TestCoreStage_DropsGlitches(t *testing.T) {
	// Смысловая проверка: облако в поле обязано уйти, честная езда остаться.
	honest := road(30, 60, 10.0, 0, 0.01, 0)
	field := road(10, 60, 30.0, 5, 0.01, 1800)
	tail := road(30, 60, 10.3, 0, 0.01, 2400)
	pts := append(append(append([]geo.Point{}, honest...), field...), tail...)

	eng := &core.Core{
		Snap: snapByPlace{},
		Road: &silentRoads{},
	}

	got, _, err := Core{Engine: eng}.Apply(context.Background(), pts)
	require.NoError(t, err)

	for _, p := range got {
		assert.Less(t, p.Lat, 1.0, "точка облака осталась: %v", p)
	}
	assert.Less(t, len(got), len(pts), "часть точек обязана была выпасть")
}

// snapByPlace — далеко от дорог всё, что уехало по широте.
type snapByPlace struct{}

func (snapByPlace) Snap(_ context.Context, pts []geo.Point) ([]float64, []bool, []string) {
	snaps, ok := make([]float64, len(pts)), make([]bool, len(pts))
	for i, p := range pts {
		snaps[i], ok[i] = 5, true
		if p.Lat > 1 {
			snaps[i] = 2000
		}
	}
	return snaps, ok, nil
}

func TestCoreStage_EmptyInput(t *testing.T) {
	got, _, err := Core{Engine: engine()}.Apply(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCoreStage_NoEngineIsPassthrough(t *testing.T) {
	// Без движка стадия обязана пропустить точки насквозь и сказать об этом,
	// а не молча вернуть пустоту: сервис должен продолжать работать, даже
	// когда ядро не настроено.
	pts := road(10, 60, 10, 0, 0.01, 0)

	got, warns, err := Core{}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, pts, got, "точки обязаны пройти без изменений")
	assert.NotEmpty(t, warns, "молчать о пропущенной чистке нельзя")
}

func TestCoreStage_ErrorPropagates(t *testing.T) {
	// Отменённый контекст — наружу ошибкой. Конвейер обязан провалить задачу,
	// а не отдать недосчитанный маршрут как готовый.
	pts := road(40, 60, 10, 0, 0.01, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := Core{Engine: engine()}.Apply(ctx, pts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "ошибка обязана дойти как есть: %v", err)
}

func TestCoreStage_DoesNotMutateInput(t *testing.T) {
	pts := road(40, 60, 10, 0, 0.01, 0)
	before := append([]geo.Point{}, pts...)

	_, _, err := Core{Engine: engine()}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, before, pts, "входной срез трогать нельзя")
}

// --------------------------------------------------------------- блокнот

func TestCoreStage_FillsReport(t *testing.T) {
	// Отчёт ядра нужен debug-ручке и следующим стадиям.
	pts := road(40, 60, 10, 0, 0.01, 0)
	st := &RunState{}

	_, _, err := Core{Engine: engine(), State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.Positive(t, st.Report.KmBefore)
	assert.Positive(t, st.Report.KmAfter)
	assert.GreaterOrEqual(t, st.Report.RoadPasses, 1)
}

func TestCoreStage_MarksStopsAsMust(t *testing.T) {
	// Схлопнутая стоянка лежит почти на прямой между въездом и выездом, и
	// упрощение снимает её геометрией — хотя она несёт смысл помимо формы:
	// машина там стояла. Стадия обязана пометить такие точки для следующих.
	head := road(5, 60, 10, 0, 0.01, 0)
	pts := append([]geo.Point{}, head...)
	for i := range 60 { // полчаса на одном месте с лёгким дрожанием
		sign := 1.0
		if i%2 == 1 {
			sign = -1
		}
		pts = append(pts, geo.Point{
			Time: stageT0.Add(time.Duration(500+i*30) * time.Second),
			Lon:  10.06 + sign*0.0002,
			Lat:  sign * 0.0002,
		})
	}
	pts = append(pts, road(5, 60, 10.10, 0, 0.01, 3000)...)

	st := &RunState{}
	got, _, err := Core{Engine: engine(), State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	require.Equal(t, 1, st.Report.StopsTotal, "стоянка обязана была найтись")
	require.NotEmpty(t, st.Must, "стоянка обязана попасть в защищённые")

	// И помеченная точка обязана присутствовать среди отданных: помечать то,
	// чего в выходе нет, бессмысленно.
	found := 0
	for _, p := range got {
		if _, must := st.Must[KeyOf(p)]; must {
			found++
		}
	}
	assert.Equal(t, len(st.Must), found, "все помеченные точки обязаны быть в выходе")
}

func TestCoreStage_NilStateIsFine(t *testing.T) {
	// Блокнот необязателен: стадию можно гонять и в одиночку.
	pts := road(40, 60, 10, 0, 0.01, 0)
	assert.NotPanics(t, func() {
		_, _, err := Core{Engine: engine()}.Apply(context.Background(), pts)
		require.NoError(t, err)
	})
}

func TestCoreStage_StateIsOverwrittenNotAppended(t *testing.T) {
	// Блокнот живёт один прогон. Если стадию вызвать дважды, во второй раз он
	// обязан описывать второй прогон, а не сумму двух.
	pts := road(40, 60, 10, 0, 0.01, 0)
	st := &RunState{Must: map[PointKey]struct{}{{Unix: 1}: {}}}

	stage := Core{Engine: engine(), State: st}
	_, _, err := stage.Apply(context.Background(), pts)
	require.NoError(t, err)
	_, _, err = stage.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.NotContains(t, st.Must, PointKey{Unix: 1}, "мусор прошлого прогона обязан уйти")
}

// ----------------------------------------------------------------- ключ точки

func TestPointKey(t *testing.T) {
	// Индексы после каждой стадии съезжают, поэтому точку между стадиями
	// опознаём по времени и месту.
	p := geo.Point{Time: stageT0, Lon: 37.6173, Lat: 55.7558}

	assert.Equal(t, KeyOf(p), KeyOf(p), "одна и та же точка — один ключ")

	east := p
	east.Lon += 0.00001
	assert.NotEqual(t, KeyOf(p), KeyOf(east), "сдвиг по долготе меняет ключ")

	north := p
	north.Lat += 0.00001
	assert.NotEqual(t, KeyOf(p), KeyOf(north), "сдвиг по широте тоже")

	later := p
	later.Time = later.Time.Add(time.Second)
	assert.NotEqual(t, KeyOf(p), KeyOf(later), "сдвиг по времени меняет ключ")

	// Монотонные часы в метке времени не должны влиять: точки приходят из
	// разных мест, и сравнение обязано идти по секундам, а не по внутреннему
	// представлению `time.Time`.
	same := geo.Point{Time: time.Unix(stageT0.Unix(), 0).UTC(), Lon: p.Lon, Lat: p.Lat}
	assert.Equal(t, KeyOf(p), KeyOf(same), "одинаковая секунда — одинаковый ключ")
}

// -------------------------------------------------------------- переходник

// fakePairs — клиент OSRM в роли источника расстояний, запоминающий вопросы.
type fakePairs struct {
	got []osrm.Pair
}

func (f *fakePairs) PairDistance(_ context.Context, pairs []osrm.Pair) ([]float64, []bool, []string) {
	f.got = append(f.got, pairs...)
	dist, ok := make([]float64, len(pairs)), make([]bool, len(pairs))
	for i := range pairs {
		dist[i], ok[i] = 1234, true
	}
	return dist, ok, []string{"предупреждение"}
}

func TestRoadAdapter(t *testing.T) {
	// Пары ядра и пары клиента OSRM — разные типы одной формы. Переходник
	// обязан переносить их точка в точку и возвращать ответы как есть.
	inner := &fakePairs{}
	adapter := RoadFrom(inner)

	a := geo.Point{Time: stageT0, Lon: 37.1, Lat: 55.1}
	b := geo.Point{Time: stageT0.Add(time.Minute), Lon: 37.2, Lat: 55.2}

	dist, ok, warns := adapter.PairDistance(context.Background(), []core.Pair{{A: a, B: b}})

	require.Len(t, inner.got, 1)
	assert.Equal(t, a, inner.got[0].A)
	assert.Equal(t, b, inner.got[0].B)

	require.Len(t, dist, 1)
	assert.InDelta(t, 1234.0, dist[0], 1e-9)
	assert.True(t, ok[0])
	assert.Equal(t, []string{"предупреждение"}, warns)
}

func TestRoadAdapter_EmptyAndNil(t *testing.T) {
	inner := &fakePairs{}
	adapter := RoadFrom(inner)

	dist, ok, _ := adapter.PairDistance(context.Background(), nil)
	assert.Empty(t, dist)
	assert.Empty(t, ok)
	assert.Empty(t, inner.got, "пустой список спрашивать не о чем")
}

func TestCoreStage_WorksWithAdapter(t *testing.T) {
	// Сквозная стыковка: ядро через переходник действительно доходит до
	// клиента и получает ответы.
	inner := &fakePairs{}
	eng := &core.Core{Snap: flatSnapper{v: 5}, Road: RoadFrom(inner)}

	// Шаг 3.3 км за пять минут: длиннее ловушки разделённых трасс, поэтому
	// переходы доходят до проверки, а не отсекаются раньше.
	pts := road(40, 300, 10, 0, 0.03, 0)

	_, _, err := Core{Engine: eng}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.NotEmpty(t, inner.got, "ядро обязано было спросить про переходы")
}
