package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ariadne/internal/geo"
)

// Точки у экватора: 1° ≈ 111.2 км по обеим осям, расстояния считаются руками.
// Маршрут «на восток»: старт (0,0) → цель (0,0.1) (~11.1 км). Точка на (0,x):
// расстояние от старта = x·111195 м, до цели = (0.1−x)·111195 м.

var anchorCtx = context.Background()

func ll(lat, lon float64) geo.Point { return geo.Point{Lat: lat, Lon: lon} }

// монотонный прогресс (каждая точка дальше от старта, ближе к цели) → не трогаем.
func TestAnchor_MonotonicKept(t *testing.T) {
	in := []geo.Point{ll(0, 0), ll(0, 0.02), ll(0, 0.04), ll(0, 0.06), ll(0, 0.08), ll(0, 0.1)}

	out, _, err := RemoveAnchorBacktrack{ToleranceMeters: 500}.Apply(anchorCtx, in)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

// откат: точка дёрнулась НАЗАД к старту И ОТ цели (оба > порога) → вырезаем.
func TestAnchor_RemovesBacktrack(t *testing.T) {
	start := ll(0, 0)
	target := ll(0, 0.1)
	in := []geo.Point{start, ll(0, 0.04), ll(0, 0.01), ll(0, 0.06), target} // 0.01 — откат после 0.04

	out, _, err := RemoveAnchorBacktrack{ToleranceMeters: 500}.Apply(anchorCtx, in)
	require.NoError(t, err)
	assert.Equal(t, []geo.Point{start, ll(0, 0.04), ll(0, 0.06), target}, out)
}

// КЛЮЧЕВОЙ для способа 2: нарушено только ОДНО условие (ушла от цели, но НЕ
// назад к старту — улетела вбок/вперёд-вверх) → НЕ трогаем. Это работа
// детектора телепортов, не якорного фильтра. Заодно: дорога может петлять.
func TestAnchor_OneConditionKept(t *testing.T) {
	start := ll(0, 0)
	target := ll(0, 0.1)
	lateral := ll(0.1, 0.06) // далеко на север: от цели ушла, но от старта НЕ приблизилась
	in := []geo.Point{start, ll(0, 0.04), lateral, target}

	out, _, err := RemoveAnchorBacktrack{ToleranceMeters: 500}.Apply(anchorCtx, in)
	require.NoError(t, err)
	assert.Equal(t, in, out, "одно условие из двух — не глюк")
}

// способ 1: мелкий откат МЕНЬШЕ порога (дрожь от изгибов дорог) → не трогаем.
func TestAnchor_SmallBacktrackBelowToleranceKept(t *testing.T) {
	start := ll(0, 0)
	target := ll(0, 0.1)
	in := []geo.Point{start, ll(0, 0.04), ll(0, 0.039), ll(0, 0.06), target} // 0.039 — откат ~111 м

	out, _, err := RemoveAnchorBacktrack{ToleranceMeters: 500}.Apply(anchorCtx, in)
	require.NoError(t, err)
	assert.Equal(t, in, out, "откат меньше порога игнорируем")
}

// порог: откат ~2224 м на обеих дистанциях. tol=500 → режем, tol=3000 → держим.
func TestAnchor_Threshold(t *testing.T) {
	start := ll(0, 0)
	target := ll(0, 0.1)
	in := []geo.Point{start, ll(0, 0.06), ll(0, 0.04), target} // 0.04 — откат ~2224 м после 0.06

	cut, _, err := RemoveAnchorBacktrack{ToleranceMeters: 500}.Apply(anchorCtx, in)
	require.NoError(t, err)
	assert.Equal(t, []geo.Point{start, ll(0, 0.06), target}, cut, "откат 2224 м > порога 500 → режем")

	kept, _, err := RemoveAnchorBacktrack{ToleranceMeters: 3000}.Apply(anchorCtx, in)
	require.NoError(t, err)
	assert.Equal(t, in, kept, "откат 2224 м < порога 3000 → держим")
}

// несколько откатов подряд относительно одной опоры → все вырезаются.
func TestAnchor_MultipleBacktracks(t *testing.T) {
	start := ll(0, 0)
	target := ll(0, 0.1)
	in := []geo.Point{start, ll(0, 0.06), ll(0, 0.02), ll(0, 0.01), ll(0, 0.08), target}

	out, _, err := RemoveAnchorBacktrack{ToleranceMeters: 500}.Apply(anchorCtx, in)
	require.NoError(t, err)
	assert.Equal(t, []geo.Point{start, ll(0, 0.06), ll(0, 0.08), target}, out)
}

// концы (якоря) держим всегда, даже если старт-якорь сам по себе выброс.
func TestAnchor_AlwaysKeepsAnchors(t *testing.T) {
	start := ll(0.1, 0) // старт намеренно «улетел»
	mid := ll(0, 0.05)
	target := ll(0, 0.1)

	out, _, err := RemoveAnchorBacktrack{ToleranceMeters: 500}.Apply(anchorCtx, []geo.Point{start, mid, target})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(out), 2)
	assert.Equal(t, start, out[0])
	assert.Equal(t, target, out[len(out)-1])
}

// порог <= 0 → стадия выключена, ничего не трогаем (даже дикий откат).
func TestAnchor_Disabled(t *testing.T) {
	in := []geo.Point{ll(0, 0), ll(0, 0.08), ll(0, 0.001), ll(0, 0.1)}
	for _, tol := range []float64{0, -1} {
		out, _, err := RemoveAnchorBacktrack{ToleranceMeters: tol}.Apply(anchorCtx, in)
		require.NoError(t, err)
		assert.Equal(t, in, out, "при tol=%v стадия — no-op", tol)
	}
}

// меньше 3 точек → без изменений и без паники.
func TestAnchor_TooFewPoints(t *testing.T) {
	st := RemoveAnchorBacktrack{ToleranceMeters: 500}
	for _, in := range [][]geo.Point{
		nil,
		{},
		{ll(0, 0)},
		{ll(0, 0), ll(0, 0.1)},
	} {
		out, _, err := st.Apply(anchorCtx, in)
		require.NoError(t, err)
		assert.Equal(t, in, out)
	}
}
