package pipeline

import (
	"context"
	"testing"
	"time"

	"ariadne/internal/geo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunFullPipeline(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		// Нормальная точка
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		// Дубль (0.1м, 1с)
		{Time: t0.Add(1 * time.Second), Lon: 37.617301, Lat: 55.755801},
		// Телепорт — 10км за 1 секунду
		{Time: t0.Add(2 * time.Second), Lon: 37.750000, Lat: 55.755800},
		// Нормальное продолжение
		{Time: t0.Add(3 * time.Second), Lon: 37.617400, Lat: 55.755900},
		{Time: t0.Add(4 * time.Second), Lon: 37.617500, Lat: 55.756000},
	}

	p := Params{
		MaxSpeedKmh:         150,
		MaxAccelKmhPerSec:   30,
		SimplifyMinMeters:   5.0,
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
	}

	pl := New(p)

	result, _, _, err := pl.Run(context.Background(), points)
	require.NoError(t, err)

	assert.Less(t, len(result), len(points),
		"expected fewer points after pipeline, got %d (was %d)", len(result), len(points))
	t.Logf("before: %d points, after: %d points", len(points), len(result))
}

func TestRunEarlyExitFewPoints(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Все точки — телепорты. После FilterBySpeed останется только первая.
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 50.000000, Lat: 60.000000},
		{Time: t0.Add(2 * time.Second), Lon: 20.000000, Lat: 40.000000},
	}

	p := Params{
		MaxSpeedKmh:         150,
		MaxAccelKmhPerSec:   30,
		SimplifyMinMeters:   5.0,
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
	}

	pl := New(p)

	result, _, _, err := pl.Run(context.Background(), points)
	require.NoError(t, err)

	assert.Less(t, len(result), 2, "expected < 2 points after speed filter")
}

func TestRunEmpty(t *testing.T) {
	p := Params{
		MaxSpeedKmh:         150,
		MaxAccelKmhPerSec:   30,
		SimplifyMinMeters:   5.0,
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
	}

	pl := New(p)

	result, _, _, err := pl.Run(context.Background(), nil)
	require.NoError(t, err)

	assert.Len(t, result, 0, "expected 0 points for nil input")
}
