package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ariadne/internal/config"
	"ariadne/internal/geo"
)

func testConfig() *config.Config {
	return &config.Config{
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
		MaxPoints:           50_000,
		IntersectMaxIter:    100,
		MaxSpeedKmh:         150,
		MaxLoopMeters:       100,
		MaxLoopSeconds:      10,
	}
}

func testPoints(n int) []geo.Point {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	points := make([]geo.Point, n)
	for i := range points {
		points[i] = geo.Point{
			Time: t0.Add(time.Duration(i*10) * time.Second),
			Lon:  37.617300 + float64(i)*0.0001,
			Lat:  55.755800 + float64(i)*0.0001,
		}
	}
	return points
}

func TestResolveHappyPath(t *testing.T) {
	svc := New(testConfig())
	points := testPoints(10)

	result, err := svc.Resolve(context.Background(), points)
	require.NoError(t, err, "Resolve")

	assert.GreaterOrEqual(t, len(result.Points), 2, "expected at least 2 points")
	assert.Greater(t, result.LengthMeters, 0.0, "LengthMeters should be > 0")
	assert.Greater(t, result.BeforeLenMeters, 0.0, "BeforeLenMeters should be > 0")
	assert.Equal(t, 10, result.BeforeCount, "BeforeCount")
}

func TestResolveTooManyPoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPoints = 5
	svc := New(cfg)

	_, err := svc.Resolve(context.Background(), testPoints(10))
	require.ErrorIs(t, err, ErrTooManyPoints)
}

func TestResolveTooFewPointsAfterPipeline(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSpeedKmh = 0.001
	svc := New(cfg)

	points := testPoints(4)
	_, err := svc.Resolve(context.Background(), points)
	require.ErrorIs(t, err, ErrTooFewPoints)
}

func TestResolveExactlyMaxPoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPoints = 10
	svc := New(cfg)

	result, err := svc.Resolve(context.Background(), testPoints(10))
	require.NoError(t, err, "Resolve")
	assert.GreaterOrEqual(t, len(result.Points), 2, "expected at least 2 points")
}

func TestResolveTwoPoints(t *testing.T) {
	svc := New(testConfig())

	result, err := svc.Resolve(context.Background(), testPoints(2))
	require.NoError(t, err, "Resolve")
	assert.Len(t, result.Points, 2)
	assert.Equal(t, 2, result.BeforeCount, "BeforeCount")
}

func TestResolveBuildParams(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSpeedKmh = 200
	cfg.DedupDistanceMeters = 5.0
	cfg.IntersectMaxIter = 500
	svc := New(cfg)

	params := svc.buildParams()
	assert.Equal(t, 200.0, params.MaxSpeedKmh, "MaxSpeedKmh")
	assert.Equal(t, 5.0, params.DedupDistanceMeters, "DedupDistanceMeters")
	assert.Equal(t, 500, params.IntersectMaxIter, "IntersectMaxIter")
}
