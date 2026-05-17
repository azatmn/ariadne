package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"ariadne/internal/geo"
)

// Stage — независимый этап обработки маршрута.
// Каждый этап получает текущий список точек и возвращает новый.
// Этапы композируются в Pipeline.
type Stage interface {
	Name() string
	Apply(points []geo.Point) ([]geo.Point, []string, error)
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

// Pipeline — упорядоченная цепочка Stage.
type Pipeline struct {
	stages []Stage
	logger *slog.Logger
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
func New(p Params, logger *slog.Logger) *Pipeline {
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

	return &Pipeline{stages: stages, logger: logger}
}

func (pl *Pipeline) Run(points []geo.Point) ([]geo.Point, []string, error) {
	var allWarnings []string

	for _, s := range pl.stages {
		before := len(points)
		start := time.Now()

		var (
			warnings []string
			err      error
		)
		points, warnings, err = s.Apply(points)
		if err != nil {
			return nil, nil, fmt.Errorf("pipeline: stage %s: %w", s.Name(), err)
		}

		allWarnings = append(allWarnings, warnings...)

		pl.logger.Info("stage done",
			"stage", s.Name(),
			"before", before,
			"after", len(points),
			"elapsed", time.Since(start),
		)

		if len(points) < 2 {
			pl.logger.Warn("pipeline stopped early: fewer than 2 points remain",
				"after_stage", s.Name(),
				"points", len(points),
			)
			break
		}
	}

	return points, allWarnings, nil
}
