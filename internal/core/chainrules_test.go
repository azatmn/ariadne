package core

import (
	"math"
	"testing"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Правила, которые видны ТОЛЬКО на готовой цепочке.
//
// До её построения их не проверить: в сыром треке между будущими соседями лежат
// точки, которые чистка потом выбросит, и разрывов ещё нет.
//
// У каждого правила здесь две обязательные проверки помимо основной:
//
//   - `*_ShortChain` — цепочка короче, чем правилу нужно соседей. Все четыре
//     правила ходят по цепочке окнами и берут соседа по индексу; на пустой
//     цепочке и на цепочке из одной-двух точек это выход за границы среза.
//     Случай настоящий: после жёсткой чистки в цепочке остаётся две точки.
//   - `*_Clean` / `*_IsClean` — на честной езде правило обязано молчать. Без
//     этой половины правило, штрафующее всё подряд, прошло бы тесты: основная
//     проверка смотрит только на то, что виновного нашли.

func penaltyOf(p map[int]float64, idx ...int) float64 {
	var sum float64
	for _, i := range idx {
		sum += p[i]
	}
	return sum
}

// --------------------------------------------------------- CheckStubs

// stubTrack — обычная езда, короткий заезд далеко в сторону, снова езда.
// Подъезд и отъезд длинные и с движением внутри, поэтому огрызком считается
// только сам заезд.
func stubTrack(insideSec int, insidePts int) ([]geo.Point, int, int) {
	var pts []geo.Point
	pts = append(pts, drive(20, 60, 10.0, 0.0, 0.004, 0)...) // подъезд, 20 минут

	lo := len(pts)
	base := 2880
	for k := range insidePts {
		pts = append(pts, at(base+k*insideSec/max(insidePts-1, 1),
			10.30+float64(k)*0.00002, 0.0)) // 22 км в стороне, внутри почти не ездим
	}
	hi := len(pts) - 1

	pts = append(pts, drive(20, 60, 10.05, 0.0, 0.004, base+insideSec+600)...)
	return pts, lo, hi
}

func TestCheckStubs_CatchesDisproportionateVisit(t *testing.T) {
	// Случай Спас-Михнево: приехал за 48 минут, побыл 15 секунд, уехал за 10.
	// Физику это не нарушает, точки лежат на дороге — ни одна прежняя проверка
	// такого не берёт. Признак не в расстоянии и не в скорости, а в
	// НЕСОРАЗМЕРНОСТИ: на дорогу потрачены десятки минут, на пребывание секунды.
	pts, lo, hi := stubTrack(15, 2)
	pen := map[int]float64{}

	hit := CheckStubs(pts, chainOf(len(pts)), pen, nil)
	assert.Positive(t, hit)
	assert.Positive(t, penaltyOf(pen, lo, hi), "огрызок обязан быть наказан")

	// Подъезд и отъезд — настоящая езда, их трогать нельзя.
	assert.Zero(t, penaltyOf(pen, 0, 1, 2, 3), "подъезд наказан зря")
	last := len(pts) - 1
	assert.Zero(t, penaltyOf(pen, last, last-1, last-2), "отъезд наказан зря")
}

func TestCheckStubs_RealVisitSurvives(t *testing.T) {
	// Настоящий заезд берёт на себя время, соразмерное дороге.
	pts, lo, hi := stubTrack(3600, 12) // пробыл час
	pen := map[int]float64{}
	CheckStubs(pts, chainOf(len(pts)), pen, nil)
	assert.Zero(t, penaltyOf(pen, lo, hi), "часовой заезд огрызком не считаем")
}

func TestCheckStubs_DrivingInsideSurvives(t *testing.T) {
	// Пробыл мало, зато внутри реально поездил — значит заезжал по делу.
	var pts []geo.Point
	pts = append(pts, drive(20, 60, 10.0, 0.0, 0.004, 0)...) // подъезд

	lo := len(pts)
	pts = append(pts, drive(8, 30, 10.30, 0.0, 0.006, 2880)...) // 4.7 км внутри
	hi := len(pts) - 1

	pts = append(pts, drive(20, 60, 10.05, 0.0, 0.004, 4000)...) // отъезд

	pen := map[int]float64{}
	CheckStubs(pts, chainOf(len(pts)), pen, nil)
	assert.Zero(t, penaltyOf(pen, rangeIdx(lo, hi)...),
		"внутри ездили — это заезд, а не огрызок")
}

func TestCheckStubs_TrustedStopIsNeverStub(t *testing.T) {
	// Кусок, признанный настоящей стоянкой, огрызком быть не может: машина
	// там именно что стояла. Без этой оговорки проверка краевых кусков
	// сносила ночлег на 14.75 часа в начале трека.
	pts, lo, hi := stubTrack(15, 2)
	trusted := map[int]struct{}{lo: {}}

	pen := map[int]float64{}
	CheckStubs(pts, chainOf(len(pts)), pen, trusted)
	assert.Zero(t, penaltyOf(pen, lo, hi), "доверенную стоянку огрызком не объявляем")
}

func TestCheckStubs_EdgeStubIsCaught(t *testing.T) {
	// Огрызок на краю трека: разрыв только с одной стороны. Раньше такие
	// проходили мимо — в bd6a0ad0 машина «приехала» за 38 км и пробыла
	// 33 секунды до самого конца записи.
	pts := []geo.Point{
		at(0, 10.00, 0.0),
		at(600, 10.01, 0.0),
		at(3000, 10.40, 0.0),   // приехал за 40 минут, 33 км
		at(3033, 10.4001, 0.0), // и на этом запись кончилась
	}
	pen := map[int]float64{}
	assert.Positive(t, CheckStubs(pts, []int{0, 1, 2, 3}, pen, nil))
	assert.Positive(t, penaltyOf(pen, 2, 3))
}

// Огрызок ищется между двумя разрывами — нужны минимум три точки.
func TestCheckStubs_ShortChain(t *testing.T) {
	pts := drive(5, 60, 10.0, 0.0, 0.01, 0)
	for _, chain := range [][]int{nil, {0}, {0, 1}} {
		assert.Zero(t, CheckStubs(pts, chain, map[int]float64{}, nil))
	}
}

// Зеркало: без разрывов заезжать некуда, штрафов быть не должно ни одного.
// Проверяется и возврат, и сама карта штрафов — правило могло бы вернуть
// ноль, но по дороге кого-нибудь оштрафовать.
func TestCheckStubs_ContinuousTrackHasNoStubs(t *testing.T) {
	pts := drive(50, 60, 10.0, 0.0, 0.005, 0) // шаг 556 м, разрывов нет
	pen := map[int]float64{}
	assert.Zero(t, CheckStubs(pts, chainOf(50), pen, nil))
	assert.Empty(t, pen)
}

// -------------------------------------------------------- CheckLateral

func TestCheckLateral_CatchesImpossibleTurn(t *testing.T) {
	// Случай Поварово: очищенный трек описывал замкнутую петлю — двенадцать
	// шагов подряд ровно по 53–54 м, скорость ровно 97 км/ч, радиус 116 м,
	// то есть 0.58 g. Гружёный тягач опрокидывается около 0.35 g.
	//
	// Строим круг радиусом 116 м, проходимый за те же 97 км/ч.
	const r = 116.0
	var pts []geo.Point
	for k := range 12 {
		ang := float64(k) * 2 * 3.14159265 / 12
		// 1 градус широты ≈ 111320 м
		dLat := r * cos(ang) / 111320
		dLon := r * sin(ang) / 111320
		pts = append(pts, at(k*2, 10.0+dLon, 0.0+dLat)) // шаг 2 с
	}
	pen := map[int]float64{}
	hit := CheckLateral(pts, chainOf(len(pts)), pen)

	assert.Positive(t, hit, "вираж, на котором фура ложится набок, обязан ловиться")
	assert.NotEmpty(t, pen)
}

// Зеркало к CatchesImpossibleTurn: 96 км/ч по прямой — самый обычный ход по
// трассе, и боковой перегрузки в нём нет никакой.
func TestCheckLateral_StraightRoadIsClean(t *testing.T) {
	pts := drive(30, 5, 10.0, 0.0, 0.0012, 0) // ~96 км/ч по прямой
	pen := map[int]float64{}
	assert.Zero(t, CheckLateral(pts, chainOf(30), pen))
	assert.Empty(t, pen)
}

func TestCheckLateral_SlowTurnIsLegal(t *testing.T) {
	// Тот же крутой поворот, но на десяти километрах в час — законный
	// разворот во дворе.
	const r = 116.0
	var pts []geo.Point
	for k := range 12 {
		ang := float64(k) * 2 * 3.14159265 / 12
		pts = append(pts, at(k*30, 10.0+r*sin(ang)/111320, 0.0+r*cos(ang)/111320))
	}
	pen := map[int]float64{}
	assert.Zero(t, CheckLateral(pts, chainOf(len(pts)), pen),
		"на малой скорости крутой поворот законен")
}

func TestCheckLateral_ShortLegsAreIgnored(t *testing.T) {
	// Плечи короче сорока метров: угол по координатам считается с погрешностью
	// в разы, и кривизна там — шум приёмника, а не вираж.
	pts := []geo.Point{
		at(0, 10.00000, 0.0),
		at(1, 10.00020, 0.0),    // 22 м
		at(2, 10.00020, 0.0002), // 22 м вбок — резкий угол
	}
	pen := map[int]float64{}
	assert.Zero(t, CheckLateral(pts, []int{0, 1, 2}, pen))
}

func TestCheckLateral_SameTimestampBatchIsIgnored(t *testing.T) {
	// При выгрузке буфера пачке ставится одно время, и 1906 честных метров
	// «проезжаются» за секунду — скорость выходит 7000 км/ч, а с ней и вираж,
	// которого не было. Так сносило десять точек ровной езды по трассе.
	pts := []geo.Point{
		at(0, 10.000, 0.0),
		at(0, 10.010, 0.0), // то же время
		at(1, 10.010, 0.01),
	}
	pen := map[int]float64{}
	assert.Zero(t, CheckLateral(pts, []int{0, 1, 2}, pen),
		"нулевое время на плече — не повод считать вираж")
}

// Поворот считается по трём точкам подряд — на двух его не существует.
func TestCheckLateral_ShortChain(t *testing.T) {
	pts := drive(5, 5, 10.0, 0.0, 0.001, 0)
	for _, chain := range [][]int{nil, {0}, {0, 1}} {
		assert.Zero(t, CheckLateral(pts, chain, map[int]float64{}))
	}
}

// ------------------------------------------------------- CheckSpeedWin

func TestCheckSpeedWin_CatchesAccumulatedSlack(t *testing.T) {
	// Допуск в 300 метров даётся на КАЖДЫЙ шаг и потому накапливается: сорок
	// шагов по секунде дают двенадцать километров мнимого запаса. Окно в
	// минуту это вскрывает.
	var pts []geo.Point
	for k := range 60 {
		pts = append(pts, at(k, 10.0+float64(k)*0.0025, 0.0)) // 278 м за секунду
	}
	pen := map[int]float64{}
	hit := CheckSpeedWin(pts, chainOf(len(pts)), pen)

	assert.Positive(t, hit, "километр в минуту — это тысяча км/ч")
	assert.NotEmpty(t, pen)
}

func TestCheckSpeedWin_HonestHighwayIsClean(t *testing.T) {
	// Девяносто км/ч ровно, минута за минутой.
	var pts []geo.Point
	for k := range 120 {
		pts = append(pts, at(k*10, 10.0+float64(k)*0.00225, 0.0)) // 250 м за 10 с
	}
	pen := map[int]float64{}
	assert.Zero(t, CheckSpeedWin(pts, chainOf(len(pts)), pen))
	assert.Empty(t, pen)
}

func TestCheckSpeedWin_ToleratesBrokenTimestamps(t *testing.T) {
	// Трекер обновляет координату раз в шесть секунд, а метку ставит каждую:
	// шаг 145 м «проходится» за секунду (522 км/ч). Метки врут, а километраж
	// верен — на окне в минуту нарушений быть не должно.
	var pts []geo.Point
	for k := range 120 {
		lon := 10.0 + float64(k/6)*0.0013 // координата меняется раз в 6 шагов
		pts = append(pts, at(k, lon, 0.0))
	}
	pen := map[int]float64{}
	assert.Zero(t, CheckSpeedWin(pts, chainOf(len(pts)), pen),
		"кривые метки при верном километраже нарушением не считаются")
}

func TestCheckSpeedWin_PenalisesWholeWindow(t *testing.T) {
	// Какая именно точка окна лишняя, по одному окну не понять — штрафуем все,
	// а цикл перестроит цепочку и проверит заново.
	var pts []geo.Point
	for k := range 30 {
		pts = append(pts, at(k*2, 10.0+float64(k)*0.005, 0.0))
	}
	pen := map[int]float64{}
	CheckSpeedWin(pts, chainOf(len(pts)), pen)
	assert.Greater(t, len(pen), 5, "наказывается окно целиком, а не одна точка")
}

// Окно набирается по времени; на двух точках набирать нечего.
func TestCheckSpeedWin_ShortChain(t *testing.T) {
	pts := drive(5, 10, 10.0, 0.0, 0.001, 0)
	for _, chain := range [][]int{nil, {0}, {0, 1}} {
		assert.Zero(t, CheckSpeedWin(pts, chain, map[int]float64{}))
	}
}

// -------------------------------------------------------- CheckLonely

func TestCheckLonely_CatchesPointInPause(t *testing.T) {
	// Случай Пензенской улицы: машина стоит, и трекер изредка отдаёт точку с
	// грубой ошибкой, рисующую ход туда-обратно на 2.7 км.
	//
	//   06:37  машина стоит
	//   12:04  ОДНА точка в 1.2 км, через 5.5 часа
	//   18:01  возврат, ещё через 6 часов
	//
	// Правило островов её не берёт: оно режет трек по разрывам от 5 км, а
	// здесь разрыв ничтожен по расстоянию и огромен по времени.
	// Точек в цепочке должно быть хотя бы четыре — на трёх правило молчит,
	// как и в прототипе: одиночка опознаётся по паузам С ОБЕИХ сторон, и
	// нужны соседи за этими паузами.
	pts := []geo.Point{
		at(0, 45.95082, 51.49300),     // машина стоит
		at(600, 45.95085, 51.49302),   // и продолжает стоять
		at(19620, 45.95789, 51.48254), // +5.5 часа, ОДНА точка в 1.2 км
		at(41040, 45.94882, 51.49446), // +6 часов, вернулись на место
		at(41640, 45.94885, 51.49448),
	}
	pen := map[int]float64{}
	hit := CheckLonely(pts, chainOf(len(pts)), pen)

	assert.Positive(t, hit)
	assert.Positive(t, pen[2], "одиночка в паузе обязана быть наказана")
	assert.Zero(t, penaltyOf(pen, 0, 1, 3, 4), "соседей не трогаем")
}

// Зеркало к CatchesPointInPause: при ровной езде пауз нет вовсе, а значит
// нет и одиноких точек внутри них.
func TestCheckLonely_NormalDrivingIsClean(t *testing.T) {
	pts := drive(50, 60, 10.0, 0.0, 0.01, 0)
	pen := map[int]float64{}
	assert.Zero(t, CheckLonely(pts, chainOf(50), pen))
	assert.Empty(t, pen)
}

func TestCheckLonely_RealTripBetweenPausesSurvives(t *testing.T) {
	// Между паузами машина реально уехала: соседи по краям пауз ДАЛЬШЕ друг
	// от друга, чем от промежуточной точки. Значит поездка была.
	pts := []geo.Point{
		at(0, 10.00, 0.0),
		at(19620, 10.10, 0.0), // уехал на 11 км
		at(41040, 10.20, 0.0), // и поехал дальше
	}
	pen := map[int]float64{}
	assert.Zero(t, CheckLonely(pts, []int{0, 1, 2}, pen),
		"машина уехала и не вернулась — это поездка")
}

func TestCheckLonely_LongSeriesIsNotLonely(t *testing.T) {
	// Длинная серия точек в паузе — это заезд, а не одиночная ошибка.
	var pts []geo.Point
	pts = append(pts, at(0, 10.0, 0.0))
	for k := range 8 {
		pts = append(pts, at(19620+k*30, 10.01, 0.0))
	}
	pts = append(pts, at(41040, 10.0, 0.0))

	pen := map[int]float64{}
	assert.Zero(t, CheckLonely(pts, chainOf(len(pts)), pen))
}

func TestCheckLonely_TooCloseIsJitter(t *testing.T) {
	// Точка отстоит меньше, чем дрожит стоящая машина, — это шум, а не «поездка».
	pts := []geo.Point{
		at(0, 10.0, 0.0),
		at(19620, 10.001, 0.0), // 111 м
		at(41040, 10.0, 0.0),
	}
	pen := map[int]float64{}
	assert.Zero(t, CheckLonely(pts, []int{0, 1, 2}, pen))
}

// Одиночество меряется по обоим соседям — нужны точки слева И справа,
// то есть цепочка хотя бы из четырёх.
func TestCheckLonely_ShortChain(t *testing.T) {
	pts := drive(5, 60, 10.0, 0.0, 0.01, 0)
	for _, chain := range [][]int{nil, {0}, {0, 1}, {0, 1, 2}} {
		assert.Zero(t, CheckLonely(pts, chain, map[int]float64{}))
	}
}

// ------------------------------------------------------------ общее

// Четыре правила зовутся ПОДРЯД по одним и тем же точкам и одной цепочке.
// Испортив вход, первое правило молча изменило бы условия для трёх следующих,
// и разобраться в этом по результату чистки было бы невозможно.
//
// Цепочка проверяется отдельно от точек: её правила получают срезом и могли
// бы переставить элементы на месте.
func TestChainRules_DoNotModifyInput(t *testing.T) {
	pts := drive(60, 30, 10.0, 0.0, 0.003, 0)
	before := make([]geo.Point, len(pts))
	copy(before, pts)

	chain := chainOf(len(pts))
	pen := map[int]float64{}
	CheckStubs(pts, chain, pen, nil)
	CheckLateral(pts, chain, pen)
	CheckSpeedWin(pts, chain, pen)
	CheckLonely(pts, chain, pen)

	assert.Equal(t, before, pts)
	require.Equal(t, chainOf(len(pts)), chain, "цепочка тоже не меняется")
}

// Все четыре правила разом — так их и зовёт ядро. Карта штрафов заводится
// внутри цикла намеренно: её заполнение входит в стоимость прохода, а
// проходов у ядра бывает до дюжины.
func BenchmarkChainRules(b *testing.B) {
	pts := drive(20000, 30, 10.0, 0.0, 0.002, 0)
	chain := chainOf(len(pts))
	b.ReportAllocs()
	for b.Loop() {
		pen := map[int]float64{}
		CheckStubs(pts, chain, pen, nil)
		CheckLateral(pts, chain, pen)
		CheckSpeedWin(pts, chain, pen)
		CheckLonely(pts, chain, pen)
	}
}

// sin/cos — короткие имена для читаемости геометрии в тестах.
func sin(x float64) float64 { return math.Sin(x) }
func cos(x float64) float64 { return math.Cos(x) }
