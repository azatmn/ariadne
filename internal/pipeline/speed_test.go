package pipeline

import (
	"context"
	"testing"
	"time"

	"ariadne/internal/geo"
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
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 3 {
		t.Errorf("expected 3 points (all normal speed), got %d", len(result))
	}
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
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 points (teleport removed), got %d", len(result))
	}
}

func TestFilterBySpeedSameTime(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.0, Lat: 55.0},
		{Time: t0, Lon: 37.1, Lat: 55.1},
	}

	f := FilterBySpeed{MaxKmh: 150}
	result, _, err := f.Apply(context.Background(), points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 1 {
		t.Errorf("expected 1 point (dt=0 skipped), got %d", len(result))
	}
}

func TestFilterBySpeedEmpty(t *testing.T) {
	f := FilterBySpeed{MaxKmh: 150}

	result, _, err := f.Apply(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 points, got %d", len(result))
	}

	result, _, err = f.Apply(context.Background(), []geo.Point{{Lon: 37.0, Lat: 55.0}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 point, got %d", len(result))
	}
}
