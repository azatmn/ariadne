package pipeline

import (
	"context"

	"ariadne/internal/geo"
)

// FilterByAcceleration выбрасывает точку, если скорость на подходе к ней
// изменилась резче, чем MaxAccelKmhPerSec.
//
// СТАДИЯ СНЯТА С КОНВЕЙЕРА. В pipeline.New не включается; файл и тесты
// оставлены как след разобранного подхода. Причина снятия общая для всех
// четырёх прежних фильтров: они судят по одному признаку и удаляют сразу,
// а ошибка такого удаления необратима — выброшенная точка становится опорой
// для следующей проверки, и за одним глюком уезжает честный кусок трека.
// Сейчас это делает ядро (пакет core): правила только копят штрафы, а
// выбрасывает один раз общий выбор цепочки, взвесив все улики разом.
//
// Замысел был поймать то, что проходит по скорости: не саму быструю точку, а
// рывок до неё. На деле рывок одинаково даёт и глюк, и обычный разрыв связи —
// после дыры в час любая честная точка выглядит внезапной.
type FilterByAcceleration struct {
	MaxAccelKmhPerSec float64
}

func (FilterByAcceleration) Name() string { return "filter_by_acceleration" }

func (f FilterByAcceleration) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if len(points) < 3 {
		return points, nil, nil
	}

	result := []geo.Point{points[0], points[1]}
	prevSpeed := speedKmh(points[0], points[1])

	for i := 2; i < len(points); i++ {
		prev := result[len(result)-1]
		curr := points[i]

		dt := curr.Time.Sub(prev.Time).Seconds()
		if dt <= 0 {
			// Одинаковое время: подставляем 1с (см. sameTimestampDt в speed.go)
			// вместо выкидывания точки. speedKmh ниже делает то же самое.
			dt = sameTimestampDt
		}

		currSpeed := speedKmh(prev, curr)
		accel := abs(currSpeed-prevSpeed) / dt

		if accel <= f.MaxAccelKmhPerSec {
			result = append(result, curr)
			prevSpeed = currSpeed
		}
	}

	return result, nil, nil
}

func speedKmh(a, b geo.Point) float64 {
	dt := b.Time.Sub(a.Time).Seconds()
	if dt <= 0 {
		dt = sameTimestampDt // одинаковое время → 1с, а не «скорость 0»
	}
	return (geo.Haversine(a, b) / dt) * 3.6
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
