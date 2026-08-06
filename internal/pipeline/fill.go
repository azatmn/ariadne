package pipeline

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ariadne/internal/geo"
	"ariadne/internal/osrm"
)

// Дорисовка дыр по дорогам — единственная стадия, которая ДОБАВЛЯЕТ точки.
//
// Между двумя честными точками трек сейчас идёт прямой, а машина ехала по
// дороге. Прямая — заведомо нижняя граница пробега: по прямой не ездят.
// Замер по 12 маршрутам: через дыры проходит 27 % километража, и дорисовка
// добавляет к нему +5.4 %.
//
// Ставится ПОСЛЕДНЕЙ и работает только по тому, что уже признано настоящим:
// рисовать дорогу через выброшенный спуфинг значило бы узаконить его.
//
// Завысить дорисовка может ТОЛЬКО крюком (путь по дорогам / прямая), больше
// ей нечем — ни один маршрут короче кратчайшего не существует. Поэтому и
// предохранители стоят на крюке, а время работает лишь в средней полосе.

const (
	// GapMinM — переход короче не считаем дырой.
	//
	// Критерий по РАССТОЯНИЮ, а не по молчанию. Первая версия считала дырой
	// молчание дольше двух минут, и пользователь показал на карте прямые через
	// поля: трекер писал раз в минуту, машина шла 90 км/ч, между точками
	// полтора километра, дорога петляла — а дырой это не считалось.
	//
	// 500 м — компромисс: 200 м дают ещё +5 км ценой втрое большего числа
	// запросов к маршрутизатору.
	GapMinM = 500.0

	// GapMinSec — нижняя отсечка от дрожания стоящей машины: подряд идущие
	// точки в полукилометре за пару секунд — это выброс, а не переезд.
	GapMinSec = 10 * time.Second

	// FillVmaxKmh — предел скорости для проверки «успела ли машина».
	//
	// Порог измерен кросс-валидацией на 12 289 участках с известным настоящим
	// путём. Отклонение итогового километража от настоящего: ничего не
	// дорисовывать −3.58 %, дорисовывать всё без проверок +55.27 %, только
	// физика 110 км/ч +0.57 %, физика 90 км/ч +0.03 %. Порог проверен на
	// устойчивость по половинам выборки (+0.23 % и −0.07 %) и физически
	// обоснован: для грузовиков в России 90 км/ч и есть предел.
	FillVmaxKmh = 90.0

	// FillSlackM — допуск на погрешность, как в ядре.
	//
	// Без него порог скорости бьёт по своим: полкилометра за двадцать секунд
	// — это уже 90 км/ч, и дорога на сотню метров длиннее прямой объявляется
	// невозможной. На `749dc894` так отклонялось 157 дыр из 594. Погрешность
	// координаты и округление времени дают эту сотню метров сами по себе,
	// поэтому допуск постоянный, а не долевой.
	FillSlackM = 300.0

	// FreeDetour — ниже этого крюка не проверяем ничего.
	//
	// Дорога почти повторяет прямую, добавка не больше 30 %, ошибиться не на
	// чем. Без этого порога проверка временем била по честной трассе: фура
	// идёт 92–108 км/ч по GPS, и дорисовка отказывала там, где дорога длиннее
	// прямой на 2–7 %. На карте это были прямые в 37.8 км у Венёва, 24.8 и
	// 23.1 км вдоль М4.
	FreeDetour = 1.3

	// MaxDetour — выше этого крюка отклоняем всегда.
	//
	// Закрывает дыру, которую проверка временем не видит по построению: на
	// долгом молчании 44 км по дорогам за 31 минуту — законные 84 км/ч, хотя
	// точки стоят в полутора километрах и крюк 29.6. Таких 17 на 20 маршрутах,
	// 155 км выдуманного пути.
	//
	// Порог измерен на двух независимых мерах (эталон кросс-валидации и живые
	// дыры): ×2.5 и ×3 закрывают абсурд полностью, ×4 пропускает 7 случаев,
	// ×2 начинает резать законные объезды (99-й перцентиль ошибки 18 %).
	// Берём мягчайший из закрывающих.
	MaxDetour = 3.0

	// FillSimplifyM — упрощение дорисованной геометрии.
	//
	// Маршрутизатор отдаёт путь до каждой вершины дороги и раздувает трек
	// втрое. Упрощаем ТОЛЬКО дорисованное, наблюдения защищаем: они и есть
	// смысл трека. Замер: убирает половину точек, теряя 0.005 % километража.
	FillSimplifyM = 5.0

	// fillWorkers — сколько дыр спрашиваем одновременно.
	//
	// Клиент OSRM держит собственный семафор, поэтому здесь ограничение не
	// про нагрузку на сервер, а про число живых горутин на задачу.
	fillWorkers = 8
)

// RouteSource — то, что умеет проложить путь по дорогам вместе с геометрией.
type RouteSource interface {
	RouteGeometry(ctx context.Context, a, b geo.Point) (*osrm.Route, bool)
}

// FillReport — что дорисовка сделала с треком.
type FillReport struct {
	Gaps     int     // сколько дыр нашлось
	Filled   int     // сколько из них дорисовано
	AddedM   float64 // прибавка к километражу, метры
	AddedPts int     // сколько точек добавлено
	Reasons  map[string]int
}

// FillGaps — стадия дорисовки.
type FillGaps struct {
	Routes RouteSource
	State  *RunState
}

func (FillGaps) Name() string { return "fill_gaps" }

// gap — дыра: позиция в треке и её концы.
type gap struct {
	at   int // индекс левого конца во входном срезе
	sec  float64
	line float64 // длина прямой между концами
}

func (f FillGaps) Apply(ctx context.Context, points []geo.Point) ([]geo.Point, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(points) < 2 {
		return points, nil, nil
	}
	if f.Routes == nil {
		return points, []string{"fill_gaps: источник путей не задан, дыры оставлены прямыми"}, nil
	}

	gaps := f.findGaps(points)
	rep := FillReport{Gaps: len(gaps), Reasons: map[string]int{}}
	if len(gaps) == 0 {
		f.save(rep, nil)
		return points, nil, nil
	}

	routes, err := f.ask(ctx, points, gaps)
	if err != nil {
		return nil, nil, err
	}

	// Что дорисовываем: позиция левого конца → путь.
	plan := make(map[int]*osrm.Route, len(gaps))
	for k, g := range gaps {
		ok, why := fillVerdict(g.sec, routes[k], g.line)
		rep.Reasons[why]++
		if !ok {
			continue
		}
		rep.Filled++
		rep.AddedM += routes[k].Distance - g.line
		plan[g.at] = routes[k]
	}

	out, synthetic := f.weave(points, plan)
	rep.AddedPts = len(out) - len(points)

	out, synthetic, dropped := simplifyDrawn(out, synthetic)
	rep.AddedPts -= dropped

	f.save(rep, synthetic)
	if rep.Filled == 0 {
		return out, nil, nil
	}
	return out, []string{fmt.Sprintf(
		"fill_gaps: дыр %d, дорисовано %d, добавлено %.0f м",
		rep.Gaps, rep.Filled, rep.AddedM)}, nil
}

// findGaps — переходы, которые имеет смысл дорисовать.
//
// Про стоянки. Условия «оба конца внутри ОДНОЙ стоянки» здесь нет, и это не
// упущение: к дорисовке стоянка уже схлопнута ядром в ОДНУ точку, поэтому
// двух её точек подряд не бывает. Замер на восьми настоящих треках — 0
// срабатываний из 54 129 переходов. Проверять же «оба конца — точки стоянок»
// нельзя: это отсекало бы законный перегон между двумя РАЗНЫМИ стоянками,
// то есть ровно тот дефект, который в прототипе и чинили.
func (f FillGaps) findGaps(points []geo.Point) []gap {
	var out []gap
	for i := 0; i < len(points)-1; i++ {
		a, b := points[i], points[i+1]

		sec := b.Time.Sub(a.Time)
		line := geo.Haversine(a, b)
		if !isGap(line, sec) {
			continue
		}
		out = append(out, gap{at: i, sec: sec.Seconds(), line: line})
	}
	return out
}

// isGap — дыра ли это. Вынесено отдельно, потому что порог иначе не проверить:
// расстояние считается гаверсинусом и ровно в пятьсот метров не попадает
// никогда, а условие сравнивает числа буквально.
func isGap(line float64, sec time.Duration) bool {
	return sec >= GapMinSec && line >= GapMinM
}

// ask спрашивает пути по всем дырам. Ответы кладутся строго по своим местам,
// поэтому порядок ответов на результат не влияет.
func (f FillGaps) ask(ctx context.Context, points []geo.Point, gaps []gap) ([]*osrm.Route, error) {
	routes := make([]*osrm.Route, len(gaps))

	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(fillWorkers, len(gaps)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range jobs {
				g := gaps[k]
				if r, ok := f.Routes.RouteGeometry(ctx, points[g.at], points[g.at+1]); ok {
					routes[k] = r
				}
			}
		}()
	}

	for k := range gaps {
		if ctx.Err() != nil {
			break
		}
		jobs <- k
	}
	close(jobs)
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return routes, nil
}

// fillVerdict — принять дорисовку или оставить прямую.
//
// Порядок проверок от самого надёжного признака к самому косвенному. Сначала
// крюк: он прямо меряет то единственное, чем дорисовка способна завысить.
// Проверка временем идёт после и работает уже только в средней полосе, где
// крюк ни о чём не говорит.
func fillVerdict(gapSec float64, r *osrm.Route, line float64) (bool, string) {
	if r == nil {
		return false, "нет пути"
	}
	if line > 0 {
		switch detour := r.Distance / line; {
		case detour <= FreeDetour:
			return true, "принято"
		case detour > MaxDetour:
			return false, "крюк"
		}
	}
	if r.Distance > gapSec*FillVmaxKmh/3.6+FillSlackM {
		return false, "физика"
	}
	return true, "принято"
}

// weave вплетает дорисованную геометрию в трек.
// Второе значение — пометка «точка выдумана по дорожной сети».
func (f FillGaps) weave(points []geo.Point, plan map[int]*osrm.Route) ([]geo.Point, []bool) {
	out := make([]geo.Point, 0, len(points))
	synthetic := make([]bool, 0, len(points))

	for i, p := range points {
		// Точку стоянки на дорогу НЕ сажаем. Стоянка на базе, складе или в поле
		// честно стоит вне дорожного графа, и сдвиг к ближайшей дороге стёр бы
		// само место — то, ради чего стоянка и хранится. Дорисовка начинается
		// с дороги, а короткий отрезок от ворот до неё — правда: машина его
		// проехала.
		if !f.isStop(p) {
			if prev, ok := plan[i-1]; ok && prev.HasSnapB {
				p.Lon, p.Lat = prev.SnapB[0], prev.SnapB[1]
			}
			if cur, ok := plan[i]; ok && cur.HasSnapA {
				p.Lon, p.Lat = cur.SnapA[0], cur.SnapA[1]
			}
		}
		out = append(out, p)
		synthetic = append(synthetic, false)

		r, ok := plan[i]
		if !ok || len(r.Coords) <= 2 {
			continue
		}

		// Время внутри дорисовки распределяем по доле пути: точной раскладки
		// не существует, но монотонность обязана сохраниться — по времени
		// идут все проверки после.
		t0, t1 := points[i].Time, points[i+1].Time
		acc, total := runningLength(r.Coords)
		for k := 1; k < len(r.Coords)-1; k++ {
			frac := acc[k] / max(total, 1)
			out = append(out, geo.Point{
				Time: t0.Add(time.Duration(float64(t1.Sub(t0)) * frac)),
				Lon:  r.Coords[k][0],
				Lat:  r.Coords[k][1],
			})
			synthetic = append(synthetic, true)
		}
	}
	return out, synthetic
}

// runningLength — накопленная длина по вершинам и общая длина.
func runningLength(coords [][2]float64) ([]float64, float64) {
	acc := make([]float64, len(coords))
	var total float64
	for i := 1; i < len(coords); i++ {
		total += geo.Haversine(
			geo.Point{Lon: coords[i-1][0], Lat: coords[i-1][1]},
			geo.Point{Lon: coords[i][0], Lat: coords[i][1]},
		)
		acc[i] = total
	}
	return acc, total
}

// simplifyDrawn упрощает ТОЛЬКО дорисованное: наблюдения — смысл трека, их
// форму трогать нельзя. Возвращает трек, пометки и число снятых точек.
func simplifyDrawn(points []geo.Point, synthetic []bool) ([]geo.Point, []bool, int) {
	drawn := 0
	for _, s := range synthetic {
		if s {
			drawn++
		}
	}
	if drawn == 0 || FillSimplifyM <= 0 {
		return points, synthetic, 0
	}

	must := make(map[PointKey]struct{}, len(points)-drawn)
	for i, p := range points {
		if !synthetic[i] {
			must[KeyOf(p)] = struct{}{}
		}
	}

	kept, _, _ := Simplify{
		MinMeters: FillSimplifyM,
		State:     &RunState{Must: must},
	}.Apply(context.Background(), points)

	// Пометки переносим ходом двумя указателями. Это законно ровно потому, что
	// упрощение только УДАЛЯЕТ точки и возвращает подпоследовательность —
	// свойство проверяется отдельным тестом (`TestSimplify_ResultIsSubsequence`).
	flags := make([]bool, 0, len(kept))
	k := 0
	for _, p := range kept {
		for points[k] != p {
			k++
		}
		flags = append(flags, synthetic[k])
		k++
	}
	return kept, flags, len(points) - len(kept)
}

// isStop — точка, которую нельзя двигать: за ней стоянка.
func (f FillGaps) isStop(p geo.Point) bool {
	if f.State == nil || len(f.State.Must) == 0 {
		return false
	}
	_, yes := f.State.Must[KeyOf(p)]
	return yes
}

func (f FillGaps) save(rep FillReport, synthetic []bool) {
	if f.State == nil {
		return
	}
	f.State.Fill = rep
	f.State.Synthetic = synthetic
}
