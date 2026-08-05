package pipeline

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"ariadne/internal/geo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimplifyStaysOnLine(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// 5 точек на прямой линии (одна долгота, разная широта)
	// Промежуточные точки на расстоянии 0 от хорды → все удалятся
	points := []geo.Point{
		{Time: t0, Lon: 37.6, Lat: 55.750},
		{Time: t0.Add(10 * time.Second), Lon: 37.6, Lat: 55.751},
		{Time: t0.Add(20 * time.Second), Lon: 37.6, Lat: 55.752},
		{Time: t0.Add(30 * time.Second), Lon: 37.6, Lat: 55.753},
		{Time: t0.Add(40 * time.Second), Lon: 37.6, Lat: 55.754},
	}

	s := Simplify{MinMeters: 5.0}
	result, _, err := s.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Len(t, result, 2, "collinear points should be reduced to first and last")
	assert.Equal(t, points[0], result[0])
	assert.Equal(t, points[4], result[1])
}

func TestSimplifyKeepsSharpTurn(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Маршрут: прямо на север, потом резко на восток
	// Точка поворота далеко от хорды A→E — должна остаться
	points := []geo.Point{
		{Time: t0, Lon: 37.600, Lat: 55.750},                       // A
		{Time: t0.Add(10 * time.Second), Lon: 37.600, Lat: 55.752}, // B: на север
		{Time: t0.Add(20 * time.Second), Lon: 37.600, Lat: 55.754}, // C: поворот
		{Time: t0.Add(30 * time.Second), Lon: 37.602, Lat: 55.754}, // D: на восток
		{Time: t0.Add(40 * time.Second), Lon: 37.604, Lat: 55.754}, // E
	}

	s := Simplify{MinMeters: 5.0}
	result, _, err := s.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Greater(t, len(result), 2, "sharp turn should preserve intermediate points")
	assert.Equal(t, points[0], result[0], "first point preserved")
	assert.Equal(t, points[4], result[len(result)-1], "last point preserved")
}

func TestSimplifyTwoPoints(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.6, Lat: 55.75},
		{Time: t0.Add(10 * time.Second), Lon: 37.601, Lat: 55.751},
	}

	s := Simplify{MinMeters: 5.0}
	result, _, err := s.Apply(context.Background(), points)
	require.NoError(t, err)
	assert.Len(t, result, 2, "two points — nothing to simplify")
}

func TestSimplifyEmpty(t *testing.T) {
	s := Simplify{MinMeters: 5.0}

	result, _, err := s.Apply(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, result)

	result, _, err = s.Apply(context.Background(), []geo.Point{{Lon: 37.0, Lat: 55.0}})
	require.NoError(t, err)
	assert.Len(t, result, 1)
}

func TestSimplifyLargeEpsilon(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// 4 точки, небольшое отклонение. С большим порогом (1000м) — все промежуточные уйдут
	points := []geo.Point{
		{Time: t0, Lon: 37.600, Lat: 55.750},
		{Time: t0.Add(10 * time.Second), Lon: 37.601, Lat: 55.751},
		{Time: t0.Add(20 * time.Second), Lon: 37.602, Lat: 55.752},
		{Time: t0.Add(30 * time.Second), Lon: 37.603, Lat: 55.753},
	}

	s := Simplify{MinMeters: 1000}
	result, _, err := s.Apply(context.Background(), points)
	require.NoError(t, err)
	assert.Len(t, result, 2, "large epsilon should reduce to first and last")
}

func TestSimplifyZeroEpsilon(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// С нулевым порогом — ничего не удалять (любое отклонение > 0)
	points := []geo.Point{
		{Time: t0, Lon: 37.600, Lat: 55.750},
		{Time: t0.Add(10 * time.Second), Lon: 37.601, Lat: 55.7505},
		{Time: t0.Add(20 * time.Second), Lon: 37.602, Lat: 55.751},
	}

	s := Simplify{MinMeters: 0}
	result, _, err := s.Apply(context.Background(), points)
	require.NoError(t, err)
	assert.Len(t, result, 3, "zero epsilon should keep all points")
}

// ---------------------------------------- защита точек, несущих смысл

// mustOf — блокнот с отмеченными точками входа.
func mustOf(pts []geo.Point, idx ...int) *RunState {
	st := &RunState{Must: map[PointKey]struct{}{}}
	for _, i := range idx {
		st.Must[KeyOf(pts[i])] = struct{}{}
	}
	return st
}

// hasPoint — есть ли точка в результате.
func hasPoint(got []geo.Point, p geo.Point) bool {
	for _, g := range got {
		if g == p {
			return true
		}
	}
	return false
}

// arc — дуга: точки уходят вбок и возвращаются, поэтому упрощение честно
// оставляет часть из них и по ним видно, что оно вообще работает.
func arc(n int) []geo.Point {
	out := make([]geo.Point, n)
	for i := range out {
		f := float64(i) / float64(n-1)
		out[i] = geo.Point{
			Time: stageT0.Add(time.Duration(i) * time.Minute),
			Lon:  10 + f*0.5,
			Lat:  55 + 0.02*f*(1-f),
		}
	}
	return out
}

func TestSimplify_KeepsMustPoint(t *testing.T) {
	// Схлопнутая стоянка лежит почти на прямой между въездом и выездом, и
	// геометрия её не отличает от лишней. Но за ней полчаса стоянки, и без
	// пометки она исчезает бесследно: замер на прототипе — семь подтверждённых
	// стоянок из 96.
	pts := []geo.Point{
		{Time: stageT0, Lon: 10.000, Lat: 55},
		{Time: stageT0.Add(time.Minute), Lon: 10.010, Lat: 55},
		{Time: stageT0.Add(33 * time.Minute), Lon: 10.020, Lat: 55}, // стоянка
		{Time: stageT0.Add(34 * time.Minute), Lon: 10.030, Lat: 55},
		{Time: stageT0.Add(35 * time.Minute), Lon: 10.040, Lat: 55},
	}
	stop := pts[2]

	plain, _, err := Simplify{MinMeters: 5}.Apply(context.Background(), pts)
	require.NoError(t, err)
	require.False(t, hasPoint(plain, stop), "для чистоты опыта геометрия должна её снимать")

	guarded, _, err := Simplify{MinMeters: 5, State: mustOf(pts, 2)}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.True(t, hasPoint(guarded, stop), "помеченная точка обязана уцелеть")
}

func TestSimplify_MustCutsTrackIntoPieces(t *testing.T) {
	// Обязательные точки режут трек на куски, и каждый упрощается сам. Это не
	// «не трогать стоянки», а общий принцип: не трогать то, что несёт смысл
	// помимо геометрии. Проверяем, что упрощение внутри кусков продолжает
	// работать, а не выключается целиком.
	pts := arc(200)
	st := mustOf(pts, 100)

	got, _, err := Simplify{MinMeters: 5, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.True(t, hasPoint(got, pts[100]), "помеченная точка на месте")
	assert.Less(t, len(got), len(pts), "внутри кусков упрощение обязано работать")
	assert.Greater(t, len(got), 2, "и не должно съедать всё")
}

func TestSimplify_ManyMustPointsKeepAll(t *testing.T) {
	// Если помечены все точки — не выброшено ничего. Крайний случай, но он
	// показывает, что пометка сильнее геометрии, а не «учитывается».
	pts := arc(50)
	all := make([]int, len(pts))
	for i := range all {
		all[i] = i
	}

	got, _, err := Simplify{MinMeters: 1000, State: mustOf(pts, all...)}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, pts, got)
}

func TestSimplify_UnknownMustIsIgnored(t *testing.T) {
	// В блокноте может лежать точка, которой в этом треке уже нет: до
	// упрощения идут стадии, которые точки удаляют. Это не ошибка.
	pts := arc(100)
	st := &RunState{Must: map[PointKey]struct{}{
		{Unix: 1, Lon: 99, Lat: 99}: {},
	}}

	got, _, err := Simplify{MinMeters: 5, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	plain, _, err := Simplify{MinMeters: 5}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, plain, got, "лишняя пометка ни на что не влияет")
}

func TestSimplify_NilStateAndEmptyMust(t *testing.T) {
	// Без блокнота и с пустым блокнотом упрощение обязано вести себя одинаково.
	pts := arc(100)

	plain, _, err := Simplify{MinMeters: 5}.Apply(context.Background(), pts)
	require.NoError(t, err)

	empty, _, err := Simplify{MinMeters: 5, State: &RunState{}}.Apply(context.Background(), pts)
	require.NoError(t, err)

	assert.Equal(t, plain, empty)
}

func TestSimplify_EndsAreAlwaysKept(t *testing.T) {
	// Концы держим всегда — это якоря маршрута. Пометка на них ничего не меняет.
	pts := arc(50)

	got, _, err := Simplify{MinMeters: 1e9, State: mustOf(pts, 0, len(pts)-1)}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Equal(t, []geo.Point{pts[0], pts[len(pts)-1]}, got)
}

// ------------------------------------------------- развёртка рекурсии

// rdpRecursive — прежняя рекурсивная реализация, оставленная эталоном.
//
// Развёртка в стек — правка ради глубины, а не ради поведения, и проверять её
// надо именно так: результат обязан совпасть с рекурсией точка в точку на
// любом треке.
func rdpRecursive(points []geo.Point, start, end int, epsilon float64, keep []bool) {
	if end-start < 2 {
		return
	}
	maxDist, maxIdx := 0.0, start
	for i := start + 1; i < end; i++ {
		if d := geo.CrossTrackDistance(points[i], points[start], points[end]); d > maxDist {
			maxDist, maxIdx = d, i
		}
	}
	if maxDist > epsilon {
		keep[maxIdx] = true
		rdpRecursive(points, start, maxIdx, epsilon, keep)
		rdpRecursive(points, maxIdx, end, epsilon, keep)
	}
}

func simplifyRecursive(points []geo.Point, epsilon float64) []geo.Point {
	if len(points) <= 2 {
		return points
	}
	keep := make([]bool, len(points))
	keep[0], keep[len(points)-1] = true, true
	rdpRecursive(points, 0, len(points)-1, epsilon, keep)

	out := make([]geo.Point, 0, len(points))
	for i, k := range keep {
		if k {
			out = append(out, points[i])
		}
	}
	return out
}

func TestSimplify_MatchesRecursion(t *testing.T) {
	// Псевдослучайные треки с постоянным зерном: воспроизводимо и при этом
	// покрывает формы, которые руками не придумаешь.
	rng := rand.New(rand.NewSource(20260805))

	for _, n := range []int{3, 5, 17, 64, 200, 999} {
		for _, eps := range []float64{0, 1, 5, 50, 500} {
			pts := make([]geo.Point, n)
			lon, lat := 37.0, 55.0
			for i := range pts {
				lon += (rng.Float64() - 0.5) * 0.01
				lat += (rng.Float64() - 0.5) * 0.01
				pts[i] = geo.Point{
					Time: stageT0.Add(time.Duration(i) * time.Second),
					Lon:  lon,
					Lat:  lat,
				}
			}

			got, _, err := Simplify{MinMeters: eps}.Apply(context.Background(), pts)
			require.NoError(t, err)
			assert.Equal(t, simplifyRecursive(pts, eps), got,
				"развёртка разошлась с рекурсией: n=%d eps=%.0f", n, eps)
		}
	}
}

func TestSimplify_DeepTrackDoesNotOverflow(t *testing.T) {
	// Худший случай для рекурсии: каждая точка чуть в стороне от хорды, и
	// разбиение идёт по одной точке за раз — глубина равна длине трека.
	// На пределе `MAX_POINTS` это пятьдесят тысяч кадров стека.
	const n = 50000
	pts := make([]geo.Point, n)
	for i := range pts {
		pts[i] = geo.Point{
			Time: stageT0.Add(time.Duration(i) * time.Second),
			Lon:  37 + float64(i)*0.0001,
			Lat:  55 + float64(i)*float64(n-i)*1e-10,
		}
	}

	assert.NotPanics(t, func() {
		got, _, err := Simplify{MinMeters: 1}.Apply(context.Background(), pts)
		require.NoError(t, err)
		assert.NotEmpty(t, got)
	})
}

func TestSimplify_ResultIsSubsequence(t *testing.T) {
	// Упрощение только выбрасывает точки: ни переставлять, ни выдумывать их
	// оно не имеет права — иначе километраж поедет.
	pts := arc(300)
	st := mustOf(pts, 50, 150, 250)

	got, _, err := Simplify{MinMeters: 5, State: st}.Apply(context.Background(), pts)
	require.NoError(t, err)

	k := 0
	for _, p := range got {
		for k < len(pts) && pts[k] != p {
			k++
		}
		require.Less(t, k, len(pts), "точка %v не из входа или порядок нарушен", p)
		k++
	}
}

func BenchmarkSimplify(b *testing.B) {
	pts := arc(50000)
	b.ReportAllocs()
	for b.Loop() {
		Simplify{MinMeters: 5}.Apply(context.Background(), pts)
	}
}

func TestSimplify_PiecesAreSimplifiedIndependently(t *testing.T) {
	// Помеченная точка не просто уцелевает — она РЕЖЕТ трек. Разница видна по
	// остальным точкам: упрощение целого куска и упрощение двух половин дают
	// разный набор, потому что хорда, от которой меряется отклонение, разная.
	//
	// Без разреза стоянка удержалась бы, но соседние точки выбирались бы
	// относительно всего трека — то есть форма вокруг неё поехала бы.
	// Разрез НЕ в вершине дуги: иначе без разреза упрощение всё равно выбрало
	// бы ту же точку первой, и опыт ничего бы не показал.
	pts := arc(200)
	const cut = 30

	whole, _, err := Simplify{MinMeters: 5, State: mustOf(pts, cut)}.Apply(context.Background(), pts)
	require.NoError(t, err)

	left, _, err := Simplify{MinMeters: 5}.Apply(context.Background(), pts[:cut+1])
	require.NoError(t, err)
	right, _, err := Simplify{MinMeters: 5}.Apply(context.Background(), pts[cut:])
	require.NoError(t, err)

	// Склеиваем половины, не задваивая точку разреза.
	want := append(append([]geo.Point{}, left...), right[1:]...)
	assert.Equal(t, want, whole, "куски обязаны упрощаться независимо друг от друга")
}

func TestSimplify_ThresholdIsInclusive(t *testing.T) {
	// Отклонение РОВНО в порог — точка лишняя. Граница включительная, и
	// проверить её надо на точном нуле: у совпадающих точек отклонение от
	// хорды в точности ноль, никакой погрешности.
	pts := make([]geo.Point, 20)
	for i := range pts {
		pts[i] = geo.Point{
			Time: stageT0.Add(time.Duration(i) * time.Second),
			Lon:  37.5,
			Lat:  55.5,
		}
	}

	got, _, err := Simplify{MinMeters: 0}.Apply(context.Background(), pts)
	require.NoError(t, err)
	assert.Len(t, got, 2, "от совпадающих точек обязаны остаться только концы")
}
