package core

import (
	"context"
	"slices"
	"time"

	"ariadne/internal/geo"
)

// Проверка переходов выбранной цепочки по дорожной сети.
//
// Проверять дорогой все пары внутри выбора цепочки нельзя — их миллионы. Но
// переходов в готовой цепочке единицы десятков, и запрос на каждый дёшев.
// Отсюда весь порядок работы: цепочка строится по прямой, её переходы
// проверяются по дорогам, невозможные запрещаются, цепочка строится заново.
// Обычно хватает двух-трёх проходов.

const (
	// RoadCheckM — переходы короче не проверяем: там дорога почти равна прямой,
	// а ошибка привязки к дороге перевешивает всё остальное.
	RoadCheckM = 500.0

	// Ловушка разделённых трасс: две точки на встречных сторонах в 615 метрах
	// друг от друга дают по дорогам 19.5 км — маршрутизатор строит разворот
	// через развязку. На коротком переходе, проходимом по прямой, дорожному
	// ответу верить нельзя.
	//
	// Замер по шести маршрутам: из 35 переходов, которые проверка звала
	// непроходимыми, 11 были такими артефактами.
	SnapTrapM   = 2000.0
	SnapTrapKmh = 110.0

	// RoadVmaxKmh — предел скорости ПО ДОРОГАМ.
	//
	// Отдельный от предела цепочки и заметно ниже: там мгновенная скорость
	// между соседними точками, здесь — средняя на перегоне в десятки
	// километров, где есть светофоры, развязки и населённые пункты. Фуре по
	// закону разрешено 90 км/ч на трассе; средняя выше сотни недостижима.
	//
	// При прежних 126 км/ч под порогом пролезали переходы вида «Электроугли →
	// Сокольники, 45 км по дорогам за 22 минуты» — их пользователь и видел на
	// карте как прямые через поля.
	RoadVmaxKmh = 100.0

	// RoadPenaltyStep — штраф точке за участие в непроходимом переходе.
	// Копится по проходам, пока вес не уйдёт в минус.
	RoadPenaltyStep = 0.6

	// Метки мгновенных скачков сырого трека: вокруг них смотрим внимательнее.
	TeleMinM    = 20000.0
	TeleMaxGap  = 5 * time.Minute
	TeleNearGap = time.Hour
)

// RoadStepsHot — на сколько шагов расширять проверку вокруг найденного дефекта.
//
// Соседние переходы ловят одиночный прыжок, разнесённые — накопленный обход.
// Запрещать саму пару оказалось мало: цепочка находит обход через соседнюю
// точку того же облака, и так по кругу — на одном треке набралось 386 запретов,
// а дефекты остались.
var RoadStepsHot = [...]int{2, 5, 12, 30}

// RoadClient — то, что умеет отвечать про расстояния по дорогам.
// Интерфейс, а не конкретный клиент: так в тестах подставляется запись готовых
// ответов, и проверка работает офлайн и детерминированно.
type RoadClient interface {
	PairDistance(ctx context.Context, pairs []Pair) ([]float64, []bool, []string)
}

// Pair — пара точек, между которыми нужно расстояние по дорогам.
type Pair struct {
	A, B geo.Point
}

// askedID — пара меток времени: чем опознаётся уже заданный вопрос.
//
// Важно, что ключ здесь по ВРЕМЕНИ, а не по клетке, как у запретов. Разница
// принципиальна: клетка в пятьдесят метров накрывает несколько точек трека, и
// дедупликация по ней съела бы вопросы про разные переходы, попавшие в одну
// клетку. Прототип спрашивает их все, а совпавшие ответы потом схлопываются в
// один запрет по максимуму требуемого времени.
type askedID struct {
	from, to int64
}

// RoadState — что накопилось за проходы: запреты, штрафы и уже спрошенное.
type RoadState struct {
	// Banned — минимальное время, за которое переход проходим по дорогам.
	// Ключ по МЕСТУ: точка, повторённая трекером, не должна плодить запреты.
	Banned map[BanID]float64

	// Penalty — накопленный штраф точке за участие в непроходимых переходах.
	Penalty map[int]float64

	// asked — какие вопросы уже задавали. Ключ по времени, см. askedID.
	asked map[askedID]struct{}

	// loops — какие окна уже разобрало правило петель.
	//
	// Отдельно от `asked`, хотя ключ той же формы. В прототипе это один
	// словарь, но с разной формой ключа: у переходов пара меток времени, у
	// петель та же пара с пометкой. Свалить их в одну карту значит молча
	// потерять часть вопросов — концы окна и переход цепочки нередко
	// совпадают.
	loops map[askedID]struct{}
}

func NewRoadState() *RoadState {
	return &RoadState{
		Banned:  make(map[BanID]float64),
		Penalty: make(map[int]float64),
		asked:   make(map[askedID]struct{}),
		loops:   make(map[askedID]struct{}),
	}
}

func askKey(a, b geo.Point) askedID {
	return askedID{from: a.Time.UnixNano(), to: b.Time.UnixNano()}
}

// CheckByRoad проверяет переходы цепочки по дорогам и копит запреты.
// Возвращает число новых запретов: ноль означает, что цикл сошёлся.
func CheckByRoad(ctx context.Context, road RoadClient, pts []geo.Point, chain []int, st *RoadState) int {
	if road == nil || len(chain) < 2 || ctx.Err() != nil {
		return 0
	}

	pairs, ends := collectPairs(pts, chain, st)
	if len(pairs) == 0 {
		return 0
	}

	dist, ok, _ := road.PairDistance(ctx, pairs)

	added := 0
	for k := range pairs {
		i, j := ends[k][0], ends[k][1]
		st.asked[askKey(pts[i], pts[j])] = struct{}{}
		if !ok[k] {
			continue // проезда нет вовсе — судить не берёмся
		}

		dt := pts[j].Time.Sub(pts[i].Time).Seconds()
		if dt > 0 && dist[k]/dt*3.6 <= RoadVmaxKmh {
			continue
		}

		// Сколько секунд нужно, чтобы проехать это по дорогам законно.
		need := dist[k] / (RoadVmaxKmh / 3.6)
		key := BanKey(pts[i], pts[j])
		if cur, exists := st.Banned[key]; !exists || need > cur {
			st.Banned[key] = need
		}
		// Штрафуем обе точки: если точка ложная, переходы к ней не сойдутся
		// ни от кого, штраф накопится, вес уйдёт в минус, и цепочка обойдёт
		// её стороной.
		st.Penalty[i] += RoadPenaltyStep
		st.Penalty[j] += RoadPenaltyStep
		added++
	}
	return added
}

// collectPairs — какие переходы имеет смысл спросить.
func collectPairs(pts []geo.Point, chain []int, st *RoadState) ([]Pair, [][2]int) {
	var pairs []Pair
	var ends [][2]int
	seen := make(map[askedID]struct{}, len(chain))

	consider := func(i, j int) {
		if i >= j {
			return
		}
		a, b := pts[i], pts[j]
		// Уже запрещённое не переспрашиваем — тут ключ по месту.
		if _, banned := st.Banned[BanKey(a, b)]; banned {
			return
		}
		// А уже спрошенное — по времени: см. askedID.
		key := askKey(a, b)
		if _, done := st.asked[key]; done {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}

		d := geo.Haversine(a, b)
		if d < RoadCheckM {
			return
		}
		// Ловушка разделённых трасс — см. SnapTrapM.
		dt := b.Time.Sub(a.Time).Seconds()
		if d < SnapTrapM && dt > 0 && d/dt*3.6 <= SnapTrapKmh {
			return
		}

		seen[key] = struct{}{}
		pairs = append(pairs, Pair{A: a, B: b})
		ends = append(ends, [2]int{i, j})
	}

	// Соседние переходы цепочки — всегда.
	for k := range len(chain) - 1 {
		consider(chain[k], chain[k+1])
	}

	// Вокруг горячих мест — ещё и разнесённые пары. Проверять разнесённые по
	// всей цепочке нельзя: на треке в три тысячи точек это давало 4418
	// запросов и семь минут.
	//
	// Обходим горячие места ПО ПОРЯДКУ, а не как придётся. Разные места могут
	// породить вопросы с одинаковым ключом, и тогда побеждает первый — значит
	// от порядка зависит, какую именно пару мы спросим. Обход карты в Go
	// случаен, и без сортировки километраж гулял бы от прогона к прогону.
	hot := hotSpots(pts, chain, st)
	order := make([]int, 0, len(hot))
	for k := range hot {
		order = append(order, k)
	}
	slices.Sort(order)

	for _, k := range order {
		for _, step := range RoadStepsHot {
			lo := max(0, k-step)
			hi := min(len(chain)-1, k+step)
			consider(chain[lo], chain[hi])
		}
	}
	return pairs, ends
}

// hotSpots — позиции цепочки, вокруг которых стоит смотреть внимательнее:
// там, где уже нашлись дефекты, и рядом с мгновенными скачками сырого трека.
func hotSpots(pts []geo.Point, chain []int, st *RoadState) map[int]struct{} {
	hot := make(map[int]struct{})
	for k, idx := range chain {
		if st.Penalty[idx] > 0 {
			hot[k] = struct{}{}
		}
	}

	// Метки скачков. Сам скачок цепочка обходит — она добирается до облака по
	// его же точкам, каждый шаг короткий и законный. Но время, потраченное на
	// обход, остаётся тем же, что было у скачка, и на разнесённых парах это
	// вскрывается.
	var marks []time.Time
	for i := range len(pts) - 1 {
		if pts[i+1].Time.Sub(pts[i].Time) > TeleMaxGap {
			continue
		}
		if geo.Haversine(pts[i], pts[i+1]) >= TeleMinM {
			marks = append(marks, pts[i].Time, pts[i+1].Time)
		}
	}
	if len(marks) == 0 {
		return hot
	}
	slices.SortFunc(marks, func(a, b time.Time) int { return a.Compare(b) })

	for k, idx := range chain {
		t := pts[idx].Time
		lo := t.Add(-TeleNearGap)
		j, _ := slices.BinarySearchFunc(marks, lo, func(m, target time.Time) int {
			return m.Compare(target)
		})
		if j < len(marks) && !marks[j].After(t.Add(TeleNearGap)) {
			hot[k] = struct{}{}
		}
	}
	return hot
}
