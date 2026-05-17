package pipeline

import (
	"log/slog"
	"testing"
	"time"

	"ariadne/internal/geo"
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
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
		IntersectMaxIter:    100,
		MaxLoopMeters:       500,
		MaxLoopSeconds:      10,
	}

	logger := slog.Default()
	pl := New(p, logger)

	result, _, err := pl.Run(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) >= len(points) {
		t.Errorf("expected fewer points after pipeline, got %d (was %d)", len(result), len(points))
	}
	t.Logf("before: %d points, after: %d points", len(points), len(result))
}

func TestRunCollectsWarnings(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Маршрут с петлёй, но IntersectMaxIter=0 — intersections не обработает,
	// вернёт warning
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617300, Lat: 55.756100},
		{Time: t0.Add(2 * time.Second), Lon: 37.617600, Lat: 55.755950},
		{Time: t0.Add(3 * time.Second), Lon: 37.617000, Lat: 55.755950},
		{Time: t0.Add(4 * time.Second), Lon: 37.617000, Lat: 55.756300},
	}

	p := Params{
		MaxSpeedKmh:         150,
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
		IntersectMaxIter:    0,
		MaxLoopMeters:       500,
		MaxLoopSeconds:      10,
	}

	logger := slog.Default()
	pl := New(p, logger)

	_, warnings, err := pl.Run(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(warnings) == 0 {
		t.Error("expected warnings from intersections (MaxIter=0), got none")
	}
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
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
		IntersectMaxIter:    100,
		MaxLoopMeters:       500,
		MaxLoopSeconds:      10,
	}

	logger := slog.Default()
	pl := New(p, logger)

	result, _, err := pl.Run(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) >= 2 {
		t.Errorf("expected < 2 points after speed filter, got %d", len(result))
	}
}

func TestRunEmpty(t *testing.T) {
	p := Params{
		MaxSpeedKmh:         150,
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
		IntersectMaxIter:    100,
		MaxLoopMeters:       500,
		MaxLoopSeconds:      10,
	}

	logger := slog.Default()
	pl := New(p, logger)

	result, _, err := pl.Run(nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 0 {
		t.Errorf("expected 0 points for nil input, got %d", len(result))
	}
}
