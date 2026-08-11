package core

import (
	"slices"
	"time"

	"ariadne/internal/geo"
)

// Два источника координат в одном треке.
//
// Найдено на `ab681145` (Саратов, 14–15.07): трек мечется между Сторожевкой и
// Саратовом 54 раза, прыжки по 20 км за секунды —
//
//	Сторожевка 13:16:28 → Саратов 13:16:58    20.6 км за 30 с = 2472 км/ч
//	Саратов    13:34:06 → Сторожевка 13:34:06 20.7 км за 0 с = 74520 км/ч
//
// Это не глюк отдельных точек, а ДВА ИСТОЧНИКА, пишущих в один трек. Прежние
// правила бессильны: сами прыжки запрещаются проверкой достижимости, но цепочка
// после этого просто СШИВАЕТ куски обоих потоков переходами, которые выглядят
// законно (20 км за 37 минут — нормальная езда). Каждый отдельный шаг
// безупречен, ложна вся конструкция. Пользователь подтвердил: в Сторожевке
// машины не было.
//
// Отличить поток нельзя ни по близости к дороге (оба на дорогах), ни по числу
// точек. Разделяет ПОКРЫТИЕ ВРЕМЕНИ: машина физически находится в одном месте,
// поэтому настоящий поток покрывает время эпизода сплошь, а подделка вспыхивает
// и гаснет. В ночном эпизоде Саратов держится 370 минут подряд и покрывает 72 %
// времени, Сторожевка — 57 минут и 14 %.
//
// Замер по 46 эпизодам двенадцати маршрутов: медиана разделения 6.9 раза, при
// пороге 2.0 уверенно решаются 82 % эпизодов. Остальные не трогаем — лучше
// пропустить, чем угадывать. На честных треках эпизодов нет вовсе.
const (
	// DualJumpKmh — выше этой скорости переход между точками невозможен.
	DualJumpKmh = 200.0
	// DualJumpMinM — и прыжок должен быть длинным: дрожание на месте не в счёт.
	DualJumpMinM = 3000.0
	// DualMinSwitch — столько метаний туда-обратно уже не случайность.
	DualMinSwitch = 6
	// DualWindow — окно, в котором прыжки считаются одним эпизодом.
	DualWindow = time.Hour
	// DualNearM — радиус принадлежности точки месту.
	DualNearM = 3000.0
	// DualMinRatio — во сколько раз покрытие лидера должно превышать второго.
	DualMinRatio = 2.0
	// DualMaxGap — разрыв больше этого означает, что место покинуто, и счёт
	// непрерывного присутствия прерывается.
	DualMaxGap = 5 * time.Minute
	// DualMinPlaces — столько соперничающих мест — это уже не «два потока».
	DualMinPlaces = 4
	// DualTrustFrac — если ни одно место не держится дольше этой доли эпизода,
	// достоверных данных за период нет вовсе.
	DualTrustFrac = 0.35
	// DualMinPoints — на коротком треке эпизодов не ищем.
	DualMinPoints = 20
)

// FindDual возвращает точки того из потоков, который покрывает время хуже.
func FindDual(pts []geo.Point) map[int]struct{} {
	bad := make(map[int]struct{})
	if len(pts) < DualMinPoints {
		return bad
	}

	jumps := findJumps(pts)
	if len(jumps) < DualMinSwitch {
		return bad
	}

	for _, ep := range groupEpisodes(pts, jumps) {
		judgeEpisode(pts, ep, bad)
	}
	return bad
}

// findJumps — переходы, невозможные физически: длинные и мгновенные.
func findJumps(pts []geo.Point) []int {
	var jumps []int
	for i := range len(pts) - 1 {
		d := geo.Haversine(pts[i], pts[i+1])
		if d < DualJumpMinM {
			continue
		}
		dt := pts[i+1].Time.Sub(pts[i].Time).Seconds()
		if dt < 0 {
			continue
		}
		// Прыжок за нулевое время — тоже прыжок, и самый показательный:
		// так выглядит выгрузка буфера, где обеим записям поставили одну метку.
		if d/max(dt, 1)*3.6 > DualJumpKmh {
			jumps = append(jumps, i)
		}
	}
	return jumps
}

// groupEpisodes собирает прыжки, идущие кучно по времени, в эпизоды.
func groupEpisodes(pts []geo.Point, jumps []int) [][]int {
	var episodes [][]int
	cur := []int{jumps[0]}
	for _, j := range jumps[1:] {
		if pts[j].Time.Sub(pts[cur[len(cur)-1]].Time) <= DualWindow {
			cur = append(cur, j)
			continue
		}
		if len(cur) >= DualMinSwitch {
			episodes = append(episodes, cur)
		}
		cur = []int{j}
	}
	if len(cur) >= DualMinSwitch {
		episodes = append(episodes, cur)
	}
	return episodes
}

// judgeEpisode разбирает один эпизод и помечает проигравший поток.
func judgeEpisode(pts []geo.Point, ep []int, bad map[int]struct{}) {
	a, b := pts[ep[0]], pts[ep[0]+1]
	if geo.Haversine(a, b) < DualJumpMinM {
		return
	}

	lo, hi := ep[0], ep[len(ep)-1]+1
	// Раздвигаем границы, пока точки принадлежат одному из мест: иначе часть
	// того же потока останется снаружи эпизода и уцелеет.
	for lo > 0 && (nearPoint(pts[lo-1], a) || nearPoint(pts[lo-1], b)) {
		lo--
	}
	for hi < len(pts)-1 && (nearPoint(pts[hi+1], a) || nearPoint(pts[hi+1], b)) {
		hi++
	}

	covA, idxA := coverage(pts, lo, hi, a)
	covB, idxB := coverage(pts, lo, hi, b)
	if len(idxA) == 0 || len(idxB) == 0 {
		return
	}

	hiCov, loCov := max(covA, covB), min(covA, covB)
	if hiCov/max(loCov, 1e-6) >= DualMinRatio {
		loser := idxB
		if covB >= covA {
			loser = idxA
		}
		for _, i := range loser {
			bad[i] = struct{}{}
		}
		return
	}

	// Разделения между двумя местами нет. Смотрим шире: сколько ВСЕГО мест в
	// эпизоде и держится ли хоть одно убедительно.
	//
	// В `ab681145` за 47 часов машина «была» в пяти местах вокруг Саратова
	// одновременно: север города, центр, Энгельс, Сторожевка и точка в 43 км
	// западнее. Покрытие лучшего 25 %, второго 15 %, и правило на два места
	// молчит. Но 86 % всего сигнала за эти двое суток лежит в саратовской
	// агломерации, а вне её — только транзит. Альтернативного места, где машина
	// могла стоять, в данных НЕТ.
	//
	// Если ни одно место не держится дольше своей доли эпизода, достоверных
	// данных за период нет вовсе, и показывать «лучшее из ложных» нельзя — это
	// выдуманный километраж. Выбрасываем период целиком: цепочка соединит
	// честные концы напрямую, а переход между ними законен, иначе эпизода бы
	// не было.
	judgeCrowd(pts, lo, hi, bad)
}

// judgeCrowd — случай, когда мест много и лидера нет.
func judgeCrowd(pts []geo.Point, lo, hi int, bad map[int]struct{}) {
	type cluster struct {
		at  geo.Point
		idx []int
	}
	var places []cluster

	for i := lo; i <= hi; i++ {
		found := false
		for k := range places {
			if nearPoint(pts[i], places[k].at) {
				places[k].idx = append(places[k].idx, i)
				found = true
				break
			}
		}
		if !found {
			places = append(places, cluster{at: pts[i], idx: []int{i}})
		}
	}
	if len(places) < DualMinPlaces {
		return
	}

	total := max(pts[hi].Time.Sub(pts[lo].Time).Seconds(), 1)
	best := 0.0
	for _, c := range places {
		ii := slices.Clone(c.idx)
		slices.Sort(ii)
		var held float64
		for k := range len(ii) - 1 {
			gap := pts[ii[k+1]].Time.Sub(pts[ii[k]].Time)
			if gap <= DualMaxGap {
				held += gap.Seconds()
			}
		}
		best = max(best, held/total)
	}

	if best < DualTrustFrac {
		for _, c := range places {
			for _, i := range c.idx {
				bad[i] = struct{}{}
			}
		}
	}
}

// coverage — какую долю времени эпизода место занимает непрерывным присутствием.
//
// Считаем именно непрерывные серии, а не число точек: подделка может сыпать
// часто, но короткими вспышками, и по числу точек выиграет у настоящей стоянки,
// которая пишет раз в семь минут.
func coverage(pts []geo.Point, lo, hi int, c geo.Point) (float64, []int) {
	var idx []int
	for i := lo; i <= hi && i < len(pts); i++ {
		if nearPoint(pts[i], c) {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return 0, nil
	}

	var span float64
	runStart := idx[0]
	for k := 1; k <= len(idx); k++ {
		if k < len(idx) && idx[k] == idx[k-1]+1 {
			continue
		}
		span += pts[idx[k-1]].Time.Sub(pts[runStart].Time).Seconds()
		if k < len(idx) {
			runStart = idx[k]
		}
	}

	total := max(pts[hi].Time.Sub(pts[lo].Time).Seconds(), 1)
	return span / total, idx
}

func nearPoint(p, c geo.Point) bool {
	return geo.Haversine(p, c) <= DualNearM
}
