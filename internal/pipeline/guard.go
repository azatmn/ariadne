package pipeline

import (
	"context"
	"fmt"

	"ariadne/internal/core"
	"ariadne/internal/geo"
)

// Страж достижимости — последняя проверка после упаковки.
//
// Упаковка (дедуп, схлопывание стоянок, упрощение) судит по геометрии и про
// физику не знает. На `da72a9aa` это вышло так: трекер выгрузил буфер пачкой,
// поставив десятку записей ОДНО время 09:34:35. Ядро провело цепочку через всю
// пачку — каждый шаг внутри допуска. Упрощение увидело, что промежуточные точки
// лежат на прямой, и сняло их, а между оставшимися оказалось 450 метров за ноль
// секунд.
//
// Проверяем не «пачки» и не «стоянки», а само условие достижимости — то же,
// что использует ядро. Так страж закрывает и любой будущий случай, откуда бы он
// ни взялся. Случай редкий, поэтому возвращаем промежуток целиком, не выгадывая.
type ReachabilityGuard struct {
	// State — общий блокнот прогона. Нужен снимок трека ДО упаковки: сравнить
	// не с чем, если не помнить, что там было.
	State *RunState
}

func (ReachabilityGuard) Name() string { return "reachability_guard" }

func (g ReachabilityGuard) Apply(_ context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if g.State == nil || len(g.State.BeforePacking) == 0 || len(points) < 2 {
		return points, nil, nil
	}
	before := g.State.BeforePacking

	// Где каждая оставшаяся точка стояла в снимке. Упаковка только удаляет,
	// поэтому вход обязан быть подпоследовательностью снимка; идём двумя
	// указателями, а не по ключам — так повторы координат не путают позиции.
	pos, ok := embed(points, before)
	if !ok {
		// Снимок не от этого прогона. Гадать нельзя.
		return points, []string{"reachability_guard: снимок до упаковки не совпал с треком, проверка пропущена"}, nil
	}

	out := make([]geo.Point, 0, len(points))
	out = append(out, points[0])
	restored := 0

	for k := 1; k < len(points); k++ {
		a, b := points[k-1], points[k]
		if !core.Reachable(a, b, nil) {
			// Возвращаем всё, что упаковка сняла между ними.
			lo, hi := pos[k-1]+1, pos[k]
			out = append(out, before[lo:hi]...)
			restored += hi - lo
		}
		out = append(out, b)
	}

	g.State.Guarded = restored
	if restored == 0 {
		return out, nil, nil
	}
	return out, []string{fmt.Sprintf(
		"reachability_guard: упаковка создала невозможные переходы, возвращено точек: %d", restored)}, nil
}

// embed — позиции точек `got` внутри `full` при условии, что `got` является её
// подпоследовательностью. Второе значение false, если это не так.
func embed(got, full []geo.Point) ([]int, bool) {
	pos := make([]int, len(got))
	k := 0
	for i, p := range got {
		for k < len(full) && full[k] != p {
			k++
		}
		if k == len(full) {
			return nil, false
		}
		pos[i] = k
		k++
	}
	return pos, true
}
