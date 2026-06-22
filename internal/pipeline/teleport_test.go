package pipeline

import (
	"context"
	"testing"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pt — короткий конструктор точки (время детектору телепортов не нужно).
func pt(lat, lon float64) geo.Point {
	return geo.Point{Lat: lat, Lon: lon}
}

// maxLat — максимальная широта в треке (для проверки, что улетевшие точки удалены).
func maxLat(points []geo.Point) float64 {
	m := -90.0
	for _, p := range points {
		if p.Lat > m {
			m = p.Lat
		}
	}
	return m
}

// пороги для тестов: скачок/возврат 2 км, размах загона до 10 км.
var teleport = RemoveTeleports{JumpMeters: 2000, ReturnMeters: 2000, MaxSpanMeters: 10000}

func TestRemoveTeleports_Name(t *testing.T) {
	assert.Equal(t, "remove_teleports", teleport.Name())
}

func TestRemoveTeleports_CleanTrackUnchanged(t *testing.T) {
	// Ровная трасса вдоль долготы (шаг ~0.01° ≈ 640 м < порога) — телепортов нет.
	in := []geo.Point{
		pt(55.0, 37.00), pt(55.0, 37.01), pt(55.0, 37.02),
		pt(55.0, 37.03), pt(55.0, 37.04),
	}
	got, _, err := teleport.Apply(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, in, got, "чистый трек не должен меняться")
}

func TestRemoveTeleports_RemovesSpoofingLoop(t *testing.T) {
	// Трасса -> скачок в сторону (~55 км) -> кластер -> возврат к точке перед скачком -> продолжение.
	in := []geo.Point{
		pt(55.0, 37.00), pt(55.0, 37.01), pt(55.0, 37.02), // anchor = индекс 2
		pt(55.5, 37.50),                    // телепорт вбок
		pt(55.51, 37.51), pt(55.49, 37.49), // хаос-кластер
		pt(55.0, 37.025),                 // возврат ~320 м к anchor
		pt(55.0, 37.03), pt(55.0, 37.04), // продолжение
	}
	got, warns, err := teleport.Apply(context.Background(), in)
	require.NoError(t, err)
	// 3 точки загона (телепорт + 2 кластера) вырезаны: было 9 -> стало 6.
	assert.Len(t, got, 6)
	// Ни одной улетевшей точки (lat ~55.5) не осталось.
	assert.Less(t, maxLat(got), 55.1, "точки телепорта должны быть удалены")
	assert.NotEmpty(t, warns, "вырезание загона должно дать warning")
}

func TestRemoveTeleports_KeepsLargeSpanTrip(t *testing.T) {
	// Скачок и возврат ЕСТЬ, но между ними — реальная поездка с большим размахом
	// (десятки км). Это не спуфинг (тот компактный), вырезать НЕ должны.
	in := []geo.Point{
		pt(55.0, 37.00), pt(55.0, 37.01), // anchor
		pt(55.0, 38.00),                  // скачок (~64 км)
		pt(55.0, 38.50), pt(55.0, 39.00), // поездка — размах загона ~64 км > 10 км
		pt(55.0, 37.015), // возврат к anchor
		pt(55.0, 37.02),
	}
	got, _, err := teleport.Apply(context.Background(), in)
	require.NoError(t, err)
	assert.Len(t, got, len(in), "загон с большим размахом — реальная поездка, не вырезаем")
}

func TestRemoveTeleports_KeepsLegitGapNoReturn(t *testing.T) {
	// Честный пропуск GPS: скачок вперёд вдоль маршрута, БЕЗ возврата назад.
	in := []geo.Point{
		pt(55.0, 37.00), pt(55.0, 37.01),
		pt(55.0, 38.00), // скачок ~64 км вперёд (пропал сигнал, едем дальше)
		pt(55.0, 38.01), pt(55.0, 38.02),
	}
	got, _, err := teleport.Apply(context.Background(), in)
	require.NoError(t, err)
	assert.Len(t, got, 5, "честный скачок без возврата не трогаем")
}

func TestRemoveTeleports_KeepsTeleportAtEndNoReturn(t *testing.T) {
	// Телепорт в самом конце без возврата — не отличить от честного финала, оставляем.
	in := []geo.Point{
		pt(55.0, 37.00), pt(55.0, 37.01), pt(55.0, 37.02),
		pt(55.5, 37.50), // улетел в конце и трек закончился
	}
	got, _, err := teleport.Apply(context.Background(), in)
	require.NoError(t, err)
	assert.Len(t, got, 4)
}

func TestRemoveTeleports_MultipleLoops(t *testing.T) {
	// Два спуфинг-загона на одном треке.
	in := []geo.Point{
		pt(55.0, 37.00), pt(55.0, 37.01), // anchor1 = индекс 1
		pt(56.0, 38.00), pt(56.01, 38.01), // загон 1
		pt(55.0, 37.015),                 // возврат 1
		pt(55.0, 37.02), pt(55.0, 37.03), // anchor2 = индекс 6
		pt(54.0, 36.00), pt(53.99, 36.01), // загон 2
		pt(55.0, 37.035), // возврат 2
		pt(55.0, 37.04),
	}
	got, _, err := teleport.Apply(context.Background(), in)
	require.NoError(t, err)
	// 11 точек, два загона по 2 точки вырезаны: 11 - 4 = 7.
	assert.Len(t, got, 7)
	assert.Less(t, maxLat(got), 55.1)
	assert.Greater(t, minLat(got), 54.5)
}

func TestRemoveTeleports_TooFewPoints(t *testing.T) {
	in := []geo.Point{pt(55.0, 37.0), pt(55.0, 37.01)}
	got, _, err := teleport.Apply(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, in, got, "меньше 3 точек — без изменений")
}

func TestRemoveTeleports_Empty(t *testing.T) {
	got, _, err := teleport.Apply(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// minLat — минимальная широта (для проверки удаления загона вниз).
func minLat(points []geo.Point) float64 {
	m := 90.0
	for _, p := range points {
		if p.Lat < m {
			m = p.Lat
		}
	}
	return m
}
