package pipeline

import (
	"context"

	"ariadne/internal/geo"
)

// sameTimestampDt — подставная разница во времени для пары точек с одинаковым
// (посекундным) штампом. Реальный зазор между ними < 1с, поэтому берём 1с как
// самый щедрый допуск: точка, уехавшая не дальше MaxKmh×1с (≈41 м при 150 км/ч),
// считается легитимной, дальше — глюком и режется обычной проверкой. Так глюк не
// становится опорной точкой для следующей проверки. Не env — это привязка к
// разрешению времени (секунда), а не бизнес-настройка. Используется и в
// filter_by_acceleration.
const sameTimestampDt = 1.0 // секунда

type FilterBySpeed struct {
	MaxKmh float64
}

func (FilterBySpeed) Name() string { return "filter_by_speed" }

func (f FilterBySpeed) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if len(points) < 2 {
		return points, nil, nil
	}

	result := []geo.Point{points[0]}

	for i := 1; i < len(points); i++ {
		prev := result[len(result)-1]
		curr := points[i]

		dt := curr.Time.Sub(prev.Time).Seconds()
		if dt <= 0 {
			// Одинаковое время: не делим на ноль и не выкидываем вслепую —
			// подставляем 1с и судим точку по расстоянию (близкая = легит,
			// далёкая = глюк, отсеется проверкой скорости ниже).
			dt = sameTimestampDt
		}

		meters := geo.Haversine(prev, curr)
		kmh := (meters / dt) * 3.6

		if kmh <= f.MaxKmh {
			result = append(result, curr)
		}
	}

	return result, nil, nil
}
