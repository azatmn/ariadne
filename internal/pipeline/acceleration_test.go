package pipeline

import (
	"context"
	"testing"
	"time"

	"ariadne/internal/geo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccelerationNormal(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Три точки, плавное ускорение: 0 → ~40 км/ч → ~40 км/ч
	points := []geo.Point{
		{Time: t0, Lon: 37.6173, Lat: 55.7558},
		{Time: t0.Add(10 * time.Second), Lon: 37.6175, Lat: 55.7559},
		{Time: t0.Add(20 * time.Second), Lon: 37.6177, Lat: 55.7560},
	}

	f := FilterByAcceleration{MaxAccelKmhPerSec: 30}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)
	assert.Len(t, result, 3, "all points should pass — normal acceleration")
}

func TestAccelerationGlitch(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Едем 40 км/ч, потом GPS-глюк: резкий скачок скорости до ~130 км/ч и обратно
	// A → B (40 км/ч) → C (130 км/ч — глюк) → D (40 км/ч — нормально)
	points := []geo.Point{
		{Time: t0, Lon: 37.6000, Lat: 55.7500},                       // A
		{Time: t0.Add(10 * time.Second), Lon: 37.6010, Lat: 55.7500}, // B: ~70м за 10с ≈ 25 км/ч
		{Time: t0.Add(11 * time.Second), Lon: 37.6050, Lat: 55.7500}, // C: ~280м за 1с ≈ 1000 км/ч ... нет, слишком
		{Time: t0.Add(21 * time.Second), Lon: 37.6020, Lat: 55.7500}, // D: нормальная скорость от B
	}

	// Скорость A→B ≈ 25 км/ч
	// Скорость B→C ≈ огромная (280м/1с) — это и speed filter поймает
	// Лучше сделаем так: скорость проходит speed filter но ускорение аномальное

	// Точка C: 400м за 5с = 80 м/с = 288 км/ч — не пройдёт speed filter
	// Нужен пример где скорость < 150 но ускорение аномальное

	// A → B: 70м за 10с = 25 км/ч
	// B → C: 500м за 5с = 100 м/с ... нет, 500м/5с = 100 м/с = 360 км/ч

	// Сделаем проще: используем маленький порог ускорения
	points = []geo.Point{
		{Time: t0, Lon: 37.6000, Lat: 55.7500},                       // A
		{Time: t0.Add(10 * time.Second), Lon: 37.6001, Lat: 55.7500}, // B: ~7м за 10с ≈ 2.5 км/ч (пешеход)
		{Time: t0.Add(20 * time.Second), Lon: 37.6020, Lat: 55.7500}, // C: ~1330м за 10с ≈ 479 км/ч от B — не то
	}

	// Ок, давай реалистичнее:
	// Едем стабильно ~50 км/ч, GPS-глюч даёт одну точку с видимой скоростью 130 км/ч
	// 50 км/ч = 13.9 м/с, 130 км/ч = 36.1 м/с
	// Ускорение: (130 - 50) / 2 = 40 км/ч/с — при пороге 30 это отсечётся

	// A(0с) → B(10с): ~139м, скорость 50 км/ч
	// B(10с) → C(12с): ~72м, скорость 130 км/ч. Ускорение: (130-50)/2 = 40 км/ч/с > 30
	// C(12с) → D(22с): ~139м, скорость 50 км/ч

	// 139м ≈ 0.00125° долготы на широте 55.75
	// 72м за 2с при 130 км/ч: 130/3.6*2 = 72.2м ≈ 0.00065° долготы

	// A→B: ~78м за 10с ≈ 28 км/ч
	// B→C: ~78м за 2с ≈ 141 км/ч (GPS-глюч, но < 150 — speed filter пропустит)
	// Ускорение: (141 - 28) / 2 ≈ 56 км/ч/с > 30 — acceleration filter поймает
	points = []geo.Point{
		{Time: t0, Lon: 37.60000, Lat: 55.7500},
		{Time: t0.Add(10 * time.Second), Lon: 37.60125, Lat: 55.7500},
		{Time: t0.Add(12 * time.Second), Lon: 37.60250, Lat: 55.7500},
		{Time: t0.Add(22 * time.Second), Lon: 37.60375, Lat: 55.7500},
	}

	f := FilterByAcceleration{MaxAccelKmhPerSec: 30}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)

	// C должна быть выкинута: ускорение (130-50)/2 = 40 > 30
	assert.Len(t, result, 3, "GPS glitch point should be removed")
	assert.Equal(t, points[0], result[0], "A stays")
	assert.Equal(t, points[1], result[1], "B stays")
	assert.Equal(t, points[3], result[2], "D stays (C removed)")
}

func TestAccelerationTwoPoints(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.6, Lat: 55.75},
		{Time: t0.Add(10 * time.Second), Lon: 37.601, Lat: 55.75},
	}

	f := FilterByAcceleration{MaxAccelKmhPerSec: 30}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)
	assert.Len(t, result, 2, "two points — nothing to compare acceleration, keep both")
}

func TestAccelerationEmpty(t *testing.T) {
	f := FilterByAcceleration{MaxAccelKmhPerSec: 30}

	result, _, err := f.Apply(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, result)

	result, _, err = f.Apply(context.Background(), []geo.Point{{Lon: 37.0, Lat: 55.0}})
	require.NoError(t, err)
	assert.Len(t, result, 1)
}

// Точка с одинаковым временем и близко — легитимна: подставная dt=1с не даёт
// фильтру ускорения выкинуть её вслепую (раньше dt=0 → continue → удаление).
func TestAccelerationSameTimeCloseKept(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.6000, Lat: 55.7500},
		{Time: t0.Add(10 * time.Second), Lon: 37.6010, Lat: 55.7500}, // ~62 м за 10с ≈ 22 км/ч
		{Time: t0.Add(10 * time.Second), Lon: 37.6011, Lat: 55.7500}, // тот же миг, ~6 м от предыдущей
	}

	f := FilterByAcceleration{MaxAccelKmhPerSec: 30}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)
	assert.Len(t, result, 3, "close same-time point must be kept")
}

// Глюк с одинаковым временем (далеко) выкидывается и не роняет следующую точку.
func TestAccelerationSameTimeFarDropped(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		{Time: t0, Lon: 37.6000, Lat: 55.7500},
		{Time: t0.Add(10 * time.Second), Lon: 37.6010, Lat: 55.7500}, // ~22 км/ч
		{Time: t0.Add(10 * time.Second), Lon: 30.3141, Lat: 59.9398}, // тот же миг, Питер = глюк
		{Time: t0.Add(20 * time.Second), Lon: 37.6011, Lat: 55.7500}, // позже, рядом
	}

	f := FilterByAcceleration{MaxAccelKmhPerSec: 30}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)
	assert.Len(t, result, 3, "far same-time glitch dropped, others kept")
}

func TestAccelerationBraking(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Реальное торможение: 80 км/ч → 0 за 4 секунды = 20 км/ч/с (< 30 порог)
	// 80 км/ч = 22.2 м/с, за 10с = 222м ≈ 0.002°
	points := []geo.Point{
		{Time: t0, Lon: 37.6000, Lat: 55.7500},
		{Time: t0.Add(10 * time.Second), Lon: 37.6020, Lat: 55.7500}, // ~80 км/ч
		{Time: t0.Add(14 * time.Second), Lon: 37.6020, Lat: 55.7500}, // 0 км/ч (остановка)
		{Time: t0.Add(24 * time.Second), Lon: 37.6020, Lat: 55.7500}, // стоим
	}

	f := FilterByAcceleration{MaxAccelKmhPerSec: 30}
	result, _, err := f.Apply(context.Background(), points)
	require.NoError(t, err)
	assert.Len(t, result, 4, "real braking should pass — acceleration 20 km/h/s < 30")
}
