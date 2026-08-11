package core

import (
	"slices"
	"time"

	"ariadne/internal/geo"
)

// Раздвоение: промежуток, в котором сигнал идёт сразу из нескольких мест.
//
// Обобщение правила двух потоков — без опоры на быстрые прыжки и без
// ограничения двумя местами. На `ab681145` двух мест мало: там с 14.07 13:16 и
// двадцать часов подряд ЧЕТЫРЕ источника пишут по очереди блоками ровно по
// четыре минуты (Заводской → Сторожевка → точка в 43 км западнее → Энгельс →
// по кругу). Внутри блока всё безупречно: снэпы 0–20 м, скорости 60–110 км/ч.
//
// Три находки, на которых правило держится:
//
//	УЛИКА — ВОЗВРАТ, а не переключение. За час фура проезжает сотню
//	километров, путь распадается на кластеры, и каждый новый выглядит как
//	переключение. Но едущая машина в покинутые места не возвращается.
//
//	МЕТИМ только места, между которыми возвраты и были: в том же окне может
//	лежать честный монотонный въезд в город, и метить всё окно нельзя.
//
//	СУДИМ по ДОЛЕ СЛОТОВ, а не по непрерывности присутствия. Настоящая
//	стоянка пишет РЕДКО — раз в семь минут за семнадцать часов, — тогда как
//	подделка сыплет раз в пять секунд. Любая мера непрерывности работает
//	ПРОТИВ настоящего сигнала.
const (
	SplitWindow    = time.Hour // окно, в котором ищем одновременность
	SplitNearM     = 3000.0    // радиус места
	SplitMinDistM  = 3000.0    // места обязаны быть разнесены: дрожание не в счёт
	SplitMinReturn = 4         // столько возвратов за окно — уже не переезд
	SplitMinPts    = 3         // место с меньшим числом точек за источник не считаем
	SplitMinRatio  = 2.0       // во сколько раз доля слотов лидера больше второго
	SplitSlot      = 15 * time.Minute
	SplitMinSlots  = 8 // промежуток короче — мерить долю слотов не на чем
	SplitGrowPass  = 4 // проходов расширения границ промежутка
	SplitSkipPts   = 3 // через столько чужих точек подряд границу ещё перешагиваем

	// SplitMaxSpanM — размах больше городской агломерации означает переезд:
	// у едущей машины точки тоже рассыпаются по «местам».
	//
	// Порог поднят с 60 до 150 км по замеру. Шестьдесят отсекали настоящие
	// случаи: в Астрахани спуфинг раскидывает 18 тысяч точек по 50 местам на
	// 127 км дельты Волги, под Волово — на 200 км вдоль М-4, и оба промежутка
	// правило пропускало как «переезд». От честного переезда их отличает не
	// размах, а возвраты: за час фура в покинутое место не возвращается, а тут
	// возвращается десятки раз.
	SplitMaxSpanM = 150000.0

	SplitMinPoints = 20 // на коротком треке промежутков не ищем
)

// splitCluster — место и точки, к нему отнесённые.
type splitCluster struct {
	at    geo.Point
	idx   []int
	share float64
}

// FindSplit возвращает точки промежутков, где трек раздвоен и лидера нет.
func FindSplit(pts []geo.Point) map[int]struct{} {
	bad := make(map[int]struct{})
	if len(pts) < SplitMinPoints {
		return bad
	}

	sus := suspectWindows(pts)
	if len(sus) == 0 {
		return bad
	}

	for _, ep := range mergeEpisodes(pts, growEpisodes(pts, sus)) {
		judgeSplit(pts, ep, bad)
	}
	return bad
}

// suspectWindows — скользящее окно, в котором ищем возвраты между местами.
func suspectWindows(pts []geo.Point) []int {
	n := len(pts)
	sus := make(map[int]struct{})

	step := SplitWindow / 2
	t := pts[0].Time
	end := pts[n-1].Time
	i := 0

	for !t.After(end) {
		for i < n && pts[i].Time.Before(t) {
			i++
		}
		j := i
		for j < n && !pts[j].Time.After(t.Add(SplitWindow)) {
			j++
		}
		t = t.Add(step)

		if j-i < 2*SplitMinPts {
			continue
		}
		idx := make([]int, 0, j-i)
		for k := i; k < j; k++ {
			idx = append(idx, k)
		}

		cs := filterBySize(splitClusters(pts, idx, SplitNearM), SplitMinPts)
		if len(cs) < 2 {
			continue
		}
		cnt, guilty := splitReturns(pts, idx, cs, SplitMinDistM)
		if cnt < SplitMinReturn {
			continue
		}
		for k := range guilty {
			for _, p := range cs[k].idx {
				sus[p] = struct{}{}
			}
		}
	}

	out := make([]int, 0, len(sus))
	for i := range sus {
		out = append(out, i)
	}
	slices.Sort(out)
	return out
}

// episode — промежуток и места, замешанные в метании внутри него.
type episode struct {
	lo, hi int
	seed   []geo.Point
}

// growEpisodes собирает подозрительные точки в промежутки и раздвигает границы.
func growEpisodes(pts []geo.Point, sus []int) []episode {
	// Разрыв в индексах промежуток не режет: между вспышками лежат точки
	// других мест, они попадут внутрь при расширении границ.
	var groups [][]int
	cur := []int{sus[0]}
	for _, k := range sus[1:] {
		if pts[k].Time.Sub(pts[cur[len(cur)-1]].Time) <= SplitWindow {
			cur = append(cur, k)
			continue
		}
		groups = append(groups, cur)
		cur = []int{k}
	}
	groups = append(groups, cur)

	n := len(pts)
	var eps []episode
	for _, g := range groups {
		seed := filterBySize(splitClusters(pts, g, SplitNearM), SplitMinPts)
		if len(seed) < 2 {
			continue
		}

		// Место, уличённое в раздвоении, не становится честным оттого, что
		// соперник помолчал полчаса: промежуток растягивается, пока точки
		// принадлежат его местам. Отъезд по трассе быстро выходит за радиус и
		// границу не сдвигает.
		lo, hi := g[0], g[len(g)-1]
		belongs := func(k int) bool {
			for _, c := range seed {
				if geo.Haversine(c.at, pts[k]) <= SplitNearM {
					return true
				}
			}
			return false
		}
		for lo > 0 && belongs(lo-1) {
			lo--
		}
		for hi < n-1 && belongs(hi+1) {
			hi++
		}

		centres := make([]geo.Point, len(seed))
		for i, c := range seed {
			centres[i] = c.at
		}
		eps = append(eps, episode{lo: lo, hi: hi, seed: centres})
	}
	return eps
}

// mergeEpisodes сливает пересекающиеся промежутки.
func mergeEpisodes(_ []geo.Point, eps []episode) []episode {
	if len(eps) == 0 {
		return nil
	}
	slices.SortFunc(eps, func(a, b episode) int { return a.lo - b.lo })

	merged := []episode{eps[0]}
	for _, e := range eps[1:] {
		last := &merged[len(merged)-1]
		if e.lo <= last.hi+1 {
			last.hi = max(last.hi, e.hi)
			last.seed = append(last.seed, e.seed...)
			continue
		}
		merged = append(merged, e)
	}
	return merged
}

// judgeSplit выносит приговор промежутку.
func judgeSplit(pts []geo.Point, ep episode, bad map[int]struct{}) {
	n := len(pts)
	lo, hi := ep.lo, ep.hi
	t0, t1 := pts[lo].Time, pts[hi].Time

	// На коротком промежутке слотов слишком мало: у всех мест доля близка к
	// единице, и «лидера не видно» означает не подделку, а нехватку данных.
	if t1.Sub(t0) < time.Duration(SplitMinSlots)*SplitSlot {
		return
	}

	places := splitClusters(pts, rangeIdx(lo, hi), SplitNearM)
	if len(places) < 2 {
		return
	}

	// Размах мерим по местам, замешанным В МЕТАНИИ, а не по всему промежутку:
	// после растяжки в него входят и подъезды, они одни разносят размах на
	// сотню километров.
	if spread(ep.seed) > SplitMaxSpanM {
		return
	}

	var guilty []splitCluster
	for range SplitGrowPass {
		for i := range places {
			places[i].share = splitShare(pts, places[i].idx, t0, t1)
		}
		slices.SortFunc(places, func(a, b splitCluster) int {
			switch {
			case a.share > b.share:
				return -1
			case a.share < b.share:
				return 1
			default:
				return 0
			}
		})

		if places[0].share >= SplitMinRatio*max(places[1].share, 1e-9) {
			guilty = places[1:]
			break
		}
		guilty = places

		// Промежуток признан ложным целиком — тянем границы дальше, но ТОЛЬКО
		// по ложным местам. Тянуть по всем нельзя: тогда растёт и место-
		// победитель, и вместе с подделкой в промежуток попадают честные заезды
		// (проверено: рабочая зона теряла все 64 точки из 64). А по ложным
		// местам растяжка безопасна и нужна: иначе на `97bd880c` за границей
		// остаётся вся Астрахань — час до промежутка и пять часов после.
		centres := make([]geo.Point, len(guilty))
		for i, c := range guilty {
			centres[i] = c.at
		}
		belongsBad := func(k int) bool {
			for _, c := range centres {
				if geo.Haversine(c, pts[k]) <= SplitNearM {
					return true
				}
			}
			return false
		}

		// Через одиночные чужие точки перешагиваем: граница, обрывающаяся на
		// первой же несовпавшей записи, оставляла снаружи весь хвост трека.
		beforeLo, beforeHi := lo, hi
		for lo > 0 {
			j := lo - 1
			for j > 0 && !belongsBad(j) && lo-j <= SplitSkipPts {
				j--
			}
			if !belongsBad(j) {
				break
			}
			lo = j
		}
		for hi < n-1 {
			j := hi + 1
			for j < n-1 && !belongsBad(j) && j-hi <= SplitSkipPts {
				j++
			}
			if !belongsBad(j) {
				break
			}
			hi = j
		}
		if lo == beforeLo && hi == beforeHi {
			break
		}

		t0, t1 = pts[lo].Time, pts[hi].Time
		places = splitClusters(pts, rangeIdx(lo, hi), SplitNearM)
		if len(places) < 2 {
			break
		}
	}

	for _, c := range guilty {
		for _, i := range c.idx {
			bad[i] = struct{}{}
		}
	}
}

// splitClusters группирует точки по местам: точка идёт в БЛИЖАЙШИЙ подходящий
// центр, а не в первый попавшийся — иначе разбиение зависело бы от порядка.
func splitClusters(pts []geo.Point, idx []int, near float64) []splitCluster {
	var out []splitCluster
	for _, i := range idx {
		best, bestD := -1, 1e18
		for k := range out {
			d := geo.Haversine(out[k].at, pts[i])
			if d <= near && d < bestD {
				best, bestD = k, d
			}
		}
		if best < 0 {
			out = append(out, splitCluster{at: pts[i], idx: []int{i}})
			continue
		}
		out[best].idx = append(out[best].idx, i)
	}
	return out
}

// splitReturns — сколько было ВОЗВРАТОВ и какие места в них замешаны.
//
// Считать простые переключения нельзя: за час фура проезжает сотню километров,
// путь распадается на кластеры, и каждый новый выглядит как переключение. Но
// едущая машина в покинутые места не возвращается — она идёт вперёд.
func splitReturns(pts []geo.Point, idx []int, places []splitCluster, minDist float64) (int, map[int]struct{}) {
	where := make(map[int]int, len(idx))
	for k := range places {
		for _, i := range places[k].idx {
			where[i] = k
		}
	}

	seen := make(map[int]struct{}, len(places))
	guilty := make(map[int]struct{})
	prev, cnt := -1, 0

	for _, i := range idx {
		k, ok := where[i]
		if !ok {
			continue
		}
		if _, been := seen[k]; prev >= 0 && k != prev && been {
			if geo.Haversine(places[prev].at, places[k].at) >= minDist {
				cnt++
				guilty[k] = struct{}{}
				guilty[prev] = struct{}{}
			}
		}
		seen[k] = struct{}{}
		prev = k
	}
	return cnt, guilty
}

// splitShare — доля слотов промежутка, в которых место подало голос.
//
// Долю времени под непрерывным присутствием брать нельзя: настоящая стоянка
// пишет РЕДКО. На `5cde6306` машина стояла на Пензенской 17 часов и отметилась
// 144 раза — точка раз в семь минут, тогда как подделка в это же время сыпала
// точку раз в пять секунд. Доля слотов от частоты записи не зависит вовсе.
func splitShare(pts []geo.Point, idx []int, t0, t1 time.Time) float64 {
	n := int(t1.Sub(t0)/SplitSlot) + 1
	if n < 1 {
		n = 1
	}
	seen := make(map[int]struct{}, n)
	for _, i := range idx {
		s := int(pts[i].Time.Sub(t0) / SplitSlot)
		if s >= 0 && s < n {
			seen[s] = struct{}{}
		}
	}
	return float64(len(seen)) / float64(n)
}

func filterBySize(cs []splitCluster, minPts int) []splitCluster {
	out := cs[:0]
	for _, c := range cs {
		if len(c.idx) >= minPts {
			out = append(out, c)
		}
	}
	return out
}

func rangeIdx(lo, hi int) []int {
	out := make([]int, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}

// spread — наибольшее расстояние между местами.
func spread(centres []geo.Point) float64 {
	worst := 0.0
	for i := range centres {
		for j := i + 1; j < len(centres); j++ {
			worst = max(worst, geo.Haversine(centres[i], centres[j]))
		}
	}
	return worst
}
