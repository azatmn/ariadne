package pipeline

import (
	"context"
	"testing"
	"time"

	"ariadne/internal/geo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSortByTimeOrders(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0.Add(3 * time.Second), Lon: 37.6176, Lat: 55.7561},
		{Time: t0.Add(1 * time.Second), Lon: 37.6174, Lat: 55.7559},
		{Time: t0.Add(2 * time.Second), Lon: 37.6175, Lat: 55.7560},
		{Time: t0.Add(0 * time.Second), Lon: 37.6173, Lat: 55.7558},
	}

	s := SortByTime{}
	result, _, err := s.Apply(context.Background(), points)
	require.NoError(t, err)

	for i := 1; i < len(result); i++ {
		assert.False(t, result[i].Time.Before(result[i-1].Time),
			"point %d (%v) is before point %d (%v)", i, result[i].Time, i-1, result[i-1].Time)
	}
}

func TestSortByTimeAlreadySorted(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.6173, Lat: 55.7558},
		{Time: t0.Add(1 * time.Second), Lon: 37.6174, Lat: 55.7559},
		{Time: t0.Add(2 * time.Second), Lon: 37.6175, Lat: 55.7560},
	}

	s := SortByTime{}
	result, _, err := s.Apply(context.Background(), points)
	require.NoError(t, err)

	assert.Len(t, result, 3, "expected 3 points")
}

func TestSortByTimeEmpty(t *testing.T) {
	s := SortByTime{}

	result, _, err := s.Apply(context.Background(), nil)
	require.NoError(t, err)
	assert.Len(t, result, 0, "expected 0 points")
}
