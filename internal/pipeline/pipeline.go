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

// Stage — независимый этап обработки маршрута: получает точки, возвращает
// точки. Этапы складываются в Pipeline и больше ничем между собой не связаны —
// поэтому стадию можно снять из состава, не трогая остальные.
//
// Контракт, одинаковый для всех реализаций:
//
//   - Name отдаёт постоянное имя для логов и статистики (snake_case);
//   - Apply не меняет входной срез — правит копию, если правит;
//   - второе возвращаемое — предупреждения для клиента, не ошибки: трек при
//     них годен, но с оговоркой;
//   - error означает, что результата нет вовсе, и прогон останавливается;
//   - стадия обязана уважать дедлайн ctx, если работает дольше мгновения.
//
// Реализации Name и Apply отдельными комментариями не снабжаются: контракт
// один и описан здесь, а повтор его у каждой из десяти стадий — шум.
type Stage interface {
	Name() string
	Apply(ctx context.Context, points []geo.Point) ([]geo.Point, []string, error)
}

// Params — параметры запроса, которые могут переопределять дефолты конфига.
// Заполняется api/handler из тела запроса.
//
// Здесь только то, что читает хотя бы одна стадия ИЗ ТЕКУЩЕГО состава. Пороги
// четырёх снятых фильтров (якорь, телепорты, скорость, ускорение) убраны: поле,
// которое никто не читает, выглядит рабочей ручкой и молча ничего не делает —
// поставить по нему потолок скорости фуры и не добиться ничего хуже, чем не
// найти настройки вовсе. Сами файлы стадий остаются в репозитории.
type Params struct {
	DedupDistanceMeters float64
	DedupTimeGap        time.Duration // окно времени для дедупа (защита от склейки «возврата в точку»)
	SimplifyMinMeters   float64
	StopRadiusMeters    float64 // размер пятна стоянки для сворачивания
	StopMinPoints       int     // от скольких точек в пятне считаем стоянкой
}

// StageStats — что стадия сделала с треком: сколько точек было, сколько
// осталось, сколько это заняло. Отдаётся наружу в GET /v1/tasks/{key}/debug и
// служит основным материалом при разборе спорного маршрута.
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

// CoreBudgetShare — какую долю ОСТАВШЕГОСЯ бюджета отдаём чистке.
//
// Чистка самая дорогая: десятки запросов к маршрутизатору и до дюжины
// перестроений цепочки. Без ограничения она съедает весь срок задачи, и
// дорисовка не успевает ничего — а это те самые 5 % километража, ради которых
// она и делалась.
//
// Замер: дорисовка на маршруте — сотня дыр, восемь потоков, 88 мс на запрос,
// то есть около полутора секунд. Пятой части бюджета ей хватает с запасом.
const CoreBudgetShare = 0.8

// budgeted — стадия, которой нужен свой кусок общего бюджета.
//
// Интерфейс, а не проверка по имени: имя стадии — про лог, и завязывать на
// него поведение значит сломать конвейер молча при первом переименовании.
type budgeted interface {
	BudgetShare() float64
}

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

// Run прогоняет точки через все стадии по очереди и возвращает результат,
// предупреждения и статистику по каждой стадии.
//
// Статистика возвращается ДАЖЕ при ошибке — по ней и видно, на какой стадии
// сломалось. Дедлайн context проверяется между стадиями и внутри долгих; на
// исчерпанном бюджете Run возвращает ошибку, а не обрезанный трек.
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
		stageCtx, releaseStage := stageBudget(ctx, s)
		points, warnings, err = s.Apply(stageCtx, points)
		releaseStage()
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

// stageBudget выдаёт стадии её долю оставшегося времени.
//
// Стадиям без своей доли отдаётся общий контекст как есть — большинство их
// вообще не ходит в сеть и укладывается в миллисекунды.
func stageBudget(ctx context.Context, s Stage) (context.Context, context.CancelFunc) {
	b, ok := s.(budgeted)
	if !ok {
		return ctx, func() {}
	}
	deadline, has := ctx.Deadline()
	if !has {
		return ctx, func() {}
	}

	left := time.Until(deadline)
	if left <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(float64(left)*b.BudgetShare()))
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
			"degraded":     r.Degraded,
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
			"degraded": f.Degraded,
		}
	}
	return nil
}
