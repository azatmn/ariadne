package core

import (
	"testing"
	"time"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------- BanKey

func TestBanKey_SamePlaceSameKey(t *testing.T) {
	// Ключ запрета — МЕСТО, а не секунда. Точка, повторённая трекером десять
	// раз подряд, даёт десять разных времён; при ключе по времени это были бы
	// десять отдельных запретов, и цикл не сходился бы.
	a1 := at(0, 10.0, 0.0)
	a2 := at(60, 10.00001, 0.0) // те же 1.1 м, другое время
	b := at(120, 10.5, 0.0)

	assert.Equal(t, BanKey(a1, b), BanKey(a2, b),
		"сдвиг в метр и другое время — тот же запрет")
}

// Ключ запрета огрубляет координаты до клетки, иначе дрожание приёмника
// делало бы каждый переход новым и запреты никогда не совпадали бы. Здесь
// проверяется обратное: точки ИЗ РАЗНЫХ клеток обязаны дать разные ключи —
// слипнись они, запрет одного перехода закрыл бы заодно соседний, честный.
func TestBanKey_DifferentPlacesDifferentKeys(t *testing.T) {
	a := at(0, 10.0, 0.0)
	b := at(60, 10.5, 0.0)
	far := at(60, 10.001, 0.0) // ~111 м, дальше клетки

	assert.NotEqual(t, BanKey(a, b), BanKey(a, far))
}

// Ключ учитывает направление. Запрет ставится на конкретный переход А→Б,
// а обратный Б→А бывает вполне проезжаем: на разделённой трассе выезд и
// въезд идут по разным сторонам.
func TestBanKey_IsDirectional(t *testing.T) {
	a, b := at(0, 10.0, 0.0), at(60, 10.5, 0.0)
	assert.NotEqual(t, BanKey(a, b), BanKey(b, a),
		"переход туда и обратно — разные переходы")
}

func TestBanKey_WorksAtHighLatitude(t *testing.T) {
	// На широте Мурманска градус долготы вдвое короче. Клетка обязана
	// оставаться примерно квадратной, иначе на севере запреты склеятся и
	// разные переходы получат один ключ.
	//
	// Требовать «две близкие точки всегда в одной клетке» нельзя: любая сетка
	// где-то проводит границу, и соседи по разные стороны от неё окажутся в
	// разных клетках. Проверяем то, что действительно важно, — что дальние
	// точки в одну клетку не сливаются.
	target := at(300, 34.0, 68.9)
	base := geo.Point{Time: t0, Lon: 33.0, Lat: 68.9}

	for _, d := range []float64{0.005, 0.01, 0.05} { // 200 м, 400 м, 2 км
		far := geo.Point{Time: t0.Add(time.Minute), Lon: 33.0 + d, Lat: 68.9}
		assert.NotEqual(t, BanKey(base, target), BanKey(far, target),
			"сдвиг на %.3f° долготы обязан дать другую клетку", d)
	}

	// И наоборот: два десятка точек, укладывающихся в шестнадцать метров,
	// обязаны занять не больше двух клеток — где-то между ними проходит
	// граница, и это нормально, но третьей клетке взяться неоткуда.
	//
	// Считать «сколько точек попало в ту же клетку, что первая» нельзя:
	// ответ зависит от того, где именно легла граница, то есть от случайности.
	cells := map[BanID]struct{}{}
	for k := range 20 {
		near := geo.Point{
			Time: t0.Add(time.Minute),
			Lon:  33.0 + float64(k)*0.00002, // шаг ~0.8 м, всего ~16 м
			Lat:  68.9,
		}
		cells[BanKey(near, target)] = struct{}{}
	}
	assert.LessOrEqual(t, len(cells), 2,
		"шестнадцать метров не могут растянуться на %d клеток по 50 м", len(cells))
}

// -------------------------------------------------------------- Reachable

// Опорная точка шкалы: 100 км за час — самый обычный ход фуры по трассе.
// Всё, что ниже этого, проверка обязана пропускать без разговоров.
func TestReachable_NormalDriving(t *testing.T) {
	a := at(0, 10.0, 0.0)
	b := at(3600, 10.9, 0.0) // ~100 км за час
	assert.True(t, Reachable(a, b, nil))
}

// Другой конец шкалы: 222 км за минуту. Это уже не «быстро едет», а телепорт,
// и именно такие переходы дают половину километража в сырой базе.
func TestReachable_TooFast(t *testing.T) {
	a := at(0, 10.0, 0.0)
	b := at(60, 12.0, 0.0) // 222 км за минуту
	assert.False(t, Reachable(a, b, nil))
}

// Время идёт назад. Без явной проверки отрицательная длительность даёт
// отрицательную скорость, а она проходит любой потолок сверху — то есть
// перевёрнутая пара выглядела бы самой законной в треке.
func TestReachable_BackwardsInTime(t *testing.T) {
	a := at(600, 10.0, 0.0)
	b := at(0, 10.001, 0.0)
	assert.False(t, Reachable(a, b, nil), "назад во времени не ездят")
}

func TestReachable_SameTimestampWithinSlack(t *testing.T) {
	// Пачка выгрузки буфера: время одно, точки разнесены. Допуск закрывает
	// погрешность координаты, но не спасает от разлёта в километры.
	a := at(100, 10.0, 0.0)
	assert.True(t, Reachable(a, at(100, 10.002, 0.0), nil), "222 м в допуске")
	assert.False(t, Reachable(a, at(100, 10.02, 0.0), nil), "2.2 км за ноль секунд")
}

func TestReachable_SlackHelpsShortHops(t *testing.T) {
	// Без допуска короткие переходы бьются о погрешность координаты: 300 м
	// за пять секунд формально дают 216 км/ч, хотя это дрожание на месте.
	a := at(0, 10.0, 0.0)
	b := at(5, 10.0027, 0.0) // ~300 м
	assert.True(t, Reachable(a, b, nil))
}

func TestReachable_BannedTransitionNeedsMoreTime(t *testing.T) {
	// В запретах лежит не «нельзя никогда», а МИНИМАЛЬНОЕ время, за которое
	// переход вообще проходим по дорогам. Между теми же местами машина может
	// проехать позже, когда времени хватит.
	a := at(0, 10.0, 0.0)
	quick := at(600, 10.1, 0.0) // 11 км за 10 минут
	slow := at(7200, 10.1, 0.0) // те же 11 км за два часа

	banned := map[BanID]float64{BanKey(a, quick): 3600} // нужен час

	assert.False(t, Reachable(a, quick, banned), "за десять минут не успеть")
	assert.True(t, Reachable(a, slow, banned), "за два часа — успеть")
}

// Запрет действует ТОЛЬКО на свой переход. Ядро запрещает переходы пачками
// и строит цепочку заново до дюжины раз; расплывись запрет на соседей — с
// каждым проходом отрезалось бы всё больше честного трека.
func TestReachable_BanOnOtherPlaceDoesNotApply(t *testing.T) {
	a := at(0, 10.0, 0.0)
	b := at(600, 10.1, 0.0)
	other := at(600, 11.0, 0.0)

	banned := map[BanID]float64{BanKey(a, other): 1e9}
	assert.True(t, Reachable(a, b, banned), "запрет чужого перехода не действует")
}

// Первый проход ядра идёт вообще без запретов, и карта приходит пустой или
// nil. Читать nil-карту в Go можно, но проверка не должна на этом строиться
// молча — случай зафиксирован тестом.
func TestReachable_NilAndEmptyBans(t *testing.T) {
	a, b := at(0, 10.0, 0.0), at(3600, 10.5, 0.0)
	assert.True(t, Reachable(a, b, nil))
	assert.True(t, Reachable(a, b, map[BanID]float64{}))
}

// ------------------------------------------------------------- BuildChain

// chainWeights — веса под тесты: положительные у «хороших» индексов.
func chainWeights(n int, good ...int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = -1
	}
	for _, i := range good {
		w[i] = 1
	}
	return w
}

// Вырожденные входы. Одна точка — это цепочка из одной точки, а не пустая:
// переходов в ней нет, проверять нечего, и выбрасывать её не за что.
func TestBuildChain_EmptyAndSingle(t *testing.T) {
	assert.Empty(t, BuildChain(nil, nil, nil))
	assert.Equal(t, []int{0}, BuildChain(drive(1, 30, 10, 0, 0.001, 0), []float64{1}, nil))
}

func TestBuildChain_TakesAllGoodPoints(t *testing.T) {
	// Честный трек: все точки хорошие и все достижимы — цепочка обязана
	// вобрать весь трек.
	pts := drive(20, 60, 10.0, 0.0, 0.005, 0)
	w := make([]float64, len(pts))
	for i := range w {
		w[i] = 1
	}
	got := BuildChain(pts, w, nil)
	assert.Len(t, got, len(pts))
}

func TestBuildChain_SkipsNegativeWeights(t *testing.T) {
	// Посреди честного трека — три точки с отрицательным весом. Обходить их
	// выгоднее, чем брать: цепочка набирает СУММУ весов.
	pts := drive(20, 60, 10.0, 0.0, 0.005, 0)
	w := make([]float64, len(pts))
	for i := range w {
		w[i] = 1
	}
	w[8], w[9], w[10] = -5, -5, -5

	got := BuildChain(pts, w, nil)
	assert.NotContains(t, got, 8)
	assert.NotContains(t, got, 9)
	assert.NotContains(t, got, 10)
	assert.Contains(t, got, 7)
	assert.Contains(t, got, 11)
}

func TestBuildChain_IsIncreasing(t *testing.T) {
	// Цепочка идёт вперёд по индексам: назад во времени машина не ездит.
	pts := drive(50, 30, 10.0, 0.0, 0.002, 0)
	got := BuildChain(pts, chainWeights(50, 0, 5, 10, 20, 30, 49), nil)
	for k := 1; k < len(got); k++ {
		assert.Greater(t, got[k], got[k-1], "цепочка обязана возрастать")
	}
}

func TestBuildChain_RespectsReachability(t *testing.T) {
	// Между двумя хорошими точками — телепорт: они хороши, но перехода
	// между ними не существует.
	pts := []geo.Point{
		at(0, 10.0, 0.0),
		at(30, 10.001, 0.0),
		at(60, 40.0, 0.0), // 3300 км за 30 секунд
		at(90, 40.001, 0.0),
	}
	w := []float64{1, 1, 1, 1}

	got := BuildChain(pts, w, nil)
	// Обе половины взять нельзя — переход невозможен, значит одна из них
	// остаётся за бортом.
	assert.NotContains(t, got, 2, "прыжок на 3300 км в цепочку не берём")
}

// Главная связка всего ядра: запреты, которые ставит проверка по дорогам,
// обязаны менять выбор цепочки. Сравнивается пара прогонов на одном треке —
// без запретов и с ними; равенство результатов означало бы, что проверка по
// дорогам работает вхолостую, а на глаз этого не видно.
func TestBuildChain_ObeysBans(t *testing.T) {
	pts := drive(10, 600, 10.0, 0.0, 0.05, 0) // шаг 5.6 км за 10 минут
	w := make([]float64, len(pts))
	for i := range w {
		w[i] = 1
	}

	free := BuildChain(pts, w, nil)
	require.Len(t, free, len(pts))

	// Запрещаем переход 4→5, требуя больше времени, чем прошло.
	banned := map[BanID]float64{BanKey(pts[4], pts[5]): 1e6}
	got := BuildChain(pts, w, banned)
	for k := 1; k < len(got); k++ {
		if got[k-1] == 4 {
			assert.NotEqual(t, 5, got[k], "запрещённый переход не должен использоваться")
		}
	}
}

func TestBuildChain_AnchorBeyondWindow(t *testing.T) {
	// Между двумя честными точками лежит облако мусора длиннее окна просмотра.
	// Без опоры цепочка ВЫНУЖДЕНА зацепиться за что-то внутри облака, даже
	// если у всего облака вес отрицательный: так на `4daf8725` держались точки
	// в поле у Новой Усмани — честные концы разнесены на 4934 позиции при
	// окне 2000, а прямой переход между ними законен.
	n := ChainLookback + 200
	pts := make([]geo.Point, n)
	for i := range pts {
		// облако топчется на месте, чтобы переход через него был законным
		pts[i] = at(i*60, 10.0+float64(i%3)*0.0001, 0.0)
	}
	// последняя точка — далеко, но за много часов: переход законен
	pts[n-1] = at(n*60, 10.5, 0.0)

	w := make([]float64, n)
	for i := range w {
		w[i] = -1
	}
	w[0], w[n-1] = 10, 10

	got := BuildChain(pts, w, nil)
	assert.Contains(t, got, 0, "начало обязано войти")
	assert.Contains(t, got, n-1, "конец обязан войти")
	assert.Less(t, len(got), 10,
		"через облако мусора цепочка не должна делать пересадок: взято %d точек", len(got))
}

func TestBuildChain_TakesAnyPositivePoint(t *testing.T) {
	// Точку с ЛЮБЫМ положительным весом брать выгодно, даже с крошечным:
	// цепочка набирает сумму, и 0.1 всё равно больше нуля. Это и есть причина,
	// по которой вес обязан уходить в минус, а не просто убывать к нулю —
	// иначе цепочка вбирала бы весь спуфинг, до которого дотягивается физика.
	pts := drive(6, 600, 10.0, 0.0, 0.02, 0)
	got := BuildChain(pts, []float64{5, 3, 3, 0.1, 3, 5}, nil)
	assert.Contains(t, got, 3, "положительный вес — довод взять точку")
}

func TestBuildChain_AvoidsNegativePointEvenIfPathIsLonger(t *testing.T) {
	// А вот отрицательную точку цепочка обходит, даже если из-за этого
	// приходится делать более длинный переход.
	pts := drive(6, 600, 10.0, 0.0, 0.02, 0)
	got := BuildChain(pts, []float64{5, 3, 3, -2, 3, 5}, nil)

	assert.NotContains(t, got, 3, "отрицательную точку обходим")
	assert.Contains(t, got, 2)
	assert.Contains(t, got, 4)
}

func TestBuildChain_AllNegativeKeepsSinglePoint(t *testing.T) {
	// Весь трек плох. Цепочка не может быть пустой — она обязана оставить
	// хотя бы лучшую точку, иначе дальше нечего обрабатывать.
	pts := drive(10, 60, 10.0, 0.0, 0.002, 0)
	w := chainWeights(10) // все −1
	got := BuildChain(pts, w, nil)
	assert.NotEmpty(t, got)
	assert.LessOrEqual(t, len(got), 2)
}

// Весов меньше и больше, чем точек. Оба перекоса настоящие: список весов
// собирается из ответов OSRM батчами, и отказавший батч укорачивает его.
func TestBuildChain_LengthMismatchIsSafe(t *testing.T) {
	pts := drive(5, 60, 10.0, 0.0, 0.002, 0)
	assert.NotPanics(t, func() { BuildChain(pts, []float64{1, 1}, nil) })
	assert.NotPanics(t, func() { BuildChain(pts, make([]float64, 100), nil) })
}

func TestBuildChain_TieBreakingIsStable(t *testing.T) {
	// При равных весах результат обязан быть одинаковым от прогона к прогону:
	// иначе километраж гулял бы на ровном месте.
	pts := drive(30, 60, 10.0, 0.0, 0.002, 0)
	w := make([]float64, 30)
	for i := range w {
		w[i] = 1
	}
	first := BuildChain(pts, w, nil)
	for range 5 {
		assert.Equal(t, first, BuildChain(pts, w, nil))
	}
}

// Двадцать тысяч точек с чередующимися весами — вход, на котором динамическое
// программирование не может рано отсечь ветку и перебирает по-настоящему.
// Цепочка строится заново на КАЖДОМ проходе ядра, а проходов до дюжины:
// стоимость здесь умножается на двенадцать.
func BenchmarkBuildChain(b *testing.B) {
	const n = 20000
	pts := drive(n, 30, 10.0, 0.0, 0.0005, 0)
	w := make([]float64, n)
	for i := range w {
		w[i] = float64(i%7) - 3
	}
	b.ReportAllocs()
	for b.Loop() {
		BuildChain(pts, w, nil)
	}
}
