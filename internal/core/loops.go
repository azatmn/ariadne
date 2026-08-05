package core

import (
	"context"
	"time"

	"ariadne/internal/geo"
)

// Правило петель: за окно намотано много, а машина осталась примерно на месте.
//
// Это «самосогласованная» подделка: гладкая петля с правдоподобными скоростями,
// без единого телепорта. Каждый переход по отдельности проверку проходит
// (60–107 км/ч), поэтому поточечные фильтры бессильны по построению. Видно
// только на интервале: за час трек намотал 85 км, мотаясь между двумя точками,
// и вернулся туда же.
//
// Меряем так, как практики телематики меряют по одометру, только вместо
// одометра — путь по дорогам между концами окна. Честная езда держится ниже
// 1.6–1.8, петля даёт 3.3 и выше.
//
// Порядок важен: правило зовётся ПОСЛЕ проверки переходов по дорогам. На сыром
// треке телепорты раздувают путь в каждом окне, и мера теряет смысл.

const (
	// LoopWindow — окно, на котором ищем накрутку.
	//
	// Полчаса: короче — честный городской заезд выглядит петлёй, длиннее —
	// накрутка размазывается по перегону и уходит под порог.
	LoopWindow = 30 * time.Minute

	// LoopEnter — во сколько раз путь превысил дорогу, чтобы начать резать.
	LoopEnter = 3.0

	// LoopStay — и до каких пор продолжаем резать.
	//
	// Гистерезис обязателен: с одним порогом хвосты петли ложатся ровно на
	// границу и выживают, а петля с уцелевшими хвостами — это всё ещё петля.
	LoopStay = 1.8

	// LoopMinM — окна с меньшим пробегом не разбираем: там нечего мерить,
	// отношение шумит на любой погрешности привязки к дороге.
	LoopMinM = 8000.0

	// LoopMinPoints — и окна короче четырёх точек тоже: накрутку не на чем
	// показать, а концы окна почти совпадают с самим путём.
	LoopMinPoints = 4

	// LoopRoadFloorM — нижний зажим знаменателя.
	//
	// Это защита от деления на ноль, а не порог поведения: LoopMinM уже
	// требует восьми километров пути, поэтому отношение с зажатым
	// знаменателем всегда не меньше шестнадцати и вердикт не меняет.
	// Маршрутизатор возвращает ноль, когда оба конца окна сели на одну точку
	// графа, — а это как раз петля и есть.
	LoopRoadFloorM = 500.0

	// LoopPenalty — штраф каждой точке окна, признанного петлёй.
	//
	// Штрафуется всё окно, а не отдельная точка: подделка тут связная, и
	// виноватой одной точки в ней не бывает.
	LoopPenalty = 0.8
)

// loopWindow — окно, дошедшее до вопроса маршрутизатору.
type loopWindow struct {
	at   []int   // позиции в цепочке
	path float64 // сколько намотано по точкам
	key  askedID // чем опознаётся уже разобранное окно
}

// CheckLoops ищет петли в выбранной цепочке и штрафует их точки.
// Возвращает число окон, признанных петлями.
func CheckLoops(ctx context.Context, road RoadClient, pts []geo.Point, chain []int, st *RoadState) int {
	if road == nil || len(chain) < LoopMinPoints || ctx.Err() != nil {
		return 0
	}

	var pairs []Pair
	var wins []loopWindow

	for _, at := range loopSlice(pts, chain) {
		if len(at) < LoopMinPoints {
			continue
		}
		a, b := pts[chain[at[0]]], pts[chain[at[len(at)-1]]]
		path := chainPath(pts, chain, at)
		if path < LoopMinM {
			continue
		}
		// Окно судится один раз за прогон. Проходов у цикла до дюжины, и
		// переспрашивать то же самое значит платить за это временем.
		key := askKey(a, b)
		if _, done := st.loops[key]; done {
			continue
		}
		pairs = append(pairs, Pair{A: a, B: b})
		wins = append(wins, loopWindow{at: at, path: path, key: key})
	}
	if len(pairs) == 0 {
		return 0
	}

	dist, ok, _ := road.PairDistance(ctx, pairs)

	found := 0
	hot := false // режем ли сейчас: состояние гистерезиса
	for k, w := range wins {
		st.loops[w.key] = struct{}{}

		// Нет ответа — окно не судим и состояние НЕ трогаем. «Не знаю» это не
		// «чисто»: сброс дал бы петле бесплатный выход через дыру в графе.
		if k >= len(ok) || k >= len(dist) || !ok[k] {
			continue
		}

		ratio := w.path / max(dist[k], LoopRoadFloorM)
		limit := LoopEnter
		if hot {
			limit = LoopStay
		}
		if ratio <= limit {
			hot = false
			continue
		}

		hot = true
		found++
		for _, pos := range w.at {
			st.Penalty[chain[pos]] += LoopPenalty
		}
	}
	return found
}

// loopSlice режет цепочку на окна по времени.
//
// Отсчёт ведётся от первой точки ТЕКУЩЕГО окна, а не от начала цепочки:
// иначе после первого же разреза все следующие окна съехали бы. Точка ровно
// на границе остаётся в текущем окне.
func loopSlice(pts []geo.Point, chain []int) [][]int {
	var wins [][]int
	cur := []int{0}
	t0 := pts[chain[0]].Time

	for k := 1; k < len(chain); k++ {
		if t := pts[chain[k]].Time; t.Sub(t0) > LoopWindow {
			wins = append(wins, cur)
			cur, t0 = []int{k}, t
			continue
		}
		cur = append(cur, k)
	}
	return append(wins, cur)
}

// chainPath — длина пути по точкам окна.
//
// Считается по ВЫБРАННОЙ цепочке: выброшенные точки в намотанное не входят,
// иначе мера мерила бы мусор, который чистка уже признала подделкой.
func chainPath(pts []geo.Point, chain, at []int) float64 {
	var total float64
	for k := 1; k < len(at); k++ {
		total += geo.Haversine(pts[chain[at[k-1]]], pts[chain[at[k]]])
	}
	return total
}
