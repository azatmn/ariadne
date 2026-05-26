package geo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHaversineMoscowSPB(t *testing.T) {
	moscow := Point{Lon: 37.6173, Lat: 55.7558}
	spb := Point{Lon: 30.3141, Lat: 59.9398}

	got := Haversine(moscow, spb)
	want := 634_000.0 // ~634 км

	assert.InDelta(t, want, got, 5000, "Moscow→SPB distance")
	t.Logf("Moscow→SPB = %.0f m", got)
}

func TestHaversineSamePoint(t *testing.T) {
	p := Point{Lon: 38.0, Lat: 54.0}
	got := Haversine(p, p)
	assert.Equal(t, 0.0, got, "same point distance should be 0")
}

func TestTotalLength(t *testing.T) {
	points := []Point{
		{Lon: 37.6173, Lat: 55.7558}, // Москва
		{Lon: 34.3, Lat: 57.63},      // примерно Тверь
		{Lon: 30.3141, Lat: 59.9398}, // Питер
	}

	total := TotalLength(points)
	direct := Haversine(points[0], points[2])

	assert.GreaterOrEqual(t, total, direct, "total should be >= direct")
	t.Logf("total = %.0f m, direct = %.0f m", total, direct)
}

func TestTotalLengthShort(t *testing.T) {
	assert.Equal(t, 0.0, TotalLength(nil), "nil slice should return 0")
	assert.Equal(t, 0.0, TotalLength([]Point{{Lon: 37.0, Lat: 55.0}}), "single point should return 0")
}
