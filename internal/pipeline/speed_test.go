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

func TestFilterBySpeedSameTime(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.0, Lat: 55.0},
		{Time: t0, Lon: 37.1, Lat: 55.1},
	}

	f := FilterBySpeed{MaxKmh: 150}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Len(t, result, 1, "expected 1 point (dt=0 skipped)")
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
