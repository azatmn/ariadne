package pipeline

import (
	"context"
	"testing"
	"time"

	"ariadne/internal/geo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterBySpeedNormal(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.6173, Lat: 55.7558},
		{Time: t0.Add(10 * time.Second), Lon: 37.6174, Lat: 55.7559},
		{Time: t0.Add(20 * time.Second), Lon: 37.6175, Lat: 55.7560},
	}

	f := FilterBySpeed{MaxKmh: 150}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Len(t, result, 3, "expected 3 points (all normal speed)")
}

func TestFilterBySpeedTeleport(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.6173, Lat: 55.7558},                       // Москва
		{Time: t0.Add(1 * time.Second), Lon: 30.3141, Lat: 59.9398},  // Питер через 1с = телепорт
		{Time: t0.Add(10 * time.Second), Lon: 37.6174, Lat: 55.7559}, // обратно рядом с Москвой
	}

	f := FilterBySpeed{MaxKmh: 150}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Len(t, result, 2, "expected 2 points (teleport removed)")
}

// Точки с одинаковым временем, но ДАЛЕКО друг от друга (~12.8 км) — это глюк:
// подставная dt=1с даёт космическую скорость → вторая точка выкидывается.
func TestFilterBySpeedSameTimeFarIsGlitch(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.0, Lat: 55.0},
		{Time: t0, Lon: 37.1, Lat: 55.1}, // тот же миг, но ~12.8 км в сторону
	}

	f := FilterBySpeed{MaxKmh: 150}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Len(t, result, 1, "far same-time point is a glitch, must be dropped")
}

// Точки с одинаковым временем и БЛИЗКО (~33 м) — легит-дубль по времени (трекер
// пишет посекундно). При подставной dt=1с скорость ~120 км/ч < 150 → точка
// остаётся, а не выкидывается вслепую. Это и есть суть фикса C-H1.
func TestFilterBySpeedSameTimeCloseKept(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.6000, Lat: 55.7500},
		{Time: t0, Lon: 37.6000, Lat: 55.7503}, // тот же миг, ~33 м севернее
	}

	f := FilterBySpeed{MaxKmh: 150}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Len(t, result, 2, "close same-time point is legit, must be kept")
}

// Глюк с одинаковым временем не должен «отравить» следующую точку: далёкая G
// (тот же миг) выкидывается и НЕ становится опорной, поэтому идущая следом
// легит-точка B (позже, рядом со стартом) выживает. Защита от каскада.
func TestFilterBySpeedSameTimeGlitchNoCascade(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	a := geo.Point{Time: t0, Lon: 37.6000, Lat: 55.7500}
	g := geo.Point{Time: t0, Lon: 30.3141, Lat: 59.9398}                       // тот же миг, Питер = глюк
	b := geo.Point{Time: t0.Add(10 * time.Second), Lon: 37.6001, Lat: 55.7500} // позже, рядом с A

	f := FilterBySpeed{MaxKmh: 150}
	result, _, err := f.Apply(context.Background(), []geo.Point{a, g, b})
	require.NoError(t, err)

	require.Len(t, result, 2, "glitch dropped, both A and B kept")
	assert.Equal(t, a, result[0], "A stays")
	assert.Equal(t, b, result[1], "B survives — glitch didn't become the reference")
}

func TestFilterBySpeedEmpty(t *testing.T) {
	f := FilterBySpeed{MaxKmh: 150}

	result, _, err := f.Apply(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, result, 0, "expected 0 points")

	result, _, err = f.Apply(context.Background(), []geo.Point{{Lon: 37.0, Lat: 55.0}})
	require.NoError(t, err)
	assert.Len(t, result, 1, "expected 1 point")
}
