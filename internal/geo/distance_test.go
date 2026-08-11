package geo

import (
	"math"
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

// Эталонные расстояния сняты с прототипа на Python (`lib_track.haversine`),
// по которому сверяется всё ядро. Тест закрепляет договорённость: обе
// реализации считают на ОДНОМ радиусе Земли, поэтому сверка идёт число в
// число, а не с допуском.
//
// Прототип берёт asin(sqrt(a)), Go — atan2(sqrt(h), sqrt(1-h)). Формулы
// тождественны, расходятся только в последних битах, отсюда допуск 1e-6 м:
// это микрометр на шестистах километрах.
func TestHaversineMatchesPrototype(t *testing.T) {
	cases := []struct {
		name string
		a, b Point
		want float64
	}{
		{"Москва→Питер", Point{Lon: 37.6173, Lat: 55.7558}, Point{Lon: 30.3141, Lat: 59.9398}, 634287.3735874515},
		{"шаг трекера", Point{Lon: 37.6173, Lat: 55.7558}, Point{Lon: 37.6180, Lat: 55.7561}, 55.0567095384},
		{"градус по экватору", Point{Lon: 0, Lat: 0}, Point{Lon: 1, Lat: 0}, 111195.0802335329},
		{"градус по меридиану", Point{Lon: 0, Lat: 0}, Point{Lon: 0, Lat: 1}, 111195.0802335329},
		{"Саратов→Энгельс", Point{Lon: 46.0086, Lat: 51.5336}, Point{Lon: 46.1264, Lat: 51.4855}, 9750.3367218019},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.InDelta(t, c.want, Haversine(c.a, c.b), 1e-6, "расхождение с прототипом")
		})
	}
}

// Радиус — не просто число, а договорённость с прототипом. Если кто-то
// вернёт округлённые 6 371 000, все сверки поедут на 1.4 ppm и придётся
// сравнивать с допуском, а допуск прячет настоящие ошибки переноса.
func TestEarthRadiusPinned(t *testing.T) {
	assert.Equal(t, 6_371_008.8, float64(earthRadius),
		"радиус обязан совпадать с прототипом (lib_track.R)")
}

// Расстояние симметрично: A→B и B→A. Звучит очевидно, но в haversine есть
// несимметричные на вид места (cos(lat1)·cos(lat2) симметрично, а вот
// порядок вычитания широт — нет), и опечатка там даёт разный ответ.
func TestHaversineSymmetric(t *testing.T) {
	a := Point{Lon: 37.6173, Lat: 55.7558}
	b := Point{Lon: 30.3141, Lat: 59.9398}
	assert.Equal(t, Haversine(a, b), Haversine(b, a), "A→B должно равняться B→A")
}

// Антиподы — верхняя граница, на которой формула обязана не развалиться.
// Ответ ровно половина окружности; asin-форма здесь теряет точность, atan2
// держит, и это одна из причин, почему в Go взята именно она.
func TestHaversineAntipodal(t *testing.T) {
	a := Point{Lon: 0, Lat: 0}
	b := Point{Lon: 180, Lat: 0}
	want := math.Pi * earthRadius
	assert.InDelta(t, want, Haversine(a, b), 1e-3, "антиподы = половина окружности")
}
