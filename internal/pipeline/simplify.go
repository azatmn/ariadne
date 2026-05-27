package pipeline

import (
	"context"

	"ariadne/internal/geo"
)

type Simplify struct {
	MinMeters float64
}

func (Simplify) Name() string { return "simplify" }

func (s Simplify) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if len(points) <= 2 {
		return points, nil, nil
	}

	keep := make([]bool, len(points))
	keep[0] = true
	keep[len(points)-1] = true

	rdp(points, 0, len(points)-1, s.MinMeters, keep)

	result := make([]geo.Point, 0, len(points))
	for i, k := range keep {
		if k {
			result = append(result, points[i])
		}
	}

	return result, nil, nil
}

func rdp(points []geo.Point, start, end int, epsilon float64, keep []bool) {
	if end-start < 2 {
		return
	}

	maxDist := 0.0
	maxIdx := start

	for i := start + 1; i < end; i++ {
		d := geo.CrossTrackDistance(points[i], points[start], points[end])
		if d > maxDist {
			maxDist = d
			maxIdx = i
		}
	}

	if maxDist > epsilon {
		keep[maxIdx] = true
		rdp(points, start, maxIdx, epsilon, keep)
		rdp(points, maxIdx, end, epsilon, keep)
	}
}
