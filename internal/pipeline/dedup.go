package pipeline

import (
	"context"
	"time"

	"ariadne/internal/geo"
)

type Deduplicate struct {
	DedupDistanceMeters float64
	MaxTimeGap          time.Duration
}

func (Deduplicate) Name() string { return "deduplicate" }

func (d Deduplicate) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if len(points) < 2 {
		return points, nil, nil
	}

	result := []geo.Point{points[0]}

	for i := 1; i < len(points); i++ {
		prev := result[len(result)-1]
		curr := points[i]

		dist := geo.Haversine(prev, curr)
		dt := curr.Time.Sub(prev.Time)

		if dist < d.DedupDistanceMeters && dt < d.MaxTimeGap {
			continue
		}

		result = append(result, curr)
	}

	return result, nil, nil
}
