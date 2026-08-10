package pipeline

import (
	"context"
	"fmt"
	"time"

	"ariadne/internal/core"
	"ariadne/internal/geo"
	"ariadne/internal/logger"
	"ariadne/internal/osrm"
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
	MaxSpeedKmh           float64
	MaxAccelKmhPerSec     float64
	TeleportJumpMeters    float64 // скачок больше этого = подозрение на телепорт-загон
	TeleportReturnMeters  float64 // возврат ближе этого к точке перед скачком = вырезаем загон
	TeleportMaxSpanMeters float64 // вырезаем загон только если его размах меньше этого
	StopRadiusMeters      float64 // размер пятна стоянки для сворачивания
	StopMinPoints         int     // от скольких точек в пятне считаем стоянкой
	AnchorToleranceMeters float64 // порог отката для якорного фильтра; 0 = выключено
}

type StageStats struct {
	Name         string `json:"name" example:"collapse_stops"`
	PointsBefore int    `json:"pointsBefore" example:"3016"`
	PointsAfter  int    `json:"pointsAfter" example:"2981"`
	Elapsed      string `json:"elapsed" example:"47.123ms"`

	// Extra — подробности стадии для разбора спорных маршрутов.
	//
	// По числу точек до и после ничего не понять: ядро может выбросить
	// половину трека и быть правым, а может ошибиться на одном правиле.
	// Здесь лежит то, что об этом говорит: сколько дыр нашла дорисовка,
	// сколько проходов сделало ядро, сколько точек вернул страж.
	Extra map[string]any `json:"extra,omitempty"`

	// Error — причина, по которой стадия упала. Пустая, если всё хорошо.
	Error string `json:"error,omitempty"`
}

// Pipeline — упорядоченная цепочка Stage.
type Pipeline struct {
	stages []Stage
	state  *RunState
}

// State — общий блокнот прогона: что ядро узнало о треке и что с ним сделали
// упаковка и дорисовка.
func (pl *Pipeline) State() *RunState { return pl.state }

// Router — всё, что конвейеру нужно от маршрутизатора.
//
// Интерфейс, а не клиент: так конвейер собирается и проверяется без сети, а
// `*osrm.Client` подходит под него как есть.
type Router interface {
	Snap(ctx context.Context, pts []geo.Point) ([]float64, []bool, []string)
	PairDistance(ctx context.Context, pairs []osrm.Pair) ([]float64, []bool, []string)
	RouteGeometry(ctx context.Context, a, b geo.Point) (*osrm.Route, bool)
}

// New собирает конвейер под заданные параметры.
//
//	SortByTime → Core → Deduplicate → CollapseStops → Simplify → ReachabilityGuard → FillGaps
//
// Смысл порядка. Сперва ЧИСТКА: ядро выбирает самую тяжёлую физически связную
// цепочку точек и выбрасывает всё остальное. Потом УПАКОВКА: дубли, стоянки,
// упрощение — она уменьшает трек втрое, теряя 0.04 % километража. Потом СТРАЖ:
// упаковка судит по геометрии и может создать переход, который не проехать.
// И только в конце ДОРИСОВКА — по тому, что уже признано настоящим; рисовать
// дорогу через выброшенный спуфинг значило бы узаконить его.
//
// Четырёх прежних фильтров (якорь, телепорты, скорость, ускорение) в составе
// НЕТ. Замер показал, что они режут асфальт: якорный стирал рабочую зону в
// Бутове, фильтр скорости уносил 81 честную точку подряд, зацепившись за один
// глюк. Каскадное удаление ядро решает по построению — оно сравнивает варианты
// целиком, а не тянет одну опору вперёд. Файлы и тесты оставлены в
// репозитории; в конвейер они не включены.
//
// `r` может быть nil: без маршрутизатора чистка и дорисовка пропускают трек
// насквозь с предупреждением, и сервис продолжает работать.
func New(p Params, r Router) *Pipeline {
	state := &RunState{}

	var engine *core.Core
	var routes RouteSource
	if r != nil {
		engine = &core.Core{Snap: r, Road: RoadFrom(r)}
		routes = r
	}

	return &Pipeline{
		state: state,
		stages: []Stage{
			SortByTime{},
			Core{Engine: engine, State: state},
			Deduplicate{DedupDistanceMeters: p.DedupDistanceMeters, MaxTimeGap: p.DedupTimeGap},
			CollapseStops{RadiusMeters: p.StopRadiusMeters, MinPoints: p.StopMinPoints},
			Simplify{MinMeters: p.SimplifyMinMeters, State: state},
			ReachabilityGuard{State: state},
			FillGaps{Routes: routes, State: state},
		},
	}
}

func (pl *Pipeline) Run(ctx context.Context, points []geo.Point) ([]geo.Point, []string, []StageStats, error) {
	log := logger.FromContext(ctx)
	var allWarnings []string
	var stats []StageStats

	// Блокнот описывает ОДИН прогон. Конвейер создаётся на запрос, но если его
	// переиспользуют, второй прогон обязан начинаться с чистого листа: иначе
	// страж возьмёт снимок от прошлого трека и вернёт чужие точки.
	if pl.state != nil {
		*pl.state = RunState{}
	}

	for _, s := range pl.stages {
		// Проверка дедлайна между стадиями — защита от зависания на долгой обработке.
		// Раньше ctx.Err() проверялся внутри intersections; после её удаления
		// механизм перенесён сюда, чтобы работать независимо от состава стадий.
		if err := ctx.Err(); err != nil {
			return nil, nil, stats, err
		}

		before := len(points)
		start := time.Now()

		var (
			warnings []string
			err      error
		)
		points, warnings, err = s.Apply(ctx, points)
		elapsed := time.Since(start)

		if err != nil {
			// Статистику по уже пройденному отдаём НАРУЖУ вместе с ошибкой:
			// без неё непонятно, где именно сломалось, а это первое, что
			// спрашивают при разборе.
			stats = append(stats, StageStats{
				Name:         s.Name(),
				PointsBefore: before,
				PointsAfter:  before,
				Elapsed:      elapsed.String(),
				Error:        err.Error(),
			})
			return nil, nil, stats, fmt.Errorf("pipeline: stage %s: %w", s.Name(), err)
		}

		stats = append(stats, StageStats{
			Name:         s.Name(),
			PointsBefore: before,
			PointsAfter:  len(points),
			Elapsed:      elapsed.String(),
			Extra:        pl.extraOf(s.Name()),
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

// extraOf — подробности стадии для разбора. Пусто у тех, кому рассказывать
// нечего: сортировка и дедуп полностью описываются числом точек до и после.
func (pl *Pipeline) extraOf(stage string) map[string]any {
	if pl.state == nil {
		return nil
	}
	switch stage {
	case "core":
		r := pl.state.Report
		return map[string]any{
			"reordered":    r.Reordered,
			"collapsed":    r.Collapsed,
			"stopsTotal":   r.StopsTotal,
			"stopsTrusted": r.StopsTrusted,
			"stopsFrozen":  r.StopsFrozen,
			"split":        r.Split,
			"spread":       r.Spread,
			"amnesty":      r.Amnesty,
			"loops":        r.Loops,
			"roadBanned":   r.RoadBanned,
			"roadAsked":    r.RoadAsked,
			"roadPasses":   r.RoadPasses,
			"dropped":      r.Dropped,
			"snapMedianM":  r.SnapMedian,
			"snapFraction": r.SnapFraction,
			"kmBefore":     r.KmBefore,
			"kmAfter":      r.KmAfter,
		}
	case "reachability_guard":
		if pl.state.Guarded == 0 {
			return nil
		}
		return map[string]any{"restored": pl.state.Guarded}
	case "fill_gaps":
		f := pl.state.Fill
		return map[string]any{
			"gaps":     f.Gaps,
			"filled":   f.Filled,
			"addedM":   f.AddedM,
			"addedPts": f.AddedPts,
			"reasons":  f.Reasons,
		}
	}
	return nil
}
