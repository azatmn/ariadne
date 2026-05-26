package pipeline

import (
	"context"
	"testing"
	"time"

	"ariadne/internal/geo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeduplicateRemovesDuplicates(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617301, Lat: 55.755801},  // 0.1м, 1с — дубль
		{Time: t0.Add(2 * time.Second), Lon: 37.617302, Lat: 55.755802},  // 0.1м, 1с — дубль
		{Time: t0.Add(10 * time.Second), Lon: 37.618000, Lat: 55.756000}, // далеко — не дубль
	}

	d := Deduplicate{DedupDistanceMeters: 2.0, MaxTimeGap: 60 * time.Second}
	result, _, err := d.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Len(t, result, 2, "expected 2 points (2 dupes removed)")
}

func TestDeduplicateKeepsTimeGap(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(2 * time.Hour), Lon: 37.617301, Lat: 55.755801}, // рядом, но 2 часа спустя
	}

	d := Deduplicate{DedupDistanceMeters: 2.0, MaxTimeGap: 60 * time.Second}
	result, _, err := d.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Len(t, result, 2, "expected 2 points (same place but time gap too big)")
}

func TestDeduplicateEmpty(t *testing.T) {
	d := Deduplicate{DedupDistanceMeters: 2.0, MaxTimeGap: 60 * time.Second}

	result, _, err := d.Apply(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, result, 0, "expected 0 points")
}
