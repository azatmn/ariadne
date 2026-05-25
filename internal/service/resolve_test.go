package service

import (
	"context"
	"errors"
	"testing"
	"time"

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
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(result.Points) < 2 {
		t.Errorf("expected at least 2 points, got %d", len(result.Points))
	}
	if result.LengthMeters <= 0 {
		t.Error("LengthMeters should be > 0")
	}
	if result.BeforeLenMeters <= 0 {
		t.Error("BeforeLenMeters should be > 0")
	}
	if result.BeforeCount != 10 {
		t.Errorf("BeforeCount: got %d, want 10", result.BeforeCount)
	}
}

func TestResolveTooManyPoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPoints = 5
	svc := New(cfg)

	_, err := svc.Resolve(context.Background(), testPoints(10))
	if !errors.Is(err, ErrTooManyPoints) {
		t.Errorf("expected ErrTooManyPoints, got %v", err)
	}
}

func TestResolveTooFewPointsAfterPipeline(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSpeedKmh = 0.001
	svc := New(cfg)

	points := testPoints(4)
	_, err := svc.Resolve(context.Background(), points)
	if !errors.Is(err, ErrTooFewPoints) {
		t.Errorf("expected ErrTooFewPoints, got %v", err)
	}
}

func TestResolveExactlyMaxPoints(t *testing.T) {
	cfg := testConfig()
	cfg.MaxPoints = 10
	svc := New(cfg)

	result, err := svc.Resolve(context.Background(), testPoints(10))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(result.Points) < 2 {
		t.Errorf("expected at least 2 points, got %d", len(result.Points))
	}
}

func TestResolveTwoPoints(t *testing.T) {
	svc := New(testConfig())

	result, err := svc.Resolve(context.Background(), testPoints(2))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(result.Points) != 2 {
		t.Errorf("expected 2 points, got %d", len(result.Points))
	}
	if result.BeforeCount != 2 {
		t.Errorf("BeforeCount: got %d, want 2", result.BeforeCount)
	}
}

func TestResolveBuildParams(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSpeedKmh = 200
	cfg.DedupDistanceMeters = 5.0
	cfg.IntersectMaxIter = 500
	svc := New(cfg)

	params := svc.buildParams()
	if params.MaxSpeedKmh != 200 {
		t.Errorf("MaxSpeedKmh: got %f, want 200", params.MaxSpeedKmh)
	}
	if params.DedupDistanceMeters != 5.0 {
		t.Errorf("DedupDistanceMeters: got %f, want 5.0", params.DedupDistanceMeters)
	}
	if params.IntersectMaxIter != 500 {
		t.Errorf("IntersectMaxIter: got %d, want 500", params.IntersectMaxIter)
	}
}
