package pipeline

import (
	"testing"
	"time"

	"ariadne/internal/geo"
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
	result, _, err := d.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 points (2 dupes removed), got %d", len(result))
	}
}

func TestDeduplicateKeepsTimeGap(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(2 * time.Hour), Lon: 37.617301, Lat: 55.755801}, // рядом, но 2 часа спустя
	}

	d := Deduplicate{DedupDistanceMeters: 2.0, MaxTimeGap: 60 * time.Second}
	result, _, err := d.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 points (same place but time gap too big), got %d", len(result))
	}
}

func TestDeduplicateEmpty(t *testing.T) {
	d := Deduplicate{DedupDistanceMeters: 2.0, MaxTimeGap: 60 * time.Second}

	result, _, err := d.Apply(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 points, got %d", len(result))
	}
}
