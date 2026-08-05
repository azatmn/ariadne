package core

import (
	"slices"
	"time"

	"ariadne/internal/geo"
)

// ReorderMax — выше этого размера пачку не трогаем.
//
// Такие пачки — не выгрузка буфера, а свалка: в одном треке до шестнадцати
// точек на секунду с разлётом 1.6 км, то есть два потока, и порядок им не
// поможет. Жадный перебор там к тому же уже ненадёжен.
const ReorderMax = 12

// ReorderBatches восстанавливает порядок внутри пачек с одинаковой меткой
// времени. Возвращает индексы исходного среза в новом порядке.
//
// Трекер копит записи без связи и выгружает пачкой, ставя всем одно время
// отправки — и нередко в ОБРАТНОМ порядке. Найдено на `573f42bf` (07.07,
// Ставрополь): трек идёт на восток, а внутри каждой пачки курс развёрнут на
// запад.
//
//	15257  41.97760  курс 102   ← пачка началась
//	15258  41.97633  курс 272   ← назад
//	15259  41.97505  курс 278
//	15262  41.98150  курс  98   ← новая пачка, снова вперёд
//
// Путь по точкам 6.80 км при прямой 2.78 км — крюк вдвое с лишним из чистой
// перестановки, при снэпах 1–8 м. Координаты верные, сломан только порядок.
//
// У точек с одинаковой меткой порядка НЕ СУЩЕСТВУЕТ: метка одна на всю пачку.
// Значит порядок можно выбрать, и выбрать надо тот, что даёт минимальный путь.
// Это не подгонка результата, а восстановление того, что трекер потерял.
//
// Замер: лишнего 3.4 % километража сырого трека, на грязных до 7.0 %,
// на честном треке — ровно ноль.
func ReorderBatches(pts []geo.Point) []int {
	seq := make([]int, len(pts))
	for i := range seq {
		seq[i] = i
	}
	if len(pts) < 3 {
		return seq
	}

	// Группируем по метке времени. Точки пачки идут подряд, но полагаться на
	// это нельзя: сортировка по времени устойчива, а метки могут повторяться
	// и в разных местах трека.
	byTime := make(map[time.Time][]int, len(pts))
	for i, p := range pts {
		byTime[p.Time] = append(byTime[p.Time], i)
	}

	for _, batch := range byTime {
		if len(batch) < 2 || len(batch) > ReorderMax {
			continue
		}
		best := bestOrder(pts, batch)
		// Раскладываем выбранный порядок по тем же позициям: точки пачки
		// меняются местами между собой, но пачка не выходит за свои границы.
		for k, slot := range batch {
			seq[slot] = best[k]
		}
	}
	return seq
}

// bestOrder выбирает из трёх кандидатов тот, что даёт кратчайший путь.
//
// Кандидаты: исходный порядок, жадный обход от предыдущей точки трека и
// жадный обход от следующей, развёрнутый. Полный перебор не нужен — пачки
// короткие, а жадный обход с обоих концов покрывает и прямой ход, и обратный,
// ради которого всё и затевалось.
func bestOrder(pts []geo.Point, batch []int) []int {
	lo, hi := batch[0], batch[len(batch)-1]
	prev, next := -1, -1
	if lo > 0 {
		prev = lo - 1
	}
	if hi+1 < len(pts) {
		next = hi + 1
	}

	candidates := [][]int{
		slices.Clone(batch),
		greedyFrom(pts, batch, prev),
	}
	if back := greedyFrom(pts, batch, next); back != nil {
		slices.Reverse(back)
		candidates = append(candidates, back)
	}

	best, bestLen := candidates[0], batchPath(pts, candidates[0], prev, next)
	for _, c := range candidates[1:] {
		// Строгое сравнение: при равных путях остаётся первый кандидат, то есть
		// исходный порядок. Иначе выбор зависел бы от порядка перебора, и на
		// залипшей пачке, где все пути нулевые, результат гулял бы от прогона
		// к прогону.
		if l := batchPath(pts, c, prev, next); l < bestLen {
			best, bestLen = c, l
		}
	}
	return best
}

// greedyFrom — обход пачки, каждый раз выбирая ближайшую из оставшихся точек.
// start = -1 означает, что отталкиваться не от чего: начинаем с первой точки.
func greedyFrom(pts []geo.Point, batch []int, start int) []int {
	left := slices.Clone(batch)
	out := make([]int, 0, len(batch))
	cur := start

	for len(left) > 0 {
		pick := 0
		if cur >= 0 {
			bestD := geo.Haversine(pts[cur], pts[left[0]])
			for k := 1; k < len(left); k++ {
				if d := geo.Haversine(pts[cur], pts[left[k]]); d < bestD {
					pick, bestD = k, d
				}
			}
		}
		cur = left[pick]
		out = append(out, cur)
		left = slices.Delete(left, pick, pick+1)
	}
	return out
}

// batchPath — длина пути через пачку вместе с соседями по краям.
// Соседи обязательны: без них порядок внутри пачки не с чем согласовывать.
func batchPath(pts []geo.Point, order []int, prev, next int) float64 {
	seq := make([]geo.Point, 0, len(order)+2)
	if prev >= 0 {
		seq = append(seq, pts[prev])
	}
	for _, i := range order {
		seq = append(seq, pts[i])
	}
	if next >= 0 {
		seq = append(seq, pts[next])
	}
	return geo.TotalLength(seq)
}
