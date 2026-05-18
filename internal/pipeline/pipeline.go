package pipeline

import (
	"context"
	"fmt"
	"time"

	"ariadne/internal/geo"
	"ariadne/internal/logger"
)

// Stage — независимый этап обработки маршрута.
// Каждый этап получает текущий список точек и возвращает новый.
// Этапы композируются в Pipeline.
type Stage interface {
	Name() string
	Apply(ctx context.Context, points []geo.Point) ([]geo.Point, []string, error)
}

// Params — параметры запроса, которые могут переопределять дефолты конфига.
// Заполняется api/handler из тела запроса.
type Params struct {
	DedupDistanceMeters float64
	DedupTimeGap        time.Duration // окно времени для дедупа (защита от склейки «возврата в точку»)
	// SimplifyMinMeters float64
	IntersectMaxIter int
	MaxSpeedKmh      float64
	MaxLoopMeters    float64 // эвристика: петли больше этого периметра не трогаем (реальные развязки)
	MaxLoopSeconds   float64 // эвристика: петли длиннее по времени не трогаем
	// UseOSRM bool
}

type StageStats struct {
	Name         string `json:"name"`
	PointsBefore int    `json:"pointsBefore"`
	PointsAfter  int    `json:"pointsAfter"`
	Elapsed      string `json:"elapsed"`
}

// Pipeline — упорядоченная цепочка Stage.
type Pipeline struct {
	stages []Stage
}

// New собирает pipeline под заданные параметры.
// Порядок (MVP):
//
//	SortByTime → FilterBySpeed → Deduplicate → RemoveSelfIntersections
//
// Speed ДО dedup: иначе склейка близких точек растворяет Δt вокруг телепорта,
// и фильтр скорости пропустит выброс. Подробнее — Decisions.md.
//
// Пост-MVP: добавится OSRMMatch перед/после Intersections (решить при интеграции).
func New(p Params) *Pipeline {
	stages := []Stage{
		SortByTime{},
		FilterBySpeed{MaxKmh: p.MaxSpeedKmh},
		Deduplicate{DedupDistanceMeters: p.DedupDistanceMeters, MaxTimeGap: p.DedupTimeGap},
		RemoveSelfIntersections{
			IntersectMaxIter: p.IntersectMaxIter,
			MaxLoopMeters:    p.MaxLoopMeters,
			MaxLoopSeconds:   p.MaxLoopSeconds,
		},
	}

	return &Pipeline{stages: stages}
}

func (pl *Pipeline) Run(ctx context.Context, points []geo.Point) ([]geo.Point, []string, []StageStats, error) {
	log := logger.FromContext(ctx)
	var allWarnings []string
	var stats []StageStats

	for _, s := range pl.stages {
		before := len(points)
		start := time.Now()

		var (
			warnings []string
			err      error
		)
		points, warnings, err = s.Apply(ctx, points)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("pipeline: stage %s: %w", s.Name(), err)
		}

		elapsed := time.Since(start)

		stats = append(stats, StageStats{
			Name:         s.Name(),
			PointsBefore: before,
			PointsAfter:  len(points),
			Elapsed:      elapsed.String(),
		})

		log.Info("stage done",
			"stage", s.Name(),
			"before", before,
			"after", len(points),
			"elapsed", elapsed,
		)

		for _, w := range warnings {
			log.Warn("pipeline warning", "stage", s.Name(), "warning", w)
		}
		allWarnings = append(allWarnings, warnings...)

		if len(points) < 2 {
			log.Warn("pipeline stopped early: fewer than 2 points remain",
				"after_stage", s.Name(),
				"points", len(points),
			)
			break
		}
	}

	return points, allWarnings, stats, nil
}
