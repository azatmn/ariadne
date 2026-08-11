package core

import (
	"time"

	"ariadne/internal/geo"
)

// Правила поиска подделки. Каждое возвращает МНОЖЕСТВО подозрительных точек, а
// не решение об их удалении: удаляет один раз выбор цепочки, взвесив все улики
// сразу. Правило, само выбрасывающее точки, ошибается необратимо — именно так
// работают локальные фильтры, где одна плохая опора убивает всё за ней.

// --- ловушки: едет быстро, но никуда не уезжает --------------------------

const (
	// TrapWindow — окно, на котором ищем несоответствие.
	// Поточечно этот обман не виден вовсе: каждый шаг правдоподобен, снэп в
	// норме, физика между соседями не нарушена. Видно только на интервале.
	TrapWindow = 10 * time.Minute

	// TrapMinKmh — ниже этой скорости окно не разбираем: там стоянка или
	// маневрирование на погрузке, и топтание на месте законно.
	TrapMinKmh = 50.0

	// TrapSpanFrac — во сколько раз размах пятна должен отставать от пути,
	// который машина обязана была бы пройти на такой скорости.
	TrapSpanFrac = 0.25

	// TrapMinPoints — короткие окна не разбираем: на трёх точках медиана
	// скорости ничего не значит.
	TrapMinPoints = 4
)

// FindTraps ищет точки, которые «едут быстро, но никуда не уезжают».
//
// Главная улика самосогласованного спуфинга, найденная на `bd6a0ad0`:
// в Шереметьеве координата шла шагами ровно по 193 м каждые ровно 7 секунд —
// то есть 99 км/ч — и за час пятьдесят намотала 4.9 км, не выйдя из квадрата
// в 800 метров. Живая фура на такой скорости уехала бы за 180 километров.
//
// От стоянки отличается скоростью: у стоянки она около нуля, здесь под сотню.
func FindTraps(pts []geo.Point) map[int]struct{} {
	bad := make(map[int]struct{})
	n := len(pts)

	speeds := make([]float64, 0, 64)
	for i := 0; i < n-3; {
		// окно фиксированной длительности
		j := i
		for j+1 < n && pts[j+1].Time.Sub(pts[i].Time) <= TrapWindow {
			j++
		}

		if j-i >= TrapMinPoints {
			speeds = speeds[:0]
			for k := i + 1; k <= j; k++ {
				dt := pts[k].Time.Sub(pts[k-1].Time).Seconds()
				if dt > 0 {
					speeds = append(speeds, geo.Haversine(pts[k-1], pts[k])/dt*3.6)
				}
			}
			if len(speeds) > 0 {
				v := medianInPlace(speeds)
				span := spanMeters(pts, StopRange{Start: i, End: j})
				// Сколько машина обязана была бы проехать на такой скорости.
				expect := v / 3.6 * pts[j].Time.Sub(pts[i].Time).Seconds()

				if v > TrapMinKmh && span < TrapSpanFrac*expect {
					for k := i; k <= j; k++ {
						bad[k] = struct{}{}
					}
				}
			}
		}

		if j > i {
			i = j
		} else {
			i++
		}
	}
	return bad
}

// --- острова: огрызки, оторванные разрывами с обеих сторон ---------------

const (
	// IslandGapM — разрыв, по которому режем трек на куски.
	IslandGapM = 5000.0

	// Огрызком считаем кусок, малый сразу по ТРЁМ меркам. Настоящий заезд
	// куда-либо оставляет следы хотя бы по одной из них: либо точек много,
	// либо времени прошло, либо внутри ездили.
	IslandMinPoints = 6
	IslandMinStay   = 3 * time.Minute
	IslandMinPathM  = 800.0
)

// FindIslands ищет огрызки — несколько точек, оторванных от трека разрывами.
//
// Пример из `5f5dd0f1`: трек заканчивается ОДНОЙ точкой в Москве, оторванной
// на 29 км от предыдущей. Переход физически возможен (43 км/ч за 40 минут),
// точка лежит на дороге — ни физика, ни снэп её не берут. Но машина не может
// «доехать» и на этом кончиться: внутри такого куска нет никакой езды.
//
// В литературе это называют сиротскими островами: огрызки подделки в несколько
// точек, зажатые между разрывами трека, которые не берёт ни одна локальная мера.
func FindIslands(pts []geo.Point) map[int]struct{} {
	bad := make(map[int]struct{})
	n := len(pts)
	if n < 3 {
		return bad
	}

	// Режем по большим прыжкам.
	cuts := []int{0}
	for i := range n - 1 {
		if geo.Haversine(pts[i], pts[i+1]) > IslandGapM {
			cuts = append(cuts, i+1)
		}
	}
	cuts = append(cuts, n)

	for k := range len(cuts) - 1 {
		lo, hi := cuts[k], cuts[k+1]
		seg := pts[lo:hi]
		if len(seg) >= IslandMinPoints {
			continue
		}
		if seg[len(seg)-1].Time.Sub(seg[0].Time) >= IslandMinStay {
			continue
		}
		if geo.TotalLength(seg) >= IslandMinPathM {
			continue
		}
		for i := lo; i < hi; i++ {
			bad[i] = struct{}{}
		}
	}
	return bad
}
