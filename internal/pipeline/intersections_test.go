package pipeline

import (
	"testing"
	"time"

	"ariadne/internal/geo"
)

func TestRemoveSmallLoop(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Маршрут на реальных координатах (Москва), разница ~20-30 метров
	// Вверх → вправо-вниз → влево (пересечение с первым сегментом) → вверх
	// Сегмент 0→1 и сегмент 2→3 пересекаются
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617300, Lat: 55.756100}, // вверх ~33м
		{Time: t0.Add(2 * time.Second), Lon: 37.617600, Lat: 55.755950}, // вправо-вниз ~25м
		{Time: t0.Add(3 * time.Second), Lon: 37.617000, Lat: 55.755950}, // влево ~35м (пересекает 0→1)
		{Time: t0.Add(4 * time.Second), Lon: 37.617000, Lat: 55.756300}, // вверх дальше
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10,
	}

	result, warnings, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) >= len(points) {
		t.Errorf("expected loop to be removed, got %d points (was %d)", len(result), len(points))
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	t.Logf("before: %d points, after: %d points", len(points), len(result))
}

func TestKeepBigLoop(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Та же геометрия но время между точками > MaxLoopSeconds
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(30 * time.Second), Lon: 37.617300, Lat: 55.756100},
		{Time: t0.Add(60 * time.Second), Lon: 37.617600, Lat: 55.755950},
		{Time: t0.Add(90 * time.Second), Lon: 37.617000, Lat: 55.755950},
		{Time: t0.Add(120 * time.Second), Lon: 37.617000, Lat: 55.756300},
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10, // петля длится 60с > 10с → считаем реальной
	}

	result, _, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != len(points) {
		t.Errorf("expected all %d points preserved (big loop), got %d", len(points), len(result))
	}
}

func TestNoIntersections(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Прямая линия — нет пересечений
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617400, Lat: 55.755900},
		{Time: t0.Add(2 * time.Second), Lon: 37.617500, Lat: 55.756000},
		{Time: t0.Add(3 * time.Second), Lon: 37.617600, Lat: 55.756100},
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10,
	}

	result, _, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 4 {
		t.Errorf("expected 4 points (no intersections), got %d", len(result))
	}
}

func TestKeepBigLoopByMeters(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Петля по времени маленькая (3с), но по расстоянию большая (>100м)
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617300, Lat: 55.757000}, // вверх ~133м
		{Time: t0.Add(2 * time.Second), Lon: 37.618500, Lat: 55.756400}, // вправо-вниз ~100м
		{Time: t0.Add(3 * time.Second), Lon: 37.616100, Lat: 55.756400}, // влево ~140м
		{Time: t0.Add(4 * time.Second), Lon: 37.616100, Lat: 55.757500}, // вверх
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    100, // петля ~230м > 100м → не трогаем
		MaxLoopSeconds:   10,
	}

	result, _, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != len(points) {
		t.Errorf("expected all %d points (loop too big in meters), got %d", len(points), len(result))
	}
}

func TestEmptyAndShort(t *testing.T) {
	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10,
	}

	result, _, err := r.Apply(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Errorf("nil: expected 0, got %d", len(result))
	}

	result, _, err = r.Apply([]geo.Point{{Lon: 37.0, Lat: 55.0}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 {
		t.Errorf("single point: expected 1, got %d", len(result))
	}
}

func TestMultipleIntersections(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Две маленькие петли подряд:
	// Петля 1: точки 0-3 (сегмент 0→1 пересекает 2→3)
	// Петля 2: точки 4-7 (сегмент 4→5 пересекает 6→7)
	points := []geo.Point{
		// Петля 1
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617300, Lat: 55.756100},
		{Time: t0.Add(2 * time.Second), Lon: 37.617600, Lat: 55.755950},
		{Time: t0.Add(3 * time.Second), Lon: 37.617000, Lat: 55.755950},
		// Петля 2 (такая же геометрия, сдвинутая вправо)
		{Time: t0.Add(4 * time.Second), Lon: 37.618300, Lat: 55.755800},
		{Time: t0.Add(5 * time.Second), Lon: 37.618300, Lat: 55.756100},
		{Time: t0.Add(6 * time.Second), Lon: 37.618600, Lat: 55.755950},
		{Time: t0.Add(7 * time.Second), Lon: 37.618000, Lat: 55.755950},
		// Конец
		{Time: t0.Add(8 * time.Second), Lon: 37.618000, Lat: 55.756300},
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10,
	}

	result, _, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	// Обе петли должны быть вырезаны — точек должно стать значительно меньше
	if len(result) >= len(points)-2 {
		t.Errorf("expected both loops removed, got %d points (was %d)", len(result), len(points))
	}
	t.Logf("before: %d points, after: %d points", len(points), len(result))
}

func TestLoopAtStart(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Петля в самом начале маршрута: сегмент 0→1 пересекает 2→3
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617300, Lat: 55.756100},
		{Time: t0.Add(2 * time.Second), Lon: 37.617600, Lat: 55.755950},
		{Time: t0.Add(3 * time.Second), Lon: 37.617000, Lat: 55.755950},
		// После петли — длинный прямой участок
		{Time: t0.Add(4 * time.Second), Lon: 37.617000, Lat: 55.756300},
		{Time: t0.Add(5 * time.Second), Lon: 37.617000, Lat: 55.756600},
		{Time: t0.Add(6 * time.Second), Lon: 37.617000, Lat: 55.756900},
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10,
	}

	result, _, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) >= len(points) {
		t.Errorf("expected loop at start removed, got %d points (was %d)", len(result), len(points))
	}

	// Прямой участок в конце должен остаться
	last := result[len(result)-1]
	if last.Lat != 55.756900 {
		t.Errorf("expected last point preserved, got Lat=%f", last.Lat)
	}
	t.Logf("before: %d points, after: %d points", len(points), len(result))
}

func TestLoopAtEnd(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Сначала прямой участок, потом петля в конце
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617000, Lat: 55.756900},
		{Time: t0.Add(1 * time.Second), Lon: 37.617000, Lat: 55.756600},
		{Time: t0.Add(2 * time.Second), Lon: 37.617000, Lat: 55.756300},
		// Петля: сегмент 2→3 пересекает 4→5
		{Time: t0.Add(3 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(4 * time.Second), Lon: 37.617300, Lat: 55.756100},
		{Time: t0.Add(5 * time.Second), Lon: 37.617600, Lat: 55.755950},
		{Time: t0.Add(6 * time.Second), Lon: 37.617000, Lat: 55.755950},
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10,
	}

	result, _, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) >= len(points) {
		t.Errorf("expected loop at end removed, got %d points (was %d)", len(result), len(points))
	}

	// Первая точка прямого участка должна остаться
	if result[0].Lat != 55.756900 {
		t.Errorf("expected first point preserved, got Lat=%f", result[0].Lat)
	}
	t.Logf("before: %d points, after: %d points", len(points), len(result))
}

func TestThreePoints(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Всего 3 точки — 2 сегмента, пересечение невозможно (соседние сегменты)
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617400, Lat: 55.755900},
		{Time: t0.Add(2 * time.Second), Lon: 37.617500, Lat: 55.756000},
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10,
	}

	result, _, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != 3 {
		t.Errorf("expected 3 points unchanged, got %d", len(result))
	}
}

func TestAllSameCoordinates(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Все точки в одном месте — вырожденный случай
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(2 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(3 * time.Second), Lon: 37.617300, Lat: 55.755800},
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 100,
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10,
	}

	result, _, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	// Не должно быть паники или бесконечного цикла
	t.Logf("same coords: %d -> %d points", len(points), len(result))
}

func TestIntersectMaxIterLimit(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Та же петля что в TestRemoveSmallLoop
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617300, Lat: 55.756100},
		{Time: t0.Add(2 * time.Second), Lon: 37.617600, Lat: 55.755950},
		{Time: t0.Add(3 * time.Second), Lon: 37.617000, Lat: 55.755950},
		{Time: t0.Add(4 * time.Second), Lon: 37.617000, Lat: 55.756300},
	}

	r := RemoveSelfIntersections{
		IntersectMaxIter: 0, // ноль итераций — ничего не делаем
		MaxLoopMeters:    500,
		MaxLoopSeconds:   10,
	}

	result, warnings, err := r.Apply(points)
	if err != nil {
		t.Fatal(err)
	}

	if len(result) != len(points) {
		t.Errorf("IntersectMaxIter=0 should skip processing, got %d points (was %d)", len(result), len(points))
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning about max iterations, got %d", len(warnings))
	}
}
