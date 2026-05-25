package pipeline

import (
	"context"
	"slices"

	"ariadne/internal/geo"
)

type SortByTime struct{}

func (SortByTime) Name() string { return "sort_by_time" }

func (SortByTime) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	sorted := slices.Clone(points)
	slices.SortStableFunc(sorted, func(a, b geo.Point) int {
		return a.Time.Compare(b.Time)
	})
	return sorted, nil, nil
}
