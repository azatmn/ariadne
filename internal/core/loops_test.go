package core

import (
	"context"
	"math"
	"testing"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Тесты правила петель. Правило судит ОКНО, а не переход, поэтому почти всё
// здесь — про нарезку окон, про то, какие окна вообще доходят до вопроса, и
// про гистерезис между соседними окнами.

// loopWins — nWin окон по ptsPerWin точек.
//
// Внутри окна шаг stepSec секунд, между окнами пауза заведомо длиннее
// LoopWindow, поэтому нарезка обязана положить каждую пачку в своё окно.
// Точки едут строго на восток, так что длина пути внутри окна считается и
// глазами: (ptsPerWin-1) шагов по stepDeg.
func loopWins(nWin, ptsPerWin, stepSec int, stepDeg float64) []geo.Point {
	const winGapSec = 4000 // заведомо больше LoopWindow
	out := make([]geo.Point, 0, nWin*ptsPerWin)
	for w := range nWin {
		for i := range ptsPerWin {
			g := w*ptsPerWin + i
			out = append(out, at(w*winGapSec+i*stepSec, 10+float64(g)*stepDeg, 0))
		}
	}
	return out
}

// wholeChain — цепочка из всех точек подряд.
func wholeChain(pts []geo.Point) []int {
	chain := make([]int, len(pts))
	for i := range chain {
		chain[i] = i
	}
	return chain
}

// winPath — длина пути по точкам окна w (окна нумеруются с нуля).
func winPath(pts []geo.Point, ptsPerWin, w int) float64 {
	return geo.TotalLength(pts[w*ptsPerWin : (w+1)*ptsPerWin])
}

// answerRatios — источник дорог, отвечающий так, чтобы у окна w получилось
// ровно заданное отношение «намотал / по дорогам». nil в ratios означает
// «проезда нет».
func answerRatios(pts []geo.Point, ptsPerWin int, ratios []*float64) *fakeRoads {
	return &fakeRoads{dist: func(a, _ geo.Point) *float64 {
		for w := range ratios {
			if !pts[w*ptsPerWin].Time.Equal(a.Time) {
				continue
			}
			if ratios[w] == nil {
				return nil
			}
			d := winPath(pts, ptsPerWin, w) / *ratios[w]
			return &d
		}
		return nil
	}}
}

func ratio(v float64) *float64 { return &v }

// ---------------------------------------------------------- вырожденный вход

func TestCheckLoops_DegenerateInput(t *testing.T) {
	// Ни один из этих случаев не должен ни падать, ни ходить в сеть.
	cases := []struct {
		name  string
		pts   []geo.Point
		chain []int
	}{
		{"пусто", nil, nil},
		{"точки без цепочки", loopWins(1, 20, 60, 0.005), nil},
		{"цепочка из одной точки", loopWins(1, 20, 60, 0.005), []int{0}},
		{"цепочка из трёх точек", loopWins(1, 20, 60, 0.005), []int{0, 1, 2}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			road := &fakeRoads{}
			got := CheckLoops(context.Background(), road, c.pts, c.chain, NewRoadState())
			assert.Zero(t, got, "вердиктов быть не должно")
			assert.Empty(t, road.asked, "спрашивать нечего")
		})
	}
}

func TestCheckLoops_NoClient(t *testing.T) {
	// Без источника дорог правило обязано молчать, а не гадать.
	pts := loopWins(1, 20, 60, 0.005)
	st := NewRoadState()
	assert.Zero(t, CheckLoops(context.Background(), nil, pts, wholeChain(pts), st))
	assert.Empty(t, st.Penalty)
}

func TestCheckLoops_CancelledContext(t *testing.T) {
	// Отменённый контекст — выходим до сети, а не после.
	pts := loopWins(1, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{ratio(10)})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.Zero(t, CheckLoops(ctx, road, pts, wholeChain(pts), NewRoadState()))
	assert.Empty(t, road.asked, "в отменённом контексте в сеть не ходим")
}

// ------------------------------------------------- какие окна доходят до сети

func TestCheckLoops_ShortWindowSkipped(t *testing.T) {
	// Окно короче четырёх точек не разбираем: мерить накрутку не на чем.
	// Путь при этом заведомо длинный — отсекает именно число точек.
	pts := loopWins(1, 3, 60, 0.05)
	require.Greater(t, geo.TotalLength(pts), LoopMinM, "путь должен пройти порог")

	road := &fakeRoads{}
	assert.Zero(t, CheckLoops(context.Background(), road, pts, wholeChain(pts), NewRoadState()))
	assert.Empty(t, road.asked, "окно из трёх точек спрашивать не о чем")
}

func TestCheckLoops_ShortWindowSkippedInsideLongChain(t *testing.T) {
	// То же, но цепочка длинная: короткое окно обязано отсеиваться само по
	// себе, а не за компанию с проверкой длины цепочки в начале. Оба окна
	// набирают нужный путь, разница только в числе точек.
	pts := []geo.Point{
		// окно из четырёх точек, шаг ~3.3 км
		at(0, 10.00, 0), at(300, 10.03, 0), at(600, 10.06, 0), at(900, 10.09, 0),
		// окно из трёх точек, шаг ~5.6 км
		at(5000, 10.15, 0), at(5300, 10.20, 0), at(5600, 10.25, 0),
	}
	require.Greater(t, geo.TotalLength(pts[0:4]), LoopMinM, "первое окно должно пройти по пути")
	require.Greater(t, geo.TotalLength(pts[4:7]), LoopMinM, "второе тоже")

	road := &fakeRoads{}
	CheckLoops(context.Background(), road, pts, wholeChain(pts), NewRoadState())

	require.Len(t, road.asked, 1, "спрашиваем только про окно из четырёх точек")
	assert.True(t, road.asked[0].A.Time.Equal(pts[0].Time))
	assert.True(t, road.asked[0].B.Time.Equal(pts[3].Time))
}

func TestCheckLoops_ShortPathSkippedInsideLongChain(t *testing.T) {
	// Зеркально: окон много, точек в каждом хватает, но одно окно намотало
	// мало — его и только его пропускаем.
	pts := append(loopWins(1, 20, 60, 0.005), loopWins(1, 20, 60, 0.001)...)
	for i := 20; i < 40; i++ {
		pts[i].Time = pts[i].Time.Add(2 * LoopWindow)
	}
	require.Greater(t, geo.TotalLength(pts[0:20]), LoopMinM)
	require.Less(t, geo.TotalLength(pts[20:40]), LoopMinM)

	road := &fakeRoads{}
	CheckLoops(context.Background(), road, pts, wholeChain(pts), NewRoadState())

	require.Len(t, road.asked, 1, "спрашиваем только про окно с длинным путём")
	assert.True(t, road.asked[0].A.Time.Equal(pts[0].Time))
}

func TestCheckLoops_ShortPathSkipped(t *testing.T) {
	// Окно, в котором намотано меньше LoopMinM, не разбираем: там нечего
	// мерить, отношение шумит на любой погрешности привязки.
	pts := loopWins(1, 20, 60, 0.001)
	require.Less(t, geo.TotalLength(pts), LoopMinM, "путь должен быть ниже порога")

	road := &fakeRoads{}
	assert.Zero(t, CheckLoops(context.Background(), road, pts, wholeChain(pts), NewRoadState()))
	assert.Empty(t, road.asked)
}

func TestCheckLoops_WindowBoundary(t *testing.T) {
	// Граница нарезки: точка ровно на LoopWindow от начала окна остаётся в
	// нём, а на секунду позже — начинает новое. Проверяется тем, о чём в
	// итоге спросили.
	const step = 0.026 // ~2.9 км на шаг, четырёх точек хватит на порог пути

	t.Run("ровно на границе — одно окно", func(t *testing.T) {
		win := int(LoopWindow.Seconds())
		pts := []geo.Point{
			at(0, 10, 0),
			at(win/3, 10+step, 0),
			at(2*win/3, 10+2*step, 0),
			at(win, 10+3*step, 0),
		}
		require.Greater(t, geo.TotalLength(pts), LoopMinM)

		road := answerRatios(pts, 4, []*float64{ratio(1)})
		CheckLoops(context.Background(), road, pts, wholeChain(pts), NewRoadState())

		require.Len(t, road.asked, 1, "все четыре точки в одном окне")
		assert.True(t, road.asked[0].A.Time.Equal(pts[0].Time))
		assert.True(t, road.asked[0].B.Time.Equal(pts[3].Time))
	})

	t.Run("на секунду позже — окно рвётся", func(t *testing.T) {
		win := int(LoopWindow.Seconds())
		pts := []geo.Point{
			at(0, 10, 0),
			at(win/3, 10+step, 0),
			at(2*win/3, 10+2*step, 0),
			at(win+1, 10+3*step, 0),
		}

		road := &fakeRoads{}
		assert.Zero(t, CheckLoops(context.Background(), road, pts, wholeChain(pts), NewRoadState()))
		assert.Empty(t, road.asked, "оба обрывка короче четырёх точек")
	})
}

func TestCheckLoops_WindowStartsFromItsOwnFirstPoint(t *testing.T) {
	// Нарезка не скользящая: отсчёт ведётся от первой точки ТЕКУЩЕГО окна,
	// а не от начала цепочки. Иначе после первого разреза все следующие
	// окна съехали бы.
	pts := loopWins(3, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{ratio(1), ratio(1), ratio(1)})

	CheckLoops(context.Background(), road, pts, wholeChain(pts), NewRoadState())

	require.Len(t, road.asked, 3, "три пачки — три окна")
	for w, p := range road.asked {
		assert.True(t, p.A.Time.Equal(pts[w*20].Time), "окно %d начинается со своей первой точки", w)
		assert.True(t, p.B.Time.Equal(pts[w*20+19].Time), "окно %d кончается своей последней", w)
	}
}

// ------------------------------------------------------------------- вердикт

func TestCheckLoops_HonestWindowUntouched(t *testing.T) {
	// Дорога почти равна намотанному — честная езда, трогать нечего.
	pts := loopWins(1, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{ratio(1.0)})
	st := NewRoadState()

	assert.Zero(t, CheckLoops(context.Background(), road, pts, wholeChain(pts), st))
	assert.Empty(t, st.Penalty, "честное окно штрафовать не за что")
}

func TestCheckLoops_LoopPenalisesEveryPoint(t *testing.T) {
	// Намотал втрое с лишним больше дороги — петля. Штраф ложится на КАЖДУЮ
	// точку окна: подделка тут связная, виноватой одной точки не бывает.
	pts := loopWins(1, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{ratio(LoopEnter + 0.5)})
	st := NewRoadState()

	assert.Equal(t, 1, CheckLoops(context.Background(), road, pts, wholeChain(pts), st))

	require.Len(t, st.Penalty, len(pts), "под штраф идёт всё окно")
	for i := range pts {
		assert.InDelta(t, LoopPenalty, st.Penalty[i], 1e-9, "точка %d", i)
	}
}

func TestCheckLoops_EnterThresholdIsStrict(t *testing.T) {
	// Ровно на пороге входа — ещё не петля: сравнение строгое.
	pts := loopWins(1, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{ratio(LoopEnter)})
	st := NewRoadState()

	assert.Zero(t, CheckLoops(context.Background(), road, pts, wholeChain(pts), st))
	assert.Empty(t, st.Penalty)
}

func TestCheckLoops_PenaltyAccumulatesOverExisting(t *testing.T) {
	// Штраф правила складывается с уже накопленным, а не затирает его:
	// улики разных правил суммируются, и решает потом цепочка.
	pts := loopWins(1, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{ratio(LoopEnter + 0.5)})
	st := NewRoadState()
	st.Penalty[0] = 1.0

	CheckLoops(context.Background(), road, pts, wholeChain(pts), st)
	assert.InDelta(t, 1.0+LoopPenalty, st.Penalty[0], 1e-9)
}

// --------------------------------------------------------------- гистерезис

func TestCheckLoops_Hysteresis(t *testing.T) {
	// Порог входа выше порога продолжения. Без этого хвосты петли ложатся
	// ровно на границу и выживают.
	//
	// Окна 1 и 3 имеют ОДИНАКОВОЕ отношение, но разный вердикт — вся разница
	// в том, режем ли мы уже.
	mid := (LoopEnter + LoopStay) / 2 // между порогами: 2.4
	pts := loopWins(4, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{
		ratio(LoopEnter + 0.5), // 0: входим
		ratio(mid),             // 1: ниже входа, выше продолжения — режем
		ratio(LoopStay - 0.3),  // 2: упали ниже продолжения — выходим
		ratio(mid),             // 3: то же, что окно 1, но заново входить нечем
	})
	st := NewRoadState()

	assert.Equal(t, 2, CheckLoops(context.Background(), road, pts, wholeChain(pts), st))

	for i := range 40 {
		assert.InDelta(t, LoopPenalty, st.Penalty[i], 1e-9, "точка %d из окон 0-1", i)
	}
	for i := 40; i < 80; i++ {
		assert.Zero(t, st.Penalty[i], "точка %d из окон 2-3", i)
	}
}

func TestCheckLoops_NoRouteKeepsHysteresis(t *testing.T) {
	// Окно без ответа маршрутизатора не судится — и не сбрасывает состояние.
	// «Не знаю» это не «чисто»: сброс дал бы петле бесплатный выход.
	mid := (LoopEnter + LoopStay) / 2
	pts := loopWins(3, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{
		ratio(LoopEnter + 0.5), // входим
		nil,                    // ответа нет
		ratio(mid),             // держимся на пороге продолжения
	})
	st := NewRoadState()

	assert.Equal(t, 2, CheckLoops(context.Background(), road, pts, wholeChain(pts), st))
	for i := 20; i < 40; i++ {
		assert.Zero(t, st.Penalty[i], "окно без ответа не штрафуем")
	}
	for i := 40; i < 60; i++ {
		assert.InDelta(t, LoopPenalty, st.Penalty[i], 1e-9, "режем дальше по нижнему порогу")
	}
}

func TestCheckLoops_HonestWindowResetsHysteresis(t *testing.T) {
	// Зеркало предыдущего: честное окно состояние сбрасывает.
	mid := (LoopEnter + LoopStay) / 2
	pts := loopWins(3, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{
		ratio(LoopEnter + 0.5),
		ratio(1.0),
		ratio(mid),
	})
	st := NewRoadState()

	assert.Equal(t, 1, CheckLoops(context.Background(), road, pts, wholeChain(pts), st))
	for i := 40; i < 60; i++ {
		assert.Zero(t, st.Penalty[i], "после честного окна порог снова высокий")
	}
}

// -------------------------------------------------------------------- память

func TestCheckLoops_AsksEachWindowOnce(t *testing.T) {
	// Окно судится один раз за прогон. Проходов у цикла до дюжины, и
	// переспрашивать то же самое значит платить за это временем.
	pts := loopWins(2, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{ratio(LoopEnter + 0.5), ratio(1.0)})
	st := NewRoadState()
	chain := wholeChain(pts)

	first := CheckLoops(context.Background(), road, pts, chain, st)
	askedAfterFirst := len(road.asked)

	second := CheckLoops(context.Background(), road, pts, chain, st)

	assert.Equal(t, 1, first)
	assert.Zero(t, second, "во второй раз судить нечего")
	assert.Len(t, road.asked, askedAfterFirst, "новых вопросов не задано")
}

func TestCheckLoops_RemembersMissingRoute(t *testing.T) {
	// Отсутствие ответа запоминается наравне с ответом, иначе такое окно
	// спрашивалось бы на каждом проходе.
	pts := loopWins(1, 20, 60, 0.005)
	road := answerRatios(pts, 20, []*float64{nil})
	st := NewRoadState()
	chain := wholeChain(pts)

	CheckLoops(context.Background(), road, pts, chain, st)
	require.Len(t, road.asked, 1)

	CheckLoops(context.Background(), road, pts, chain, st)
	assert.Len(t, road.asked, 1, "про то же окно не переспрашиваем")
}

func TestCheckLoops_LoopMemoryIsSeparateFromRoadChecks(t *testing.T) {
	// Ключи петель и ключи проверки переходов живут раздельно. В прототипе
	// это один словарь, но разной формы ключа; смешать их значит потерять
	// часть вопросов молча.
	pts := loopWins(1, 20, 60, 0.005)
	st := NewRoadState()
	chain := wholeChain(pts)

	// Сначала спрашиваем ту же пару как переход.
	st.asked[askKey(pts[0], pts[19])] = struct{}{}

	road := answerRatios(pts, 20, []*float64{ratio(LoopEnter + 0.5)})
	assert.Equal(t, 1, CheckLoops(context.Background(), road, pts, chain, st),
		"память проверки переходов не должна затыкать правило петель")
}

// ---------------------------------------------------------- защита от нуля

func TestCheckLoops_ZeroRoadDistance(t *testing.T) {
	// Маршрутизатор вернул ноль (концы окна сели на одну точку графа).
	// Делить на это нельзя — знаменатель зажат снизу.
	pts := loopWins(1, 20, 60, 0.005)
	zero := 0.0
	road := &fakeRoads{dist: func(_, _ geo.Point) *float64 { return &zero }}
	st := NewRoadState()

	assert.Equal(t, 1, CheckLoops(context.Background(), road, pts, wholeChain(pts), st))
	for i := range pts {
		assert.False(t, math.IsNaN(st.Penalty[i]), "штраф не должен стать NaN")
		assert.False(t, math.IsInf(st.Penalty[i], 0), "штраф не должен стать бесконечным")
		assert.InDelta(t, LoopPenalty, st.Penalty[i], 1e-9)
	}
}

func TestCheckLoops_NegativeRoadDistance(t *testing.T) {
	// Маршрутизатор ответил отрицательным расстоянием — такого быть не должно,
	// но если случится, петля не имеет права превратиться в «чисто».
	// Без нижнего зажима знаменателя отношение уходит в минус и вердикт
	// переворачивается молча.
	pts := loopWins(1, 20, 60, 0.005)
	neg := -100.0
	road := &fakeRoads{dist: func(_, _ geo.Point) *float64 { return &neg }}
	st := NewRoadState()

	assert.Equal(t, 1, CheckLoops(context.Background(), road, pts, wholeChain(pts), st))
	assert.InDelta(t, LoopPenalty, st.Penalty[0], 1e-9)
}

func TestCheckLoops_ClientReturnsShortAnswer(t *testing.T) {
	// Клиент вернул меньше ответов, чем было вопросов. Контракт этого не
	// допускает, но выходить за границы среза мы не имеем права ни при каком
	// ответе снаружи.
	pts := loopWins(2, 20, 60, 0.005)
	road := &shortRoads{}
	st := NewRoadState()

	assert.NotPanics(t, func() {
		CheckLoops(context.Background(), road, pts, wholeChain(pts), st)
	})
	assert.Empty(t, st.Penalty, "недоданные ответы судить нельзя")
}

// shortRoads — источник, отдающий ответов меньше, чем спрошено.
type shortRoads struct{}

func (shortRoads) PairDistance(_ context.Context, _ []Pair) ([]float64, []bool, []string) {
	return nil, nil, nil
}

// ------------------------------------------------------- цепочка с пропусками

func TestCheckLoops_JudgesChainNotRawTrack(t *testing.T) {
	// Правило смотрит на ВЫБРАННУЮ цепочку, а не на сырой трек: выброшенные
	// точки в намотанный путь не входят. Иначе на сыром треке телепорты
	// раздували бы каждое окно и мера теряла бы смысл.
	pts := loopWins(1, 20, 60, 0.005)
	// В цепочку берём каждую вторую — путь тот же, точек вдвое меньше.
	var chain []int
	for i := 0; i < len(pts); i += 2 {
		chain = append(chain, i)
	}

	road := answerRatios(pts, 20, []*float64{ratio(LoopEnter + 0.5)})
	st := NewRoadState()

	assert.Equal(t, 1, CheckLoops(context.Background(), road, pts, chain, st))
	assert.Len(t, st.Penalty, len(chain), "штрафуются только точки цепочки")
	for _, i := range chain {
		assert.InDelta(t, LoopPenalty, st.Penalty[i], 1e-9)
	}
}

// Правило ходит в сеть, поэтому мерить надо не сеть, а свою часть: нарезку
// окон и подсчёт намотанного. На 20 тысячах точек это должно быть даром.
func BenchmarkCheckLoops(b *testing.B) {
	pts := drive(20000, 30, 10.0, 0.0, 0.002, 0)
	chain := wholeChain(pts)
	road := &fakeRoads{}
	b.ReportAllocs()
	for b.Loop() {
		CheckLoops(context.Background(), road, pts, chain, NewRoadState())
	}
}
