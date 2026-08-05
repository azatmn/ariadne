package core

import (
	"testing"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Тесты амнистии. Правило отвечает на вопрос «кого из попавших под штраф за
// эпизод оправдываем напрямую», и у него два независимых довода: цепочка
// доверия от заведомо чистой точки и длинная связная серия на асфальте.
// Тесты разделены по доводам, потому что путать их нельзя: первый требует
// чистого соседа, второй работает как раз там, где чистых соседей не осталось.

// amnTrack — n точек с шагом 10 секунд и ~100 м, то есть 36 км/ч.
// Такой шаг заведомо проходит проверку связи, поэтому в тестах, где связь не
// проверяется, она не мешает.
func amnTrack(n int) []geo.Point {
	return drive(n, 10, 10, 0, 0.0009, 0)
}

// snapAll — снэпы одной величины на все точки.
func snapAll(n int, v float64) ([]float64, []bool) {
	snaps := make([]float64, n)
	ok := make([]bool, n)
	for i := range n {
		snaps[i], ok[i] = v, true
	}
	return snaps, ok
}

// set — множество индексов.
func set(ii ...int) map[int]struct{} {
	out := make(map[int]struct{}, len(ii))
	for _, i := range ii {
		out[i] = struct{}{}
	}
	return out
}

// keys — отсортированное представление множества для внятного сообщения теста.
func keys(m map[int]struct{}) []int {
	out := make([]int, 0, len(m))
	for i := range m {
		out = append(out, i)
	}
	return out
}

// ---------------------------------------------------------- вырожденный вход

func TestFindAmnesty_DegenerateInput(t *testing.T) {
	snaps, ok := snapAll(5, 5)
	pts := amnTrack(5)

	cases := []struct {
		name           string
		pts            []geo.Point
		snaps          []float64
		ok             []bool
		spread, bad    map[int]struct{}
		decoy          []float64
		wantEmptyLabel string
	}{
		{"нет точек", nil, nil, nil, nil, nil, nil, "пустой трек"},
		{"никого не подозреваем", pts, snaps, ok, nil, nil, nil, "без подозрений оправдывать некого"},
		{"подозреваем всех, но их мало", pts, snaps, ok, set(0, 1, 2, 3, 4), nil, nil, "серия короче порога"},
		{"нет снэпов вовсе", pts, nil, nil, set(0, 1, 2, 3, 4), nil, nil, "без снэпа поручиться нечем"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FindAmnesty(c.pts, c.snaps, c.ok, c.spread, c.bad, c.decoy)
			assert.Empty(t, got, c.wantEmptyLabel)
		})
	}
}

// ------------------------------------------------ довод 1: цепочка доверия

func TestFindAmnesty_TrustFlowsForward(t *testing.T) {
	// Точка 0 вне подозрений — от неё доверие идёт вперёд по времени,
	// пока переходы плотные, а точки на дороге.
	pts := amnTrack(5)
	snaps, ok := snapAll(5, 5)

	got := FindAmnesty(pts, snaps, ok, set(1, 2, 3, 4), nil, nil)
	assert.Equal(t, set(1, 2, 3, 4), got, "получили %v", keys(got))
}

func TestFindAmnesty_TrustFlowsBackward(t *testing.T) {
	// Зеркальный случай: чистая точка ПОСЛЕ подозреваемых. Без обратного
	// прохода все они остались бы без адвоката — а глюк одинаково часто
	// стоит и до честной езды, и после.
	pts := amnTrack(5)
	snaps, ok := snapAll(5, 5)

	got := FindAmnesty(pts, snaps, ok, set(0, 1, 2, 3), nil, nil)
	assert.Equal(t, set(0, 1, 2, 3), got, "получили %v", keys(got))
}

func TestFindAmnesty_ConvictedNeverAmnestied(t *testing.T) {
	// Уличённого лично не оправдывают, и он же рвёт цепочку доверия:
	// сквозь глюк ручаться нельзя.
	//
	// Точка 0 чиста, точка 4 чиста, уличена точка 2. Значит 1 спасает
	// прямой проход, 3 — обратный, а 2 не спасает никто.
	pts := amnTrack(5)
	snaps, ok := snapAll(5, 5)

	got := FindAmnesty(pts, snaps, ok, set(1, 2, 3), set(2), nil)
	assert.Equal(t, set(1, 3), got, "получили %v", keys(got))
}

func TestFindAmnesty_ConvictedBlocksEverythingBehindIt(t *testing.T) {
	// Уличённая точка не пропускает доверие дальше себя: за ней цепочка
	// начинается заново, а чистого соседа там уже нет.
	pts := amnTrack(6)
	snaps, ok := snapAll(6, 5)

	// Чист только 0. Уличена 2. Значит спасается только 1.
	got := FindAmnesty(pts, snaps, ok, set(1, 2, 3, 4, 5), set(2), nil)
	assert.Equal(t, set(1), got, "получили %v", keys(got))
}

func TestFindAmnesty_TimeGapBreaksTrust(t *testing.T) {
	// Разрыв дольше AmnestyGap рвёт цепочку: через дыру связи ручаться
	// нельзя, за ней машина могла оказаться где угодно.
	gap := int(AmnestyGap.Seconds()) + 1
	pts := []geo.Point{
		at(0, 10, 0),
		at(gap, 10.0009, 0),
		at(gap+10, 10.0018, 0),
	}
	snaps, ok := snapAll(3, 5)

	got := FindAmnesty(pts, snaps, ok, set(1, 2), nil, nil)
	assert.Empty(t, got, "получили %v", keys(got))
}

func TestFindAmnesty_GapBoundary(t *testing.T) {
	// Ровно AmnestyGap — ещё ручаемся, на секунду больше — уже нет.
	gap := int(AmnestyGap.Seconds())
	snaps, ok := snapAll(2, 5)

	within := []geo.Point{at(0, 10, 0), at(gap, 10.0009, 0)}
	assert.Equal(t, set(1), FindAmnesty(within, snaps, ok, set(1), nil, nil),
		"на самой границе доверие ещё идёт")

	beyond := []geo.Point{at(0, 10, 0), at(gap+1, 10.0009, 0)}
	assert.Empty(t, FindAmnesty(beyond, snaps, ok, set(1), nil, nil),
		"за границей уже нет")
}

func TestFindAmnesty_FarFromRoadBreaksTrust(t *testing.T) {
	// Точка вдали от дороги не оправдывается и цепочку обрывает: довод
	// амнистии в том, что точка лежит на асфальте.
	pts := amnTrack(4)
	snaps, ok := snapAll(4, 5)
	snaps[1] = AmnestySnapM + 1

	got := FindAmnesty(pts, snaps, ok, set(1, 2, 3), nil, nil)
	assert.Empty(t, got, "плохая точка обрывает цепочку, за ней спасать некому: %v", keys(got))
}

func TestFindAmnesty_SnapBoundary(t *testing.T) {
	// Ровно на пороге снэпа — ещё на дороге.
	pts := amnTrack(2)
	snaps, ok := snapAll(2, 5)

	snaps[1] = AmnestySnapM
	assert.Equal(t, set(1), FindAmnesty(pts, snaps, ok, set(1), nil, nil),
		"порог включительный")

	snaps[1] = AmnestySnapM + 0.001
	assert.Empty(t, FindAmnesty(pts, snaps, ok, set(1), nil, nil),
		"за порогом — нет")
}

func TestFindAmnesty_MissingSnapBreaksTrust(t *testing.T) {
	// Снэпа нет вовсе (маршрутизатор не ответил) — ручаться не за что.
	pts := amnTrack(3)
	snaps, ok := snapAll(3, 5)
	ok[1] = false

	got := FindAmnesty(pts, snaps, ok, set(1, 2), nil, nil)
	assert.Empty(t, got, "получили %v", keys(got))
}

func TestFindAmnesty_ShortSnapsSlice(t *testing.T) {
	// Снэпов меньше, чем точек: за хвост ручаться нечем, но падать нельзя.
	pts := amnTrack(4)
	snaps, ok := snapAll(2, 5)

	got := FindAmnesty(pts, snaps, ok, set(1, 2, 3), nil, nil)
	assert.Equal(t, set(1), got, "спасается только та, у которой снэп есть: %v", keys(got))
}

func TestFindAmnesty_ImpossibleStepBreaksTrust(t *testing.T) {
	// Переход, который не проехать: доверие через телепорт не идёт, даже
	// если обе точки лежат на дороге.
	pts := []geo.Point{
		at(0, 10, 0),
		at(10, 20, 0), // ~1113 км за 10 секунд
	}
	snaps, ok := snapAll(2, 5)

	got := FindAmnesty(pts, snaps, ok, set(1), nil, nil)
	assert.Empty(t, got, "получили %v", keys(got))
}

func TestFindAmnesty_SlackCoversJitter(t *testing.T) {
	// Допуск на погрешность координаты обязателен: 300 метров за секунду
	// формально дают 1080 км/ч, а на деле это дрожание стоящей машины.
	// Без допуска цепочка доверия рвалась бы на каждой стоянке.
	pts := []geo.Point{
		at(0, 10, 0),
		at(1, 10+300/111320.0, 0),
	}
	snaps, ok := snapAll(2, 5)

	got := FindAmnesty(pts, snaps, ok, set(1), nil, nil)
	assert.Equal(t, set(1), got, "дрожание не должно рвать доверие")

	// А вот вдвое дальше допуска уже не проходит.
	far := []geo.Point{at(0, 10, 0), at(1, 10+700/111320.0, 0)}
	assert.Empty(t, FindAmnesty(far, snaps, ok, set(1), nil, nil))
}

// ------------------------------------------- довод 2: серия ручается за себя

func TestFindAmnesty_RunVouchesForItself(t *testing.T) {
	// Ровно AmnestyRunMin точек подряд, все на асфальте, все связаны — и ни
	// одной чистой точки рядом. Первый адвокат бессилен по построению,
	// спасает только второй.
	n := AmnestyRunMin
	pts := amnTrack(n)
	snaps, ok := snapAll(n, 5)

	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	got := FindAmnesty(pts, snaps, ok, set(all...), nil, nil)
	assert.Len(t, got, n, "серия на пороге оправдывается целиком: %v", keys(got))
}

func TestFindAmnesty_RunOneShortOfThreshold(t *testing.T) {
	// На одну точку короче порога — не оправдывается. Порог включительный,
	// и граница обязана быть именно здесь.
	n := AmnestyRunMin - 1
	pts := amnTrack(n)
	snaps, ok := snapAll(n, 5)

	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	assert.Empty(t, FindAmnesty(pts, snaps, ok, set(all...), nil, nil))
}

func TestFindAmnesty_RunBrokenByGap(t *testing.T) {
	// Разрыв посередине делит серию надвое, и обе половины короче порога.
	// Считается именно СВЯЗНАЯ серия, а не «сколько всего подозреваемых».
	// Половины намеренно по AmnestyRunMin-1 точек: если бы порог считался от
	// всей толпы подозреваемых, а не от связной серии, тест бы это поймал.
	half := AmnestyRunMin - 1
	n := 2 * half
	pts := amnTrack(n)
	// Сдвигаем вторую половину далеко вперёд по времени.
	shift := AmnestyGap + AmnestyGap
	for i := half; i < n; i++ {
		pts[i].Time = pts[i].Time.Add(shift)
	}
	snaps, ok := snapAll(n, 5)

	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	assert.Empty(t, FindAmnesty(pts, snaps, ok, set(all...), nil, nil),
		"две серии по %d короче порога %d", half, AmnestyRunMin)
}

func TestFindAmnesty_RunBrokenByConvicted(t *testing.T) {
	// Уличённая точка внутри серии рвёт её: связность подделки не довод,
	// она как раз связная и на дорогах.
	// Длина подобрана так, чтобы обе половины были на одну точку короче
	// порога: иначе тест прошёл бы и на неверной реализации, где серия
	// считается без учёта уличённых.
	n := 2*AmnestyRunMin - 1
	mid := n / 2
	pts := amnTrack(n)
	snaps, ok := snapAll(n, 5)

	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	got := FindAmnesty(pts, snaps, ok, set(all...), set(mid), nil)
	assert.Empty(t, got, "обе половины по %d короче порога %d: %v", mid, AmnestyRunMin, keys(got))
}

func TestFindAmnesty_RunBrokenByBadSnap(t *testing.T) {
	// Точка вне асфальта тоже рвёт серию — довод второго адвоката в том,
	// что серия идёт по дорогам, а не просто существует.
	n := 2*AmnestyRunMin - 1
	pts := amnTrack(n)
	snaps, ok := snapAll(n, 5)
	snaps[n/2] = AmnestySnapM + 1

	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	assert.Empty(t, FindAmnesty(pts, snaps, ok, set(all...), nil, nil))
}

func TestFindAmnesty_RunRefusedInsideDecoy(t *testing.T) {
	// Приманка — прямая улика МЕСТА, и связность её не перебивает: длинная
	// серия, топчущаяся внутри приманочной клетки, оправданию не подлежит.
	// Достаточно ОДНОЙ приманочной точки, чтобы отказать всей серии.
	n := AmnestyRunMin + 5
	pts := amnTrack(n)
	snaps, ok := snapAll(n, 5)
	decoy := make([]float64, n)
	decoy[n/2] = 0.4

	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	assert.Empty(t, FindAmnesty(pts, snaps, ok, set(all...), nil, decoy),
		"серия с приманкой внутри не оправдывается")

	// Без приманки та же серия оправдывается — значит отказал именно этот довод.
	assert.Len(t, FindAmnesty(pts, snaps, ok, set(all...), nil, make([]float64, n)), n)
}

func TestFindAmnesty_RunIgnoresPointsOutsideSuspicion(t *testing.T) {
	// Второй адвокат считает серию только из ПОДОЗРЕВАЕМЫХ точек. Чистая
	// точка внутри — не продолжение серии, а её разрыв: за соседей она
	// ручается сама, первым доводом, и второму делать там нечего.
	n := 2*AmnestyRunMin + 1
	pts := amnTrack(n)
	snaps, ok := snapAll(n, 5)

	mid := n / 2
	all := make([]int, 0, n)
	for i := range n {
		if i != mid {
			all = append(all, i)
		}
	}
	got := FindAmnesty(pts, snaps, ok, set(all...), nil, nil)

	// Чистая точка в середине спасает всех первым доводом — и это правильно.
	// Проверяем именно то, что её саму в ответ не записали.
	assert.NotContains(t, got, mid, "чистую точку оправдывать не от чего")
	assert.Len(t, got, n-1)
}

func TestFindAmnesty_ShortDecoySlice(t *testing.T) {
	// Данных о приманках может не быть вовсе (список не собран) или быть
	// короче трека — правило обязано работать, а не падать.
	n := AmnestyRunMin
	pts := amnTrack(n)
	snaps, ok := snapAll(n, 5)

	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	assert.Len(t, FindAmnesty(pts, snaps, ok, set(all...), nil, []float64{0, 0}), n,
		"короткий список приманок не должен мешать")
}

// ------------------------------------------------------- два довода вместе

func TestFindAmnesty_AdvocatesCombine(t *testing.T) {
	// Итог — объединение доводов, а не выбор одного. Здесь чистая точка 0
	// спасает соседа 1 первым доводом, а длинная серия в конце — сама себя
	// вторым, и между ними уличённая точка, через которую доверие не идёт.
	head := 3
	convicted := head
	runStart := head + 1
	n := runStart + AmnestyRunMin

	pts := amnTrack(n)
	snaps, ok := snapAll(n, 5)

	spread := make([]int, 0, n)
	for i := 1; i < n; i++ {
		spread = append(spread, i)
	}

	got := FindAmnesty(pts, snaps, ok, set(spread...), set(convicted), nil)

	assert.Contains(t, got, 1, "сосед чистой точки спасён первым доводом")
	assert.NotContains(t, got, convicted, "уличённого не оправдываем")
	for i := runStart; i < n; i++ {
		assert.Contains(t, got, i, "точка %d спасена серией", i)
	}
}

func TestFindAmnesty_DoesNotMutateInput(t *testing.T) {
	// Правило обязано быть чистым: наверху те же множества идут дальше в
	// штрафы, и тихая правка сломала бы их молча.
	n := AmnestyRunMin
	pts := amnTrack(n)
	snaps, ok := snapAll(n, 5)

	all := make([]int, n)
	for i := range all {
		all[i] = i
	}
	spread, bad := set(all...), set()
	decoy := make([]float64, n)

	require.Len(t, spread, n)
	FindAmnesty(pts, snaps, ok, spread, bad, decoy)

	assert.Len(t, spread, n, "множество подозреваемых не должно меняться")
	assert.Empty(t, bad, "множество уличённых не должно меняться")
	for i, d := range decoy {
		assert.Zero(t, d, "приманки не должны меняться, позиция %d", i)
	}
}

// Амнистия проходит трек трижды: вперёд, назад и серией. Каждый проход
// линейный, но зовётся она на полном треке, поэтому цена важна.
func BenchmarkFindAmnesty(b *testing.B) {
	const n = 50000
	pts := drive(n, 10, 10.0, 0.0, 0.0009, 0)
	snaps, ok := snapAll(n, 5)

	// Половина точек под подозрением — примерно как на грязном треке.
	spread := make(map[int]struct{}, n/2)
	for i := range n {
		if i%2 == 0 {
			spread[i] = struct{}{}
		}
	}
	decoy := make([]float64, n)

	b.ReportAllocs()
	for b.Loop() {
		FindAmnesty(pts, snaps, ok, spread, nil, decoy)
	}
}
