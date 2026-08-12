package pipeline

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"ariadne/internal/geo"
	"ariadne/internal/logger"
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

	// Degraded — бюджет кончился раньше, чем спросили про все дыры.
	//
	// Недоспрошенные остаются прямыми: километраж занижен ровно так же, как
	// был бы занижен без дорисовки вовсе. Потеря ограниченная и понятная,
	// поэтому в карточку идёт результат с пометкой, а не `failed`.
	Degraded bool
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
		// Дыры остаются прямыми через поля — километраж занижен ровно на
		// разницу между дорогой и хордой.
		if f.State != nil {
			f.State.Degraded = true
		}
		return points, []string{"fill_gaps: no route source configured, gaps left as straight lines"}, nil
	}

	gaps := f.findGaps(points)
	rep := FillReport{Gaps: len(gaps), Reasons: map[string]int{}}
	if len(gaps) == 0 {
		f.save(rep, nil)
		return points, nil, nil
	}

	routes, spent, err := f.ask(ctx, points, gaps)
	if err != nil {
		return nil, nil, err
	}
	rep.Degraded = spent

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

	var warnings []string
	if rep.Filled > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"fill_gaps: %d gaps, %d filled, %.0f m added",
			rep.Gaps, rep.Filled, rep.AddedM))
	}
	if rep.Degraded {
		warnings = append(warnings, fmt.Sprintf(
			"fill_gaps: budget spent, %d of %d gaps left straight — mileage understated",
			rep.Gaps-rep.Filled, rep.Gaps))
	}
	return out, warnings, nil
}

// findGaps — переходы, которые имеет смысл дорисовать.
//
// Про стоянки. В прототипе есть отсечка «оба конца перехода внутри ОДНОЙ
// стоянки». Здесь её нет, и это осознанно.
//
// Ядро отдаёт по ОДНОЙ точке на стоянку — по построению, а не по совпадению:
// схлопывается всё, кроме первой точки серии, какой бы длинной та ни была.
// Значит на входе дорисовки двух точек одной стоянки не бывает никогда, и
// условие недостижимо. Замер по всему корпусу это подтверждает: 55 треков,
// 2985 стоянок, 752 802 перехода, максимум ОДНА уцелевшая точка на стоянку,
// ноль пар «оба конца в одной стоянке».
//
// Приблизить его нельзя. Единственное, что стадия знает, — какие точки
// являются стоянками; проверка «оба конца — стоянки» отсекала бы законный
// перегон между двумя РАЗНЫМИ стоянками (74 таких пары на десяти треках, 14
// из них дорисовываются). Это ровно тот дефект, который в прототипе и чинили.
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
func (f FillGaps) ask(ctx context.Context, points []geo.Point, gaps []gap) ([]*osrm.Route, bool, error) {
	routes := make([]*osrm.Route, len(gaps))

	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(fillWorkers, len(gaps)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Паника в дочерней горутине проходит мимо `recover` воркера — он
			// ловит только в своей — и валит ВЕСЬ процесс вместе с чужими
			// задачами. Здесь она стоит недорисованной дыры: путь остаётся
			// пустым, вердикт по нему будет «нет пути», то есть прямая. Это
			// уже предусмотренный случай, ровно как при исчерпании бюджета.
			defer func() {
				if r := recover(); r != nil {
					logger.FromContext(ctx).Error("panic in fill worker",
						"panic", r, "stack", string(debug.Stack()))
				}
			}()
			for k := range jobs {
				g := gaps[k]
				if r, ok := f.Routes.RouteGeometry(ctx, points[g.at], points[g.at+1]); ok {
					routes[k] = r
				}
			}
		}()
	}

	// Отправка ПОД `select`, а не голым `jobs <- k`.
	//
	// Иначе стадия висит вечно, когда принимать некому. Случай не выдуманный:
	// `recover` выше ловит панику, но горутина после неё выходит из цикла — и
	// если попадали все, отправитель ждёт места в канале до конца жизни
	// процесса. Раньше та же паника роняла процесс: громко и лечилось
	// перезапуском; стало бы тихо и навсегда — воркер занят, задача не
	// подтверждается, уборщик отдаёт её следующему, и так пока пул не кончится,
	// а сервис снаружи выглядит живым.
	//
	// Отмена ограничивает ожидание бюджетом задачи: недоспрошенные дыры
	// останутся прямыми, и это уже предусмотренный случай.
	for k := range gaps {
		select {
		case jobs <- k:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			spent, err := budgetSpent(ctx)
			if err != nil {
				return nil, false, err
			}
			return routes, spent, nil
		}
	}
	close(jobs)
	wg.Wait()

	// Бюджет кончился — недоспрошенные дыры остаются пустыми, и вердикт по
	// ним будет «нет пути», то есть прямая. Отмена — другое дело: ждать уже
	// некому.
	spent, err := budgetSpent(ctx)
	if err != nil {
		return nil, false, err
	}
	return routes, spent, nil
}

// budgetSpent — можно ли продолжать считать.
//
// Различает два случая, которые легко спутать.
//
// DEADLINE — задача упёрлась в свой срок (RESOLVE_TIMEOUT). Карточка в Redis
// будет записана в любом случае, вопрос только в том, что в ней окажется:
// результат с пометкой неполноты или `failed`. Результат полезнее — Laravel
// прочитает его и увидит предупреждение.
//
// CANCELED — обработку прервали снаружи. В асинхронном пути этого не бывает:
// `procCtx` в воркере растёт из `context.Background()`, поэтому выключение
// сервиса начатую задачу не рвёт (её домолачивают в дренаж). Отмена приходит
// только из синхронной отладочной ручки — там браузер отвалился, и результат
// читать некому.
func budgetSpent(ctx context.Context) (bool, error) {
	err := ctx.Err()
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, context.DeadlineExceeded):
		return true, nil
	default:
		return false, err
	}
}

// Названия причин — ДАННЫЕ, а не текст: они уезжают в `extra.reasons` и
// сверяются с эталоном от прототипа по золотым векторам. Менять их в одном Go
// нельзя — только вместе с прототипом и пересборкой векторов (сделано
// 2026-08-12: пересобрали, сверили — кроме самих названий не изменилось
// ничего).
//
// fillVerdict — принять дорисовку или оставить прямую.
//
// Порядок проверок от самого надёжного признака к самому косвенному. Сначала
// крюк: он прямо меряет то единственное, чем дорисовка способна завысить.
// Проверка временем идёт после и работает уже только в средней полосе, где
// крюк ни о чём не говорит.
func fillVerdict(gapSec float64, r *osrm.Route, line float64) (bool, string) {
	if r == nil {
		return false, "no route"
	}
	if line > 0 {
		switch detour := r.Distance / line; {
		case detour <= FreeDetour:
			return true, "accepted"
		case detour > MaxDetour:
			return false, "detour"
		}
	}
	if r.Distance > gapSec*FillVmaxKmh/3.6+FillSlackM {
		return false, "physics"
	}
	return true, "accepted"
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

	// Защищаем наблюдения ПО ПОЗИЦИИ, а не по значению. Начало дорисованного
	// пути совпадает с концом предыдущего наблюдения байт в байт — то же
	// время, те же координаты, — и опознание «по значению» защитило бы эту
	// копию тоже, оставив в треке дубликат. Найдено сверкой на `5f5dd0f1`.
	idx := simplifyKeep(points, FillSimplifyM, func(i int) bool { return !synthetic[i] })

	kept := make([]geo.Point, len(idx))
	flags := make([]bool, len(idx))
	for k, i := range idx {
		kept[k], flags[k] = points[i], synthetic[i]
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
	if rep.Degraded {
		f.State.Degraded = true
	}
}
