package core

import (
	"math"
	"time"

	"ariadne/internal/geo"
)

// Правила, которые видны ТОЛЬКО на готовой цепочке.
//
// До её построения их не проверить: в сыром треке между будущими соседями лежат
// точки, которые чистка потом выбросит, и разрывов ещё нет. Все они не удаляют
// точки, а копят штраф — цепочка перестраивается и проверяется заново.

// --- огрызки: приехал издалека, побыл нисколько, уехал --------------------

const (
	// StubGapM — разрыв, по которому режем цепочку на куски.
	//
	// Порог в 5 км был настроен на междугородние телепорты и пропускал
	// городские: в Ставрополе глюк въезжает скачком на 2.3–3.8 км, мелькает
	// пятью точками за минуту и уходит.
	StubGapM = 2000.0

	// StubTimeFrac — пробыл меньше этой доли времени эпизода — не заезд.
	StubTimeFrac = 0.10

	// StubPathM — «внутри почти не ездил».
	//
	// Порог поднят с 1500 м после разбора всех мест, названных пользователем:
	// подделка успевает намотать два-три километра и проходила проверку, хотя
	// главное условие выполнялось с запасом (Нащёкино 2055 м за 3.4 минуты при
	// 42 минутах дороги, Гончары 3072 м при 21 часе).
	StubPathM = 3500.0

	StubPenalty = 2.0
)

// CheckStubs ищет несоразмерные заезды: на дорогу потрачены десятки минут, на
// пребывание — секунды.
//
// Так выживали остатки в местах, которые пользователь назвал на карте прямо:
// Спас-Михнево — приехал за 48 минут, побыл 15 секунд, уехал за 10; Молоково —
// приехал за 40 минут, побыл 53 секунды, уехал за 37. Физику это не нарушает,
// точки лежат на дороге — ни одна прежняя проверка их не брала. Настоящий заезд
// берёт на себя время, соразмерное дороге.
//
// trusted — точки, признанные настоящими стоянками: они огрызком быть не могут,
// машина там именно что стояла.
func CheckStubs(pts []geo.Point, chain []int, penalty map[int]float64, trusted map[int]struct{}) int {
	if len(chain) < 3 {
		return 0
	}

	cuts := []int{0}
	for k := range len(chain) - 1 {
		if geo.Haversine(pts[chain[k]], pts[chain[k+1]]) >= StubGapM {
			cuts = append(cuts, k+1)
		}
	}
	cuts = append(cuts, len(chain))

	hit := 0
	// Куски перебираем ВСЕ, включая крайние. Раньше проверялись только те, у
	// которых разрыв с обеих сторон, и огрызок на краю трека проходил мимо:
	// машина «приехала» за 38 км и пробыла 33 секунды до конца записи.
	for c := range len(cuts) - 1 {
		lo, hi := cuts[c], cuts[c+1]-1
		if hi < lo {
			continue
		}
		if hasTrusted(chain, lo, hi, trusted) {
			continue
		}

		var inside float64
		if hi > lo {
			inside = pts[chain[hi]].Time.Sub(pts[chain[lo]].Time).Seconds()
		}

		// Время дороги считаем по той стороне, которая есть: у краевого куска
		// разрыв только с одной стороны.
		var travel float64
		if cuts[c] > 0 {
			travel += pts[chain[lo]].Time.Sub(pts[chain[cuts[c]-1]].Time).Seconds()
		}
		if cuts[c+1] < len(chain) {
			travel += pts[chain[cuts[c+1]]].Time.Sub(pts[chain[hi]].Time).Seconds()
		}
		if travel <= 0 {
			continue
		}
		if inside/(inside+travel) > StubTimeFrac {
			continue // пробыл соразмерно дороге — заезд
		}

		var path float64
		for j := lo; j < hi; j++ {
			path += geo.Haversine(pts[chain[j]], pts[chain[j+1]])
		}
		if path >= StubPathM {
			continue // внутри реально поездил — заезд
		}

		for j := lo; j <= hi; j++ {
			penalty[chain[j]] += StubPenalty
		}
		hit++
	}
	return hit
}

func hasTrusted(chain []int, lo, hi int, trusted map[int]struct{}) bool {
	for j := lo; j <= hi; j++ {
		if _, ok := trusted[chain[j]]; ok {
			return true
		}
	}
	return false
}

// --- вираж, на котором фура ложится набок --------------------------------

const (
	// LateralG — предел бокового ускорения, в долях g.
	//
	// Гружёный седельный тягач опрокидывается около 0.35 g, и водитель к этому
	// пределу никогда не приближается: на шести маршрутах честная езда, включая
	// круги по МКАД и КАД, нигде не переваливает за 0.36 g.
	//
	// Так ловится Поварово, где очищенный трек описывал замкнутую петлю:
	// двенадцать шагов подряд ровно по 53–54 м, скорость ровно 97 км/ч,
	// периметр около 730 м, то есть радиус 116 м и 0.58 g. Ни физика перехода,
	// ни близость к дороге, ни огрызки этого не брали — по отдельности каждый
	// шаг безупречен.
	LateralG = 0.35

	// LateralMinLegM — короче этого кривизну считать бессмысленно: угол по
	// координатам там считается с погрешностью в разы.
	LateralMinLegM = 40.0

	// LateralMinKmh — на малой скорости крутой разворот законен.
	LateralMinKmh = 30.0

	LateralPenalty = 2.0
)

// lateralG — боковое ускорение на средней точке тройки, в долях g.
// Радиус описанной окружности через площадь треугольника: R = abc / 4S.
// Скорость берём среднюю по двум плечам — именно она определяет центробежную силу.
//
// Второе значение false означает «судить не берёмся».
func lateralG(a, b, c geo.Point) (float64, bool) {
	d1 := geo.Haversine(a, b)
	d2 := geo.Haversine(b, c)
	d3 := geo.Haversine(a, c)
	if min(d1, d2) < LateralMinLegM {
		return 0, false
	}

	// Оба плеча обязаны иметь ненулевое время. При выгрузке буфера трекер ставит
	// пачке одно время отправки, и тогда 1906 честных метров «проезжаются» за
	// секунду — скорость выходит 7000 км/ч, а с ней и вираж, которого не было.
	dt := c.Time.Sub(a.Time).Seconds()
	if dt <= 0 || b.Time.Sub(a.Time) <= 0 || c.Time.Sub(b.Time) <= 0 {
		return 0, false
	}

	v := (d1 + d2) / dt
	if v*3.6 < LateralMinKmh {
		return 0, false
	}

	s := (d1 + d2 + d3) / 2
	area2 := s * (s - d1) * (s - d2) * (s - d3)
	if area2 <= 0 {
		return 0, true // вырожденный треугольник — прямая, виража нет
	}
	area := math.Sqrt(area2)
	if area < 1e-6 {
		return 0, true
	}
	r := d1 * d2 * d3 / (4 * area)
	if r <= 1e-6 {
		return 0, false
	}
	return (v * v / r) / 9.81, true
}

// CheckLateral ищет виражи, на которых фура легла бы набок.
func CheckLateral(pts []geo.Point, chain []int, penalty map[int]float64) int {
	if len(chain) < 3 {
		return 0
	}
	hit := 0
	for k := 1; k < len(chain)-1; k++ {
		g, ok := lateralG(pts[chain[k-1]], pts[chain[k]], pts[chain[k+1]])
		if ok && g >= LateralG {
			penalty[chain[k]] += LateralPenalty
			hit++
		}
	}
	return hit
}

// --- скорость на ОКНЕ времени, а не на шаге ------------------------------

const (
	// SpeedWindow — окно, на котором проверяется накопленный путь.
	//
	// Достижимость разрешает переход при «путь ≤ время × предел + допуск».
	// Допуск в 300 метров нужен для коротких интервалов, но даётся на КАЖДЫЙ
	// шаг и потому накапливается: замер по 12 маршрутам дал 3807 переходов
	// быстрее 110 км/ч на 469 км, худшие — 866 км/ч на шаге в одну секунду.
	//
	// Окно устойчиво к искажению отдельных меток, и это ключевое его свойство:
	// на треке, где трекер обновляет координату раз в шесть секунд, а метку
	// ставит каждую, шаг 145 м «проходится» за секунду (522 км/ч) — и таких
	// шагов 3248 на 393 км. На окне в минуту нарушений там РОВНО НОЛЬ: метки
	// врут, а километраж верен. Настоящую накрутку окно при этом видит.
	SpeedWindow = 60 * time.Second

	SpeedWinPenalty = 2.0
)

// CheckSpeedWin ищет окна, где накопленный путь не укладывается в предел скорости.
func CheckSpeedWin(pts []geo.Point, chain []int, penalty map[int]float64) int {
	n := len(chain)
	if n < 3 {
		return 0
	}

	steps := make([]float64, n-1)
	for i := range steps {
		steps[i] = geo.Haversine(pts[chain[i]], pts[chain[i+1]])
	}

	hit := 0
	j, acc := 0, 0.0
	for i := range n - 1 {
		if j < i {
			j, acc = i, 0.0
		}
		for j < n-1 && pts[chain[j+1]].Time.Sub(pts[chain[i]].Time) <= SpeedWindow {
			acc += steps[j]
			j++
		}
		dt := pts[chain[j]].Time.Sub(pts[chain[i]].Time).Seconds()
		if j > i && dt > 0 && acc > dt*ChainVmaxKmh/3.6+ChainSlackM {
			// Какая именно точка окна лишняя, по одному окну не понять —
			// штрафуем все, а цикл перестроит цепочку и проверит заново.
			for k := i; k <= j; k++ {
				penalty[chain[k]] += SpeedWinPenalty
			}
			hit++
		}
		acc -= steps[i]
	}
	return hit
}

// --- одиночка в паузе ----------------------------------------------------

const (
	// LonelyGap — пауза с обеих сторон.
	LonelyGap = 30 * time.Minute
	// LonelyMinM — и точка отстоит дальше, чем дрожит стоящая машина.
	LonelyMinM = 300.0
	// LonelyMaxGroup — одиночка или пара-тройка; длинная серия это заезд.
	LonelyMaxGroup = 3

	LonelyPenalty = 3.0
)

// CheckLonely ищет точки, оторванные по ВРЕМЕНИ, а не по расстоянию.
//
// Найдено на `5cde6306` — последний дефект, который пользователь видел на карте
// как линию к реке:
//
//	06:37  машина стоит
//	12:04  ОДНА точка в 1.2 км, через 5.5 часа
//	18:01  возврат, ещё через 6 часов
//
// Пока машина стоит, трекер изредка отдаёт точку с грубой ошибкой, и она рисует
// ход туда-обратно на 2.7 км. Правило островов её не берёт: оно режет трек по
// разрывам от 5 км, а здесь разрыв ничтожен по расстоянию и огромен по времени.
//
// Проверка: соседи по краям пауз стоят ближе друг к другу, чем к самой одиночке.
// Значит машина никуда не уезжала, и поездки не было.
func CheckLonely(pts []geo.Point, chain []int, penalty map[int]float64) int {
	n := len(chain)
	if n < 4 {
		return 0
	}

	hit := 0
	for k := 0; k < n; {
		j := k
		for j+1 < n && pts[chain[j+1]].Time.Sub(pts[chain[j]].Time) < LonelyGap {
			j++
		}

		if k > 0 && j+1 < n && j-k+1 <= LonelyMaxGroup &&
			pts[chain[k]].Time.Sub(pts[chain[k-1]].Time) >= LonelyGap &&
			pts[chain[j+1]].Time.Sub(pts[chain[j]].Time) >= LonelyGap {

			a, b := pts[chain[k-1]], pts[chain[j+1]]
			dPrev := geo.Haversine(a, pts[chain[k]])
			dNext := geo.Haversine(pts[chain[j]], b)
			dAround := geo.Haversine(a, b)

			if min(dPrev, dNext) >= LonelyMinM && dAround < min(dPrev, dNext) {
				for x := k; x <= j; x++ {
					penalty[chain[x]] += LonelyPenalty
				}
				hit++
			}
		}
		k = j + 1
	}
	return hit
}
