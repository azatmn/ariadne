package core

import (
	"context"
	"math"
	"time"

	"ariadne/internal/geo"
)

// Сборка ядра.
//
// Всё остальное в пакете — части: правила, веса, выбор цепочки. Здесь они
// складываются в один проход, и складываются в СТРОГОМ порядке. Порядок тут не
// стилистика: привилегия стоянке, множитель за число наблюдений, деление веса
// пачек, штраф заморозке, штрафы правил и амнистия дают разный результат при
// любой перестановке, притом что каждая часть по отдельности остаётся верной.
//
// Общий принцип, ради которого всё устроено именно так: правила НЕ УДАЛЯЮТ
// точки. Они только копят улики в весах. Удаляет один раз выбор цепочки, взвесив
// все улики разом. Правило, само выбрасывающее точки, ошибается необратимо —
// именно этим болели прежние поточечные фильтры, где одна плохая опора убивала
// всё за собой.

const (
	// StopTrustW — пол веса для стоянки, заслужившей доверие.
	//
	// У долгой стоянки снэп плохой судья: машина ночует на базе, складской
	// площадке или стоянке, которых в дорожном графе нет (на `4daf8725` у
	// стоянки на 4.1 часа снэп 113 м при полностью честных данных). Её роль в
	// цепочке — связать два куска, пробега она не добавляет.
	StopTrustW = 1.0

	// FrozenPenalty — штраф залипшей координате: место показано верно, но
	// устарело, машина уже уехала. Опорой такая точка быть не может — на
	// `4daf8725` на ней держались 9.4 часа мнимой стоянки, перед которой был
	// прыжок в 36 км.
	FrozenPenalty = 1.5

	// AirfieldPenalty — штраф уличённой точке и её эпизоду.
	//
	// Большой, чтобы цепочка не могла «окупить» такую точку соседями: в
	// `bd6a0ad0` иначе выживал переход Домодедово → Шереметьево → Кашира, где
	// обе скорости (61 и 66 км/ч) совершенно правдоподобны и физикой не ловятся.
	AirfieldPenalty = 5.0

	// AirfieldSpread — на сколько вокруг уличённой точки расходится штраф.
	//
	// Подделка живёт эпизодами, а не отдельными точками. В Шереметьеве из 266
	// точек 211 лежали на лётном поле, и оставшихся 55 хватало, чтобы переход
	// через него выжил.
	AirfieldSpread = 10 * time.Minute

	// DecoyPenalty — множитель штрафа за приманку: место, куда машины попадают
	// прыжком, а не приезжают. Улика статистическая, поэтому штраф
	// пропорционален её силе.
	DecoyPenalty = 3.0

	// SplitPenalty — штраф точке, уличённой в раздвоении.
	SplitPenalty = 3.0

	// RoadPasses — потолок числа проходов «построить цепочку → проверить её
	// дорогами → перестроить». Обычно сходится за два-три.
	RoadPasses = 12

	// sameSpotDeg — насколько близкими должны быть координаты, чтобы считать
	// их ОДНИМ повторённым значением, а не двумя наблюдениями.
	sameSpotDeg = 1e-6
)

// Snapper — то, что умеет сказать, насколько точка отстоит от дорожной сети.
// Интерфейс, а не клиент: ядро проверяется без сети.
type Snapper interface {
	Snap(ctx context.Context, pts []geo.Point) ([]float64, []bool, []string)
}

// DecoyMask — сила улики места по приманкам, на точку. 0 — обычное место.
//
// ⚠️ Данных сейчас НЕТ: список строился по 2522 трекам, и восстановить его
// нечем. Правило перенесено целиком и молчит, ровно как молчит сейчас
// прототип, — но останется рабочим, когда список пересоберут.
type DecoyMask interface {
	Mask(pts []geo.Point) []float64
}

// AirfieldMask — лежит ли точка внутри контура лётного поля.
//
// Это не эвристика с порогом, а физическая невозможность: там полосы, рулёжки
// и перрон, грузовик туда не попадёт.
//
// ⚠️ Контуров (852 полигона из OSM) сейчас тоже нет — см. DecoyMask.
type AirfieldMask interface {
	Mask(pts []geo.Point) []bool
}

// Core — чистка трека. Нулевое значение бесполезно: без Snap веса строить не
// на чем, без Road переходы не проверить.
type Core struct {
	Snap      Snapper
	Road      RoadClient
	Decoys    DecoyMask
	Airfields AirfieldMask
}

// Report — что ядро сделало с треком. Уходит в debug-ручку сервиса.
type Report struct {
	Reordered    int // точек, переставленных внутри пачек выгрузки буфера
	Collapsed    int // точек, схлопнутых в стоянки
	StopsTotal   int
	StopsTrusted int
	StopsFrozen  int
	Split        int // уличено раздвоением
	Spread       int // накрыто штрафом за эпизод
	Amnesty      int // из них оправдано
	Loops        int // окон, признанных петлями
	RoadBanned   int // запрещённых переходов
	RoadAsked    int // вопросов к маршрутизатору
	RoadPasses   int
	Dropped      int
	SnapMedian   float64
	KmBefore     float64
	KmAfter      float64

	// Stops — уцелевшие точки, представляющие стоянки, в нумерации ВХОДА.
	//
	// Нужны стадиям после ядра. Упрощению — чтобы не снять их геометрией:
	// схлопнутая стоянка лежит почти на прямой между въездом и выездом, и RDP
	// её убирает, хотя она несёт смысл помимо формы (машина стояла 32 минуты).
	// Дорисовке — чтобы не рисовать дорогу внутри стоянки: там никто не ехал.
	Stops []int
}

// stopFacts — что ядро узнало о стоянках, прежде чем строить веса.
// Индексы здесь и далее — позиции в СХЛОПНУТОМ треке.
type stopFacts struct {
	trusted  map[int]struct{} // заслужили привилегию
	observed map[int]int      // сколько наблюдений схлопнуто в точку
	frozen   map[int]struct{} // залипшая координата
	split    map[int]struct{} // уличены раздвоением
}

// Run чистит трек и возвращает индексы оставленных точек в нумерации ВХОДНОГО
// массива, по порядку следования.
//
// Порядок не обязан возрастать: внутри пачки с одной меткой времени точки
// переставляются, и вернуть их надо в исправленном порядке, а не в исходном.
func (c *Core) Run(ctx context.Context, pts []geo.Point) ([]int, Report, error) {
	var rep Report
	if err := ctx.Err(); err != nil {
		return nil, rep, err
	}
	n := len(pts)
	if n == 0 {
		return nil, rep, nil
	}

	// Раздвоение считаем ДО перестановки пачек — и потом ещё раз после.
	//
	// Правило к перестановке неустойчиво: порядок внутри пачки с одной меткой
	// меняет кластеризацию окна, а с ней возвраты и границы промежутка. Замер:
	// считать только до — чинится Астрахань (2327 точек → 0), но возвращается
	// Волово (0 → 126); только после — ровно наоборот. Перестановка
	// искусственна, поэтому берём ОБЪЕДИНЕНИЕ: точка уличена, если уличена хоть
	// в одном порядке.
	splitPre := FindSplit(pts)

	// Перестановка — первым делом: и стоянки, и цепочка считают шаги между
	// соседями, а перевёрнутая пачка даёт мнимые метания туда-обратно.
	perm := ReorderBatches(pts)
	for k, i := range perm {
		if k != i {
			rep.Reordered++
		}
	}

	work := pts
	if rep.Reordered > 0 {
		work = make([]geo.Point, n)
		for k, i := range perm {
			work[k] = pts[i]
		}
		splitPre = remapSplit(splitPre, perm)
	}
	rep.KmBefore = geo.TotalLength(work) / 1000

	// Стоянки: серию неподвижных точек схлопываем в одну — её первую.
	stops := FindStops(work, StopRadiusM, StopMinStay)
	rep.StopsTotal = len(stops)

	collapsed := make(map[int]struct{})
	for _, s := range stops {
		for i := s.Start + 1; i <= s.End; i++ {
			collapsed[i] = struct{}{}
		}
	}
	rep.Collapsed = len(collapsed)

	alive := make([]int, 0, n-len(collapsed))
	for i := range n {
		if _, gone := collapsed[i]; !gone {
			alive = append(alive, i)
		}
	}
	sub := make([]geo.Point, len(alive))
	for k, i := range alive {
		sub[k] = work[i]
	}

	// Судить нечего: цепочке нужно хотя бы четыре точки, чтобы правила по ней
	// вообще имели смысл.
	if len(sub) < 4 {
		rep.KmAfter = rep.KmBefore
		return originalIdx(alive, perm), rep, nil
	}

	snaps, ok := c.snapOf(ctx, sub)
	rep.SnapMedian = medianSnap(snaps, ok)

	// Позиция схлопнутой стоянки в `sub` — это её первая точка.
	pos := make(map[int]int, len(alive))
	for k, i := range alive {
		pos[i] = k
	}

	facts := stopFacts{
		trusted:  make(map[int]struct{}),
		observed: make(map[int]int),
		frozen:   make(map[int]struct{}),
		split:    make(map[int]struct{}),
	}
	for _, s := range stops {
		// Первая точка стоянки всегда жива: схлопываем мы начиная со следующей.
		k := pos[s.Start]
		snap, has := 0.0, false
		if k < len(snaps) && k < len(ok) {
			snap, has = snaps[k], ok[k]
		}
		if TrustedStop(work, s, snap, has) {
			facts.trusted[k] = struct{}{}
		}
		// Сколько наблюдений стоит за схлопнутой точкой. Без этого стоянка из
		// 95 точек за 14 часов весит ровно столько же, сколько случайная
		// одиночная точка рядом, — и проигрывает ей.
		facts.observed[k] = s.End - s.Start + 1
		// Залипание — это И долго, И без дрожания. Одного размаха мало:
		// короткая остановка на светофоре тоже неподвижна.
		if work[s.End].Time.Sub(work[s.Start].Time) >= StopTrustStay && IsFrozen(work, s) {
			facts.frozen[k] = struct{}{}
		}
	}
	rep.StopsTrusted = len(facts.trusted)
	rep.StopsFrozen = len(facts.frozen)

	// Раздвоение по сырому треку: на схлопнутом мера врёт в обратную сторону.
	// Настоящая стоянка сжата в ОДНУ точку и отмечается в одном слоте времени,
	// а подделка едет и остаётся россыпью по всем.
	splitRaw := FindSplit(work)
	for i := range splitPre {
		splitRaw[i] = struct{}{}
	}
	ends := make(map[int]int, len(stops))
	for _, s := range stops {
		ends[s.Start] = s.End
	}
	facts.split = splitOnCollapsed(alive, ends, splitRaw)
	rep.Split = len(facts.split)

	chain, err := c.pick(ctx, sub, snaps, ok, facts, &rep)
	if err != nil {
		return nil, rep, err
	}

	kept := make([]geo.Point, len(chain))
	keepAlive := make([]int, len(chain))
	for k, i := range chain {
		kept[k] = sub[i]
		keepAlive[k] = alive[i]
		// Ключи `observed` — это ровно позиции схлопнутых стоянок в `sub`.
		if _, isStop := facts.observed[i]; isStop {
			rep.Stops = append(rep.Stops, perm[alive[i]])
		}
	}
	rep.Dropped = len(sub) - len(chain)
	rep.KmAfter = geo.TotalLength(kept) / 1000

	return originalIdx(keepAlive, perm), rep, nil
}

// remapSplit переносит вердикт, вынесенный ДО перестановки пачек, в нумерацию
// переставленного трека.
//
// Без переноса объединение двух порядков складывало бы индексы из разных
// нумераций — то есть штрафовало бы случайные точки. Ошибка тихая: счётчик в
// отчёте выглядел бы правдоподобно.
func remapSplit(pre map[int]struct{}, perm []int) map[int]struct{} {
	out := make(map[int]struct{}, len(pre))
	for k, i := range perm {
		if _, in := pre[i]; in {
			out[k] = struct{}{}
		}
	}
	return out
}

// splitOnCollapsed переносит приговор раздвоения с СЫРЫХ точек на схлопнутые.
//
// Стоянка представлена одной точкой, за которой стоит вся серия наблюдений.
// Судим по большинству: уличена половина с лишним — уличена и точка. Порог
// именно большинство, а не «хоть одно»: правило метит целыми пластами, и
// единственное задетое наблюдение на краю серии не повод топить стоянку.
//
// ends — конец серии для точек, с которых начинается стоянка; остальные точки
// сами себе и начало, и конец.
func splitOnCollapsed(alive []int, ends map[int]int, splitRaw map[int]struct{}) map[int]struct{} {
	out := make(map[int]struct{})
	for k, a := range alive {
		b := a
		if e, in := ends[a]; in {
			b = e
		}
		hit := 0
		for i := a; i <= b; i++ {
			if _, in := splitRaw[i]; in {
				hit++
			}
		}
		if hit*2 > b-a+1 {
			out[k] = struct{}{}
		}
	}
	return out
}

// originalIdx переводит индексы переставленного трека обратно в нумерацию
// входного массива. При отсутствии перестановки `perm` тождественна.
func originalIdx(idx, perm []int) []int {
	out := make([]int, len(idx))
	for k, i := range idx {
		out[k] = perm[i]
	}
	return out
}

// pick — самая тяжёлая физически связная цепочка точек.
//
// Строим цепочку, проверяем её переходы по дорогам, запрещаем невозможные и
// строим заново. Обычно хватает двух-трёх проходов: плохих переходов в готовой
// цепочке единицы десятков, а не миллионы, как было бы при проверке всех пар
// внутри самого выбора.
//
// Отдельной проверки на короткий вход тут нет: `Run` не зовёт `pick` меньше чем
// на четырёх точках.
func (c *Core) pick(
	ctx context.Context,
	pts []geo.Point, snaps []float64, ok []bool,
	facts stopFacts, rep *Report,
) ([]int, error) {
	n := len(pts)
	w, wrep := c.buildWeights(pts, snaps, ok, facts)
	rep.Spread, rep.Amnesty = wrep.Spread, wrep.Amnesty

	st := NewRoadState()
	chain := BuildChain(pts, w, st.Banned)
	wp := make([]float64, n)

	for pass := range RoadPasses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rep.RoadPasses = pass + 1

		// (1) переходы, которые физически не проехать по дорогам
		banned := CheckByRoad(ctx, c.Road, pts, chain, st)
		// (2) петли: намотал за окно много, а никуда не приехал. Обязательно
		//     ПОСЛЕ первой чистки — на сыром треке телепорты раздувают путь в
		//     каждом окне и мера теряет смысл.
		loops := CheckLoops(ctx, c.Road, pts, chain, st)
		// (3) огрызки — видны только на готовой цепочке, когда соседи выброшены
		//     и разрывы проявились
		stubs := CheckStubs(pts, chain, st.Penalty, facts.trusted)
		// (4) виражи, на которых машина легла бы набок: в сыром треке между
		//     этими точками лежат другие
		tilt := CheckLateral(pts, chain, st.Penalty)
		// (5) скорость на окне времени — отдельно от достижимости, та даёт
		//     допуск на КАЖДЫЙ шаг, и на коротких интервалах он копится
		fast := CheckSpeedWin(pts, chain, st.Penalty)
		// (6) одиночки в паузе: видны, когда соседние точки стоянки схлопнуты
		lone := CheckLonely(pts, chain, st.Penalty)

		rep.Loops += loops

		// Дедлайн проверяем ещё раз, уже ПОСЛЕ правил. С отменённым контекстом
		// сетевые правила молча возвращают ноль, и цикл принял бы это за
		// «нарушений нет» — то есть выдал бы недосчитанный километраж как
		// готовый ответ. Молчание из-за отмены не то же самое, что чистый трек.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if banned == 0 && loops == 0 && stubs == 0 && tilt == 0 && fast == 0 && lone == 0 {
			break
		}

		for i := range n {
			wp[i] = w[i] - st.Penalty[i]
		}
		chain = BuildChain(pts, wp, st.Banned)
	}

	rep.RoadBanned = len(st.Banned)
	rep.RoadAsked = len(st.asked) + len(st.loops)
	return chain, nil
}

// buildWeights собирает веса точек перед выбором цепочки.
//
// Порядок наложения здесь и есть суть сборки, поэтому шаги пронумерованы: он
// проверен на данных, и переставлять их нельзя.
func (c *Core) buildWeights(
	pts []geo.Point, snaps []float64, ok []bool, facts stopFacts,
) ([]float64, Report) {
	var rep Report
	n := len(pts)
	raw, w := pointWeights(pts, snaps, ok)
	dec := c.decoys(pts)

	// (1) Привилегия доверенной стоянке — от СЫРОГО веса: у долгой стоянки
	// соседи это уже другой эпизод, и сглаживание топит её чужими глюками.
	for i := range facts.trusted {
		if i >= n || dec[i] > 0 {
			// «Стоянка» в приманочном месте доверия не заслуживает: там, куда
			// сотня чужих машин попала прыжком, наша не ночевала.
			continue
		}
		w[i] = max(raw[i], StopTrustW)
	}

	// (2) Множитель за число наблюдений — ПОВЕРХ привилегии. Рост
	// логарифмический: стоянка обязана перевешивать случайную точку, но не
	// должна оправдывать крюк через полстраны ради того, чтобы до неё дотянуться.
	for i, cnt := range facts.observed {
		if i >= n || w[i] <= 0 {
			continue
		}
		// Уличённой в раздвоении «стоянке» бонус не даём: многочисленность
		// подделки — её свойство, а не довод за неё. Настоящая стоянка пишет
		// раз в семь минут, подделка сыплет раз в пять секунд.
		if _, convicted := facts.split[i]; convicted {
			continue
		}
		w[i] *= 1 + math.Log10(float64(max(cnt, 1)))
	}

	// (3) Пачка записей с одной секундой = одно наблюдение по времени. Делим
	// вес поровну, иначе выгрузка буфера перевешивает честный редкий участок.
	perSec := make(map[int64]int, n)
	for _, p := range pts {
		perSec[p.Time.Unix()]++
	}
	for i := range n {
		if cnt := perSec[pts[i].Time.Unix()]; cnt > 1 && w[i] > 0 {
			w[i] /= float64(cnt)
		}
	}

	// (4) То же для повторённой КООРДИНАТЫ. Трекер, потерявший спутники, шлёт
	// последнюю известную позицию байт в байт — это одно значение, а не десять
	// наблюдений. Стоянки такие пачки ловят только от пяти минут, а короткие
	// проваливались мимо всех проверок с полным весом: снэп у них отличный,
	// потому что замерла машина на дороге.
	for start, i := 0, 1; i <= n; i++ {
		if i < n && sameSpot(pts[i], pts[start]) {
			continue
		}
		if cnt := i - start; cnt > 1 {
			for j := start; j < i; j++ {
				if w[j] > 0 {
					w[j] /= float64(cnt)
				}
			}
		}
		start = i
	}

	// (5) Заморозку штрафуем ПОСЛЕДНЕЙ, поверх всего: сколько бы точек ни
	// стояло за такой «стоянкой», это одно значение, повторённое много раз.
	for i := range facts.frozen {
		if i < n {
			w[i] = min(w[i], 0) - FrozenPenalty
		}
	}

	// (6) Уличённые лично. Лётные поля добавляются ПОСЛЕ сглаживания:
	// сглаживание нужно, чтобы одинокая точка не решала судьбу участка, а тут
	// решение как раз одиночное и безусловное — соседи не должны его размывать.
	bad := make(map[int]struct{})
	for i, on := range c.airfields(pts) {
		if on {
			bad[i] = struct{}{}
		}
	}
	for _, rule := range []map[int]struct{}{FindTraps(pts), FindIslands(pts), FindDual(pts)} {
		for i := range rule {
			bad[i] = struct{}{}
		}
	}

	// (7) Штраф расходится на эпизод: подделка — это эпизод целиком, а не
	// отдельные точки в нём.
	spread := make(map[int]struct{}, len(bad))
	for i := range bad {
		spread[i] = struct{}{}
		t := pts[i].Time
		for j := i; j > 0 && t.Sub(pts[j-1].Time) <= AirfieldSpread; {
			j--
			spread[j] = struct{}{}
		}
		for j := i; j < n-1 && pts[j+1].Time.Sub(t) <= AirfieldSpread; {
			j++
			spread[j] = struct{}{}
		}
	}

	// (8) Прежде чем штрафовать — даём попавшим под раздачу шанс оправдаться.
	// Точки в приманках судим тем же порядком: сперва под подозрение, потом
	// шанс оправдаться непрерывностью с чистой частью, иначе штраф скосил бы
	// честный проезд по трассе, которая идёт через приманочную клетку.
	decoyed := make(map[int]struct{})
	for i, v := range dec {
		if v > 0 {
			decoyed[i] = struct{}{}
		}
	}
	suspect := make(map[int]struct{}, len(spread)+len(decoyed))
	for i := range spread {
		suspect[i] = struct{}{}
	}
	for i := range decoyed {
		suspect[i] = struct{}{}
	}
	amnesty := FindAmnesty(pts, snaps, ok, suspect, bad, dec)

	// Долгая стоянка оправдывает себя сама: цепочка доверия сквозь глюк не
	// проходит, а стоящая машина пробег не накручивает. Уличённых лично это
	// по-прежнему не касается, как и «стоянок» в приманочных местах.
	for i := range facts.trusted {
		if i >= n || dec[i] > 0 {
			continue
		}
		if _, convicted := bad[i]; convicted {
			continue
		}
		amnesty[i] = struct{}{}
	}

	// (9) И только теперь — штрафы.
	for i := range spread {
		if _, saved := amnesty[i]; !saved {
			w[i] -= AirfieldPenalty
		}
	}
	for i := range decoyed {
		if _, saved := amnesty[i]; !saved {
			w[i] -= DecoyPenalty * dec[i]
		}
	}
	// Раздвоение — без расширения на окрестность и без амнистии. Оно уличает
	// целыми пластами и само знает, какое место настоящее: разносить его штраф
	// значит топить ровно то место, которое правило признало настоящим. А
	// амнистия его не берёт, потому что подделка здесь связная и лежит на
	// асфальте — её оправдал бы любой адвокат.
	for i := range facts.split {
		if i < n {
			w[i] -= SplitPenalty
		}
	}

	rep.Spread, rep.Amnesty = len(spread), len(amnesty)
	return w, rep
}

// sameSpot — одна и та же координата, повторённая трекером.
func sameSpot(a, b geo.Point) bool {
	return math.Abs(a.Lon-b.Lon) < sameSpotDeg && math.Abs(a.Lat-b.Lat) < sameSpotDeg
}

// snapOf спрашивает расстояния до дорог, подстраховываясь от короткого ответа.
// Молчание маршрутизатора — не улика: вес такой точки просто нулевой.
func (c *Core) snapOf(ctx context.Context, pts []geo.Point) ([]float64, []bool) {
	snaps := make([]float64, len(pts))
	ok := make([]bool, len(pts))
	if c.Snap == nil {
		return snaps, ok
	}
	got, gotOK, _ := c.Snap.Snap(ctx, pts)
	copy(snaps, got)
	copy(ok, gotOK)
	return snaps, ok
}

// medianSnap — типичное расстояние до дороги на этом треке.
func medianSnap(snaps []float64, ok []bool) float64 {
	valid := make([]float64, 0, len(snaps))
	for i, s := range snaps {
		if i < len(ok) && ok[i] {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return 0
	}
	return medianInPlace(valid)
}

// decoys / airfields — маски по точкам; без данных это нули и «нет».
func (c *Core) decoys(pts []geo.Point) []float64 {
	if c.Decoys == nil {
		return make([]float64, len(pts))
	}
	return fitLen(c.Decoys.Mask(pts), len(pts))
}

func (c *Core) airfields(pts []geo.Point) []bool {
	if c.Airfields == nil {
		return make([]bool, len(pts))
	}
	return fitLen(c.Airfields.Mask(pts), len(pts))
}

// fitLen подгоняет маску под длину трека: источник данных внешний, и полагаться
// на то, что он вернёт ровно столько, сколько спросили, нельзя.
func fitLen[T any](v []T, n int) []T {
	if len(v) == n {
		return v
	}
	out := make([]T, n)
	copy(out, v)
	return out
}
