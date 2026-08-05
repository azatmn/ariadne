package pipeline

import (
	"context"

	"ariadne/internal/geo"
)

// Упрощение Рамера — Дугласа — Пекера: выбрасывает точки, лежащие почти на
// прямой между соседями. Форма трека сохраняется, число точек падает втрое.
//
// Две правки против исходной реализации, обе не косметические.
//
// Первая — `State.Must`. Упрощение судит ТОЛЬКО по форме, а часть точек несёт
// смысл помимо формы. Схлопнутая стоянка — ровно такой случай: машина заехала
// на заправку у трассы, простояла 32 минуты и выехала, ядро сжало это в одну
// точку, и лежит она почти на прямой между въездом и выездом. Геометрия её не
// отличает от лишней, и стоянка исчезает бесследно. Замер на прототипе: без
// защиты терялось семь подтверждённых стоянок из 96, с ней — ни одной, ценой
// 26 точек из 12153.
//
// Решение общее, а не «не трогать стоянки»: помеченные точки режут трек на
// куски, и каждый кусок упрощается сам по себе. На дорисовке по дорогам
// понадобится ровно тот же список.
//
// Вторая — рекурсия развёрнута в стек. В худшем случае разбиение идёт по одной
// точке за раз, и глубина равна длине трека: при `MAX_POINTS=50000` это
// пятьдесят тысяч кадров.
type Simplify struct {
	MinMeters float64

	// State — общий блокнот прогона. Нужен ради `Must`; без него упрощение
	// работает как обычный RDP.
	State *RunState
}

func (Simplify) Name() string { return "simplify" }

// span — участок между двумя опорами, который ещё предстоит разобрать.
type span struct{ start, end int }

func (s Simplify) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if len(points) <= 2 {
		return points, nil, nil
	}

	keep := make([]bool, len(points))
	keep[0], keep[len(points)-1] = true, true

	// Опоры — концы трека и всё, что помечено обязательным. Между соседними
	// опорами трек упрощается независимо.
	anchors := []int{0}
	for i := 1; i < len(points)-1; i++ {
		if s.must(points[i]) {
			keep[i] = true
			anchors = append(anchors, i)
		}
	}
	anchors = append(anchors, len(points)-1)

	stack := make([]span, 0, len(anchors))
	for i := 0; i < len(anchors)-1; i++ {
		stack = append(stack, span{anchors[i], anchors[i+1]})
	}

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.end-cur.start < 2 {
			continue
		}

		// Самая удалённая от хорды точка участка.
		maxDist, maxIdx := 0.0, cur.start
		for i := cur.start + 1; i < cur.end; i++ {
			if d := geo.CrossTrackDistance(points[i], points[cur.start], points[cur.end]); d > maxDist {
				maxDist, maxIdx = d, i
			}
		}
		if maxDist <= s.MinMeters {
			continue // весь участок укладывается в допуск — держим только концы
		}

		keep[maxIdx] = true
		stack = append(stack, span{cur.start, maxIdx}, span{maxIdx, cur.end})
	}

	result := make([]geo.Point, 0, len(points))
	for i, k := range keep {
		if k {
			result = append(result, points[i])
		}
	}
	return result, nil, nil
}

// must — обязана ли точка уцелеть независимо от геометрии.
func (s Simplify) must(p geo.Point) bool {
	if s.State == nil || len(s.State.Must) == 0 {
		return false
	}
	_, yes := s.State.Must[KeyOf(p)]
	return yes
}
