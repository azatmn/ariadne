package pipeline

import "ariadne/internal/geo"

type FilterBySpeed struct {
	MaxKmh float64
}

func (FilterBySpeed) Name() string { return "filter_by_speed" }

func (f FilterBySpeed) Apply(points []geo.Point) ([]geo.Point, []string, error) {
	if len(points) < 2 {
		return points, nil, nil
	}

	result := []geo.Point{points[0]}

	for i := 1; i < len(points); i++ {
		prev := result[len(result)-1]
		curr := points[i]

		dt := curr.Time.Sub(prev.Time).Seconds()
		if dt <= 0 {
			continue
		}

		meters := geo.Haversine(prev, curr)
		kmh := (meters / dt) * 3.6

		if kmh <= f.MaxKmh {
			result = append(result, curr)
		}
	}

	return result, nil, nil
}
