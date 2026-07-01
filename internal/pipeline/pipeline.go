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
	DedupDistanceMeters   float64
	DedupTimeGap          time.Duration // окно времени для дедупа (защита от склейки «возврата в точку»)
	SimplifyMinMeters     float64
	IntersectMaxIter      int
	MaxSpeedKmh           float64
	MaxAccelKmhPerSec     float64
	TeleportJumpMeters    float64 // скачок больше этого = подозрение на телепорт-загон
	TeleportReturnMeters  float64 // возврат ближе этого к точке перед скачком = вырезаем загон
	TeleportMaxSpanMeters float64 // вырезаем загон только если его размах меньше этого
	StopRadiusMeters      float64 // размер пятна стоянки для сворачивания
	StopMinPoints         int     // от скольких точек в пятне считаем стоянкой
	MaxLoopMeters         float64 // эвристика: петли больше этого периметра не трогаем (реальные развязки)
	MaxLoopSeconds        float64 // эвристика: петли длиннее по времени не трогаем
	AnchorToleranceMeters float64 // порог отката для якорного фильтра; 0 = выключено
}

type StageStats struct {
	Name         string `json:"name" example:"RemoveSelfIntersections"`
	PointsBefore int    `json:"pointsBefore" example:"3016"`
	PointsAfter  int    `json:"pointsAfter" example:"2981"`
	Elapsed      string `json:"elapsed" example:"47.123ms"`
}

// Pipeline — упорядоченная цепочка Stage.
type Pipeline struct {
	stages []Stage
}

// New собирает pipeline под заданные параметры.
// Порядок (MVP):
//
//	SortByTime → RemoveAnchorBacktrack → RemoveTeleports → FilterBySpeed → FilterByAcceleration → Deduplicate → CollapseStops → Simplify
//
// RemoveAnchorBacktrack сразу после сортировки: якоря = крайние точки по времени,
// режем «откаты» (точка дёрнулась назад к старту и от цели) до локальных фильтров.
func New(p Params) *Pipeline {
	stages := []Stage{
		SortByTime{},
		RemoveAnchorBacktrack{ToleranceMeters: p.AnchorToleranceMeters},
		RemoveTeleports{JumpMeters: p.TeleportJumpMeters, ReturnMeters: p.TeleportReturnMeters, MaxSpanMeters: p.TeleportMaxSpanMeters},
		FilterBySpeed{MaxKmh: p.MaxSpeedKmh},
		FilterByAcceleration{MaxAccelKmhPerSec: p.MaxAccelKmhPerSec},
		Deduplicate{DedupDistanceMeters: p.DedupDistanceMeters, MaxTimeGap: p.DedupTimeGap},
		CollapseStops{RadiusMeters: p.StopRadiusMeters, MinPoints: p.StopMinPoints},
		Simplify{MinMeters: p.SimplifyMinMeters},
	}

	return &Pipeline{stages: stages}
}

func (pl *Pipeline) Run(ctx context.Context, points []geo.Point) ([]geo.Point, []string, []StageStats, error) {
	log := logger.FromContext(ctx)
	var allWarnings []string
	var stats []StageStats

	for _, s := range pl.stages {
		// Проверка дедлайна между стадиями — защита от зависания на долгой обработке.
		// Раньше ctx.Err() проверялся внутри intersections; после её удаления
		// механизм перенесён сюда, чтобы работать независимо от состава стадий.
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}

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
