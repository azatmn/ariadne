package pipeline

import (
	"slices"

	"ariadne/internal/geo"
)

type RemoveSelfIntersections struct {
	IntersectMaxIter int
	MaxLoopMeters    float64
	MaxLoopSeconds   float64
}

func (RemoveSelfIntersections) Name() string { return "remove_self_intersections" }

func (r RemoveSelfIntersections) Apply(points []geo.Point) ([]geo.Point, []string, error) {
	if len(points) < 4 {
		return points, nil, nil
	}

	for iter := 0; iter < r.IntersectMaxIter; iter++ {
		from, to, found := r.findLoop(points)
		if !found {
			return points, nil, nil
		}

		points = slices.Delete(points, from, to)
	}

	return points, []string{"intersect: max iterations reached, route may still contain intersections"}, nil
}

func (r RemoveSelfIntersections) findLoop(points []geo.Point) (int, int, bool) {
	n := len(points)
	for i := 0; i < n-2; i++ {
		for j := i + 2; j < n-1; j++ {
			dt := points[j].Time.Sub(points[i+1].Time).Seconds()
			if dt > r.MaxLoopSeconds {
				break
			}

			if !geo.SegmentsIntersect(points[i], points[i+1], points[j], points[j+1]) {
				continue
			}

			loopFrom := i + 1
			loopTo := j + 1

			loopMeters := geo.TotalLength(points[loopFrom : loopTo+1])

			if loopMeters > r.MaxLoopMeters {
				continue
			}

			return loopFrom, loopTo, true
		}
	}

	return 0, 0, false
}
