package pipeline

import (
	"context"

	"ariadne/internal/geo"
)

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
			continue
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
		return 0
	}
	return (geo.Haversine(a, b) / dt) * 3.6
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
