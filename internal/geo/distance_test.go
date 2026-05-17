package geo

import (
	"math"
	"testing"
)

func TestHaversineMoscowSPB(t *testing.T) {
	moscow := Point{Lon: 37.6173, Lat: 55.7558}
	spb := Point{Lon: 30.3141, Lat: 59.9398}

	got := Haversine(moscow, spb)
	want := 634_000.0 // ~634 км

	if math.Abs(got-want) > 5000 {
		t.Errorf("Moscow→SPB: got %.0f m, want ~%.0f m", got, want)
	}
	t.Logf("Moscow→SPB = %.0f m", got)
}

func TestHaversineSamePoint(t *testing.T) {
	p := Point{Lon: 38.0, Lat: 54.0}
	got := Haversine(p, p)
	if got != 0 {
		t.Errorf("same point: got %f, want 0", got)
	}
}

func TestTotalLength(t *testing.T) {
	points := []Point{
		{Lon: 37.6173, Lat: 55.7558}, // Москва
		{Lon: 34.3, Lat: 57.63},      // примерно Тверь
		{Lon: 30.3141, Lat: 59.9398}, // Питер
	}

	total := TotalLength(points)
	direct := Haversine(points[0], points[2])

	if total < direct {
		t.Errorf("total (%f) should be >= direct (%f)", total, direct)
	}
	t.Logf("total = %.0f m, direct = %.0f m", total, direct)
}

func TestTotalLengthShort(t *testing.T) {
	if TotalLength(nil) != 0 {
		t.Error("nil slice should return 0")
	}
	if TotalLength([]Point{{Lon: 37.0, Lat: 55.0}}) != 0 {
		t.Error("single point should return 0")
	}
}
