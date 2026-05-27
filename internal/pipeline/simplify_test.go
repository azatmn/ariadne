package pipeline

import (
	"context"
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
