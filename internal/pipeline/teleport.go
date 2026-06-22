package pipeline

import (
	"context"
	"fmt"
	"math"

	"ariadne/internal/geo"
)

// RemoveTeleports вырезает «телепорт-загоны» — артефакт GPS-спуфинга в РФ:
// трек скачком улетает в сторону (например в зону аэропорта), там хаотично
// «крутится», и скачком возвращается примерно туда же. Фильтры скорости/дедупа
// его не ловят (спуфинг мимикрирует под движение ~100 км/ч с разбросом сотни метров).
//
// Принцип: сам по себе большой скачок НЕ удаляем (это может быть честный пропуск
// GPS — едем дальше по трассе). Удаляем только если после скачка трек ВЕРНУЛСЯ
// близко к точке перед скачком — значит это «слетал и вернулся».
type RemoveTeleports struct {
	JumpMeters    float64 // скачок между соседними точками больше этого = подозрение
	ReturnMeters  float64 // возврат ближе этого к точке перед скачком = телепорт-загон
	MaxSpanMeters float64 // вырезаем загон ТОЛЬКО если его размах меньше этого (спуфинг компактен; реальная поездка растянута)
}

func (RemoveTeleports) Name() string { return "remove_teleports" }

func (f RemoveTeleports) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if len(points) < 3 {
		return points, nil, nil
	}

	result := []geo.Point{points[0]}
	var warnings []string
	removed := 0

	// i — индекс последней ПРИНЯТОЙ точки в исходном массиве.
	i := 0
	for i < len(points)-1 {
		if geo.Haversine(points[i], points[i+1]) > f.JumpMeters {
			// Подозрение на телепорт: ищем возврат к points[i] дальше по треку.
			ret := -1
			for j := i + 2; j < len(points); j++ {
				if geo.Haversine(points[i], points[j]) < f.ReturnMeters {
					ret = j
					break
				}
			}
			if ret != -1 && spanMeters(points[i+1:ret]) < f.MaxSpanMeters {
				// Загон компактный (спуфинг-кластер) — вырезаем, продолжаем с точки возврата.
				removed += ret - (i + 1)
				result = append(result, points[ret])
				i = ret
				continue
			}
			// Возврата нет (честный пропуск сигнала) или загон растянут (реальная
			// поездка с возвратом — заезд на склад и т.п.) — точку оставляем.
		}
		result = append(result, points[i+1])
		i++
	}

	if removed > 0 {
		warnings = append(warnings, fmt.Sprintf("teleport: removed %d points", removed))
	}
	return result, warnings, nil
}

// spanMeters — размах набора точек (диагональ ограничивающего прямоугольника).
// Малый размах = точки кучкуются в зоне (спуфинг); большой = реальное перемещение.
func spanMeters(pts []geo.Point) float64 {
	if len(pts) == 0 {
		return 0
	}
	minLa, maxLa := pts[0].Lat, pts[0].Lat
	minLo, maxLo := pts[0].Lon, pts[0].Lon
	for _, p := range pts {
		minLa, maxLa = math.Min(minLa, p.Lat), math.Max(maxLa, p.Lat)
		minLo, maxLo = math.Min(minLo, p.Lon), math.Max(maxLo, p.Lon)
	}
	return geo.Haversine(geo.Point{Lat: minLa, Lon: minLo}, geo.Point{Lat: maxLa, Lon: maxLo})
}
