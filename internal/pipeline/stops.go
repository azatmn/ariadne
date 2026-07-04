package pipeline

import (
	"context"

	"ariadne/internal/geo"
)

// CollapseStops сворачивает «стоянки» — кучи точек в малом радиусе, которые
// копятся, пока грузовик стоит (погрузка, пробка, отдых) или пока GPS дрожит на месте.
// В отличие от Deduplicate, НЕ смотрит на Δt между точками: стоянка — это долгое
// пребывание в зоне (большие Δt), и условие dedup «Δt < 60с» её как раз не ловит.
//
// Точку стоянки сохраняем (одну, момент въезда) — поэтому стоянка на точке заказа
// не теряется, убирается только каша вокруг.
type CollapseStops struct {
	RadiusMeters float64 // размер пятна стоянки
	MinPoints    int     // сколько точек в пятне, чтобы считать стоянкой (а не проездом)
}

func (CollapseStops) Name() string { return "collapse_stops" }

func (f CollapseStops) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if f.MinPoints < 2 || len(points) < f.MinPoints {
		return points, nil, nil
	}

	result := make([]geo.Point, 0, len(points))

	i := 0
	for i < len(points) {
		// Сколько ПОДРЯД идущих точек умещаются в радиус от points[i].
		j := i + 1
		for j < len(points) && geo.Haversine(points[i], points[j]) < f.RadiusMeters {
			j++
		}
		result = append(result, points[i]) // одна точка от группы
		if j-i >= f.MinPoints {
			// Стоянка: сворачиваем группу в одну точку, продолжаем с точки выхода.
			i = j
		} else {
			i++
		}
	}

	return result, nil, nil
}
