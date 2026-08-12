package pipeline

import (
	"ariadne/internal/osrm"
	"context"
	"errors"
	"testing"
	"time"

	"ariadne/internal/geo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunFullPipeline(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	points := []geo.Point{
		// Нормальная точка
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		// Дубль (0.1м, 1с)
		{Time: t0.Add(1 * time.Second), Lon: 37.617301, Lat: 55.755801},
		// Телепорт — 10км за 1 секунду
		{Time: t0.Add(2 * time.Second), Lon: 37.750000, Lat: 55.755800},
		// Нормальное продолжение
		{Time: t0.Add(3 * time.Second), Lon: 37.617400, Lat: 55.755900},
		{Time: t0.Add(4 * time.Second), Lon: 37.617500, Lat: 55.756000},
	}

	p := Params{
		SimplifyMinMeters:   5.0,
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
	}

	pl := New(p, nil)

	result, _, _, err := pl.Run(context.Background(), points)
	require.NoError(t, err)

	assert.Less(t, len(result), len(points),
		"expected fewer points after pipeline, got %d (was %d)", len(result), len(points))
	t.Logf("before: %d points, after: %d points", len(points), len(result))
}

func TestRunEarlyExitFewPoints(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

	// Три записи с одного места подряд: дедуп схлопывает их в одну, и
	// продолжать конвейер не на чем.
	//
	// Раньше здесь стоял трек из телепортов, который резал фильтр скорости, —
	// но его в составе больше нет, и ранний выход надо показывать стадией,
	// которая в конвейере действительно есть.
	points := []geo.Point{
		{Time: t0.Add(0 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(1 * time.Second), Lon: 37.617300, Lat: 55.755800},
		{Time: t0.Add(2 * time.Second), Lon: 37.617300, Lat: 55.755800},
	}

	p := Params{
		SimplifyMinMeters:   5.0,
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
	}

	pl := New(p, nil)

	result, _, _, err := pl.Run(context.Background(), points)
	require.NoError(t, err)

	assert.Less(t, len(result), 2, "после схлопывания дублей осталась одна точка")
}

func TestRunEmpty(t *testing.T) {
	p := Params{
		SimplifyMinMeters:   5.0,
		DedupDistanceMeters: 2.0,
		DedupTimeGap:        60 * time.Second,
	}

	pl := New(p, nil)

	result, _, _, err := pl.Run(context.Background(), nil)
	require.NoError(t, err)

	assert.Len(t, result, 0, "expected 0 points for nil input")
}

// ------------------------------------------- новый состав и общий блокнот

// stageNames — имена стадий в порядке применения.
func stageNames(pl *Pipeline) []string {
	out := make([]string, 0, len(pl.stages))
	for _, s := range pl.stages {
		out = append(out, s.Name())
	}
	return out
}

func TestNew_Composition(t *testing.T) {
	// Порядок стадий — это и есть алгоритм сервиса, поэтому он закреплён
	// списком, а не «примерно так». Чистка идёт ПЕРВОЙ, упаковка после неё,
	// дорисовка последней — по тому, что уже признано настоящим.
	pl := New(Params{}, nil)

	assert.Equal(t, []string{
		"sort_by_time",
		"core",
		"deduplicate",
		"collapse_stops",
		"simplify",
		"reachability_guard",
		"fill_gaps",
	}, stageNames(pl))
}

func TestNew_OldFiltersAreOut(t *testing.T) {
	// Четыре прежние стадии из состава убраны: замер показал, что они режут
	// асфальт, а каскадное удаление ядро решает по построению. Файлы и тесты
	// остались в репозитории, но в конвейер не включены.
	//
	// Якорный фильтр стирал рабочую зону Бутово, фильтр скорости уносил 81
	// точку подряд, зацепившись за глюк.
	got := stageNames(New(Params{}, nil))
	for _, gone := range []string{
		"remove_anchor_backtrack",
		"remove_teleports",
		"filter_by_speed",
		"filter_by_acceleration",
	} {
		assert.NotContains(t, got, gone, "стадия %s не должна быть в составе", gone)
	}
}

func TestNew_StagesShareOneState(t *testing.T) {
	// Блокнот один на прогон: ядро в него пишет, упаковка и дорисовка читают.
	// Разные экземпляры блокнота означали бы, что стоянки до упрощения не
	// доходят, и оно снимет их геометрией.
	pl := New(Params{}, nil)
	require.NotNil(t, pl.State())

	seen := 0
	for _, s := range pl.stages {
		switch v := s.(type) {
		case Core:
			assert.Same(t, pl.State(), v.State)
			seen++
		case Simplify:
			assert.Same(t, pl.State(), v.State)
			seen++
		case ReachabilityGuard:
			assert.Same(t, pl.State(), v.State)
			seen++
		case FillGaps:
			assert.Same(t, pl.State(), v.State)
			seen++
		}
	}
	assert.Equal(t, 4, seen, "блокнот нужен четырём стадиям")
}

func TestRun_ResetsStateBetweenRuns(t *testing.T) {
	// Блокнот описывает ОДИН прогон. Конвейер создаётся на запрос, но если
	// его переиспользуют, второй прогон обязан начинаться с чистого листа —
	// иначе страж возьмёт снимок от прошлого трека.
	pl := New(Params{SimplifyMinMeters: 5}, nil)
	pl.State().Guarded = 7
	pl.State().Must = map[PointKey]struct{}{{Unix: 1}: {}}
	pl.State().BeforePacking = []geo.Point{{Lon: 1, Lat: 1}}
	pl.State().Synthetic = []bool{true}

	pts := road(20, 60, 10, 0, 0.01, 0)
	_, _, _, err := pl.Run(context.Background(), pts)
	require.NoError(t, err)

	assert.Zero(t, pl.State().Guarded, "счётчик стража обязан обнулиться")
	assert.Empty(t, pl.State().Synthetic, "пометки прошлого прогона обязаны уйти")
}

func TestRun_KeepsStatsOnStageError(t *testing.T) {
	// Стадия упала — статистика по уже пройденным обязана дойти наружу.
	// Без неё непонятно, где именно сломалось, а это первое, что спрашивают.
	pl := &Pipeline{
		state:  &RunState{},
		stages: []Stage{SortByTime{}, failingStage{}, SortByTime{}},
	}

	_, _, stats, err := pl.Run(context.Background(), road(10, 60, 10, 0, 0.01, 0))
	require.Error(t, err)

	require.Len(t, stats, 2, "две стадии успели отработать: удачная и упавшая")
	assert.Equal(t, "sort_by_time", stats[0].Name)
	assert.Equal(t, "боевая", stats[1].Name)
	assert.NotEmpty(t, stats[1].Error, "у упавшей стадии обязана быть причина")
}

// failingStage — стадия, которая всегда падает.
type failingStage struct{}

func (failingStage) Name() string { return "боевая" }
func (failingStage) Apply(context.Context, []geo.Point) ([]geo.Point, []string, error) {
	return nil, nil, errors.New("сломалась")
}

func TestRun_ExtraCarriesDetails(t *testing.T) {
	// Подробности ядра и дорисовки уходят в debug-ручку: по ним разбирают
	// спорные маршруты, а по числу точек до/после ничего не понять.
	pl := New(Params{SimplifyMinMeters: 5}, nil)
	_, _, stats, err := pl.Run(context.Background(), road(40, 60, 10, 0, 0.01, 0))
	require.NoError(t, err)

	byName := map[string]StageStats{}
	for _, s := range stats {
		byName[s.Name] = s
	}

	assert.NotEmpty(t, byName["core"].Extra, "ядро обязано рассказать о себе")
	assert.NotEmpty(t, byName["fill_gaps"].Extra, "дорисовка тоже")
	assert.Empty(t, byName["sort_by_time"].Extra, "простой стадии рассказывать нечего")
}

func TestRun_WithoutRouterStillWorks(t *testing.T) {
	// Маршрутизатор не настроен или лежит: сервис обязан отдать результат,
	// а не упасть. Чистка и дорисовка при этом честно предупреждают.
	pl := New(Params{SimplifyMinMeters: 5}, nil)

	pts := road(40, 60, 10, 0, 0.01, 0)
	got, warns, _, err := pl.Run(context.Background(), pts)
	require.NoError(t, err)

	assert.NotEmpty(t, got, "трек не должен исчезнуть")
	assert.NotEmpty(t, warns, "молчать о пропущенных стадиях нельзя")
}

// pipeRouter — настроенный маршрутизатор, ничего не знающий о дорогах.
type pipeRouter struct{}

func (pipeRouter) Snap(_ context.Context, pts []geo.Point) ([]float64, []bool, []string) {
	snaps, ok := make([]float64, len(pts)), make([]bool, len(pts))
	for i := range pts {
		snaps[i], ok[i] = 5, true
	}
	return snaps, ok, nil
}

func (pipeRouter) PairDistance(_ context.Context, pairs []osrm.Pair) ([]float64, []bool, []string) {
	return make([]float64, len(pairs)), make([]bool, len(pairs)), nil
}

func (pipeRouter) RouteGeometry(context.Context, geo.Point, geo.Point) (*osrm.Route, bool) {
	return nil, false
}

func TestNew_WithRouterWiresCleaningAndFilling(t *testing.T) {
	// С маршрутизатором ядро и дорисовка обязаны быть НАСТРОЕНЫ, а не просто
	// присутствовать в списке: иначе конвейер молча работает вхолостую.
	pl := New(Params{SimplifyMinMeters: 5}, pipeRouter{})

	var core Core
	var fill FillGaps
	for _, s := range pl.stages {
		switch v := s.(type) {
		case Core:
			core = v
		case FillGaps:
			fill = v
		}
	}
	require.NotNil(t, core.Engine, "ядру нужен движок")
	assert.NotNil(t, core.Engine.Snap, "и источник снэпов")
	assert.NotNil(t, core.Engine.Road, "и источник расстояний по дорогам")
	require.NotNil(t, fill.Routes, "дорисовке нужен источник путей")

	// И конвейер на этом работает без предупреждений о пропущенных стадиях.
	got, warns, _, err := pl.Run(context.Background(), road(40, 60, 10, 0, 0.01, 0))
	require.NoError(t, err)
	assert.NotEmpty(t, got)
	assert.Empty(t, warns, "настроенному конвейеру предупреждать не о чем")
}

func TestRun_CancelledReturnsStats(t *testing.T) {
	// Отмена до первой стадии: наружу уходит ошибка, а не половина результата.
	pl := New(Params{SimplifyMinMeters: 5}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, stats, err := pl.Run(ctx, road(20, 60, 10, 0, 0.01, 0))
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, stats, "до первой стадии дойти не успели")
}

func TestExtraOf_GuardSilentWhenNothingRestored(t *testing.T) {
	// Страж рассказывает о себе только когда что-то вернул. Пустой раздел в
	// отчёте — шум: его читают глазами при разборе.
	pl := New(Params{SimplifyMinMeters: 5}, nil)
	assert.Nil(t, pl.extraOf("reachability_guard"), "молчит, пока возвращать нечего")

	pl.state.Guarded = 3
	assert.Equal(t, map[string]any{"restored": 3}, pl.extraOf("reachability_guard"))
}

func TestExtraOf_WithoutState(t *testing.T) {
	// Конвейер можно собрать и вручную, без блокнота: подробностям тогда
	// взяться неоткуда, но падать нельзя.
	pl := &Pipeline{stages: []Stage{SortByTime{}}}
	assert.Nil(t, pl.extraOf("core"))
}

// ------------------------------------------------------------ бюджет времени

func TestRun_CoreGetsShareNotWholeBudget(t *testing.T) {
	// Чистка — самая дорогая стадия: она ходит в маршрутизатор десятками
	// запросов и перестраивает цепочку до дюжины раз. Отдай ей весь бюджет —
	// дорисовке не останется ничего, и километраж потеряет свои 5 %.
	//
	// Проверяем, что ядру приходит СВОЙ, более короткий срок.
	pl := New(Params{SimplifyMinMeters: 5}, pipeRouter{})

	var coreAt, fillAt time.Time
	for i, s := range pl.stages {
		switch s.(type) {
		case Core:
			pl.stages[i] = deadlineSpy{name: "core", at: &coreAt, inner: s}
		case FillGaps:
			// Обёртка БЕЗ своей доли — как и сама дорисовка. Дай ей долю, и
			// тест перестал бы отличать «доля только ядру» от «доля всем».
			pl.stages[i] = plainSpy{name: "fill_gaps", at: &fillAt, inner: s}
		}
	}

	const budget = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	outer, _ := ctx.Deadline()

	_, _, _, err := pl.Run(ctx, road(40, 60, 10, 0, 0.01, 0))
	require.NoError(t, err)

	// Ядру — свой, более ранний срок.
	require.False(t, coreAt.IsZero(), "ядру обязан прийти срок")
	assert.True(t, coreAt.Before(outer), "и он раньше общего")
	assert.True(t, coreAt.After(time.Now()), "но не в прошлом")

	// А дорисовке — общий, без укорачивания. Иначе она получала бы долю от
	// доли и на длинных треках не успевала бы ничего.
	require.False(t, fillAt.IsZero())
	assert.WithinDuration(t, outer, fillAt, time.Millisecond,
		"дорисовке свой срок не выдаём: ей остаётся весь хвост бюджета")
}

func TestRun_NoDeadlineNoSubBudget(t *testing.T) {
	// Без срока снаружи делить нечего: своего срока стадиям не выдаём.
	pl := New(Params{SimplifyMinMeters: 5}, pipeRouter{})

	var coreAt time.Time
	for i, s := range pl.stages {
		if _, ok := s.(Core); ok {
			pl.stages[i] = deadlineSpy{name: "core", at: &coreAt, inner: s}
		}
	}

	_, _, _, err := pl.Run(context.Background(), road(40, 60, 10, 0, 0.01, 0))
	require.NoError(t, err)
	assert.True(t, coreAt.IsZero(), "срока быть не должно")
}

// deadlineSpy — обёртка, запоминающая срок, с которым стадию позвали.
type deadlineSpy struct {
	name  string
	at    *time.Time
	inner Stage
}

func (d deadlineSpy) Name() string { return d.name }

func (d deadlineSpy) Apply(ctx context.Context, pts []geo.Point) ([]geo.Point, []string, error) {
	if dl, ok := ctx.Deadline(); ok {
		*d.at = dl
	}
	return d.inner.Apply(ctx, pts)
}

// BudgetShare прокидывается насквозь: обёртка не должна менять то, что она
// оборачивает, иначе тест мерил бы не ту стадию.
func (d deadlineSpy) BudgetShare() float64 {
	return d.inner.(budgeted).BudgetShare()
}

// plainSpy — то же, но БЕЗ своей доли бюджета: для стадий, которым доля не
// положена.
type plainSpy struct {
	name  string
	at    *time.Time
	inner Stage
}

func (p plainSpy) Name() string { return p.name }

func (p plainSpy) Apply(ctx context.Context, pts []geo.Point) ([]geo.Point, []string, error) {
	if dl, ok := ctx.Deadline(); ok {
		*p.at = dl
	}
	return p.inner.Apply(ctx, pts)
}

// --- признак «результат неполный» ---------------------------------------

// degradedTrack — ровная езда по прямой, десять минут с шагом в минуту.
// Ничего особенного: нужен просто трек, который конвейер пройдёт целиком.
func degradedTrack() []geo.Point {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	pts := make([]geo.Point, 10)
	for i := range pts {
		pts[i] = geo.Point{
			Time: t0.Add(time.Duration(i) * time.Minute),
			Lon:  37.6173 + float64(i)*0.01,
			Lat:  55.7558,
		}
	}
	return pts
}

// Без маршрутизатора чистка и дорисовка пропускают трек насквозь. Результат
// при этом отдаётся, но он не тот, каким должен быть: спуфинг не убран,
// километраж занижен на дырах. Отдавать такое молча нельзя.
func TestRunState_DegradedWithoutRouter(t *testing.T) {
	pl := New(Params{SimplifyMinMeters: 5}, nil)
	out, warnings, _, err := pl.Run(context.Background(), degradedTrack())
	require.NoError(t, err)
	require.NotEmpty(t, out)

	assert.True(t, pl.State().Degraded,
		"без маршрутизатора результат неполный — это обязано быть видно вызывающему")
	assert.NotEmpty(t, warnings, "и сказано словами")
}

// С исправным маршрутизатором и достаточным бюджетом оговорок быть не должно:
// иначе пометка обесценится и её перестанут читать.
func TestRunState_NotDegradedOnHealthyRun(t *testing.T) {
	pl := New(Params{SimplifyMinMeters: 5}, pipeRouter{})
	_, _, _, err := pl.Run(context.Background(), degradedTrack())
	require.NoError(t, err)

	assert.False(t, pl.State().Degraded,
		"исправный прогон не должен помечаться неполным")
}
