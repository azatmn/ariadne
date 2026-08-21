package core

import (
	"testing"
	"time"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------- FindTraps

// trap строит «ловушку»: точки идут быстрым шагом, но топчутся в пятне.
// step — сколько метров между соседями, period — через сколько секунд.
func trap(n, periodSec int, stepDeg float64, lon, lat float64, startSec int) []geo.Point {
	out := make([]geo.Point, n)
	for i := range out {
		// ход туда-обратно: расстояние набегает, а с места не уходим
		k := i % 4
		var dx, dy float64
		switch k {
		case 1:
			dx = stepDeg
		case 2:
			dx, dy = stepDeg, stepDeg
		case 3:
			dy = stepDeg
		}
		out[i] = at(startSec+i*periodSec, lon+dx, lat+dy)
	}
	return out
}

func TestFindTraps_CatchesFastButStationary(t *testing.T) {
	// Ровно случай Шереметьева: координата шла шагами по 193 м каждые 7 секунд
	// (99 км/ч) и за час пятьдесят намотала 4.9 км, не выйдя из квадрата 800 м.
	// Живая фура на такой скорости уехала бы за 180 км.
	pts := trap(120, 7, 0.0018, 10.0, 0.0, 0) // шаг ~200 м, ~103 км/ч

	got := FindTraps(pts)
	assert.NotEmpty(t, got, "быстрый ход без перемещения обязан уличаться")
	assert.Greater(t, len(got), len(pts)/2, "уличается окно целиком, а не отдельные точки")
}

func TestFindTraps_HonestDrivingIsClean(t *testing.T) {
	// Настоящая езда: та же скорость, но машина уезжает.
	pts := drive(120, 7, 10.0, 0.0, 0.0018, 0)
	assert.Empty(t, FindTraps(pts), "честный ход по трассе уличать нельзя")
}

func TestFindTraps_StopIsNotTrap(t *testing.T) {
	// У стоянки скорость около нуля — это другой случай, и ловушка его не берёт.
	pts := still(120, 7, 10.0, 0.0, 0.0002, 0)
	assert.Empty(t, FindTraps(pts), "стоянка — не ловушка, там скорость нулевая")
}

func TestFindTraps_SlowCircleIsNotTrap(t *testing.T) {
	// Медленное кружение по двору: скорость ниже порога, уличать нечего —
	// маневрирование на погрузке выглядит именно так.
	pts := trap(120, 60, 0.0018, 10.0, 0.0, 0) // те же 200 м, но за минуту = 12 км/ч
	assert.Empty(t, FindTraps(pts), "на малой скорости кружение законно")
}

// Окно ловушки требует нескольких точек. На более коротком входе правило
// обязано молча ничего не найти, а не выйти за границы среза: такие треки
// приходят с трекера, который только включили.
func TestFindTraps_TooFewPoints(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4} {
		assert.NotPanics(t, func() { FindTraps(trap(n, 7, 0.0018, 10, 0, 0)) })
	}
}

func TestFindTraps_ZeroTimeSteps(t *testing.T) {
	// Выгрузка буфера: у пачки записей одно время. Делить на ноль нельзя.
	pts := make([]geo.Point, 20)
	for i := range pts {
		pts[i] = at(0, 10.0+float64(i)*0.001, 0.0)
	}
	assert.NotPanics(t, func() { FindTraps(pts) })
}

// Правило возвращает НОМЕРА точек, и по ним вызывающий ставит штрафы.
// Номер за пределами трека либо уронит ядро, либо — что хуже — оштрафует
// чужую точку, и разобраться в этом по результату будет нельзя.
func TestFindTraps_ReturnsIndicesWithinRange(t *testing.T) {
	pts := trap(200, 7, 0.0018, 10.0, 0.0, 0)
	for i := range FindTraps(pts) {
		assert.GreaterOrEqual(t, i, 0)
		assert.Less(t, i, len(pts))
	}
}

// ------------------------------------------------------------ FindIslands

func TestFindIslands_LoneTailPointIsOrphan(t *testing.T) {
	// Случай из 5f5dd0f1: трек кончается ОДНОЙ точкой, оторванной на 29 км.
	// Переход физически возможен (43 км/ч за 40 минут), точка лежит на дороге —
	// ни физика, ни снэп её не берут. Но машина не может «доехать» и на этом
	// кончиться: внутри такого куска нет никакой езды.
	var pts []geo.Point
	pts = append(pts, drive(20, 60, 10.0, 0.0, 0.005, 0)...)
	pts = append(pts, at(3000, 10.4, 0.0)) // 29 км в сторону, одна точка

	got := FindIslands(pts)
	require.Len(t, got, 1)
	assert.Contains(t, got, len(pts)-1)
}

func TestFindIslands_RealVisitSurvives(t *testing.T) {
	// Настоящий заезд оставляет следы: точек много, времени прошло, внутри
	// есть движение. Достаточно ОДНОЙ из трёх мерок, чтобы не быть огрызком.
	var pts []geo.Point
	pts = append(pts, drive(20, 60, 10.0, 0.0, 0.005, 0)...)
	pts = append(pts, drive(10, 120, 10.4, 0.0, 0.003, 3000)...) // заезд с ездой внутри

	assert.Empty(t, FindIslands(pts), "заезд с движением внутри — не огрызок")
}

func TestFindIslands_LongStayIsNotOrphan(t *testing.T) {
	// Точек мало и внутри не ездили, зато простояли час — это стоянка,
	// а не огрызок.
	var pts []geo.Point
	pts = append(pts, drive(20, 60, 10.0, 0.0, 0.005, 0)...)
	pts = append(pts, still(3, 1800, 10.4, 0.0, 0.0001, 3000)...) // час на месте

	assert.Empty(t, FindIslands(pts), "долгое стояние — не огрызок")
}

// Третья мерка по отдельности: точек много, хотя времени прошло мало и
// внутри не ездили. Одной уцелевшей мерки достаточно — иначе плотная пачка
// с настоящей погрузки уехала бы в мусор.
func TestFindIslands_ManyPointsIsNotOrphan(t *testing.T) {
	var pts []geo.Point
	pts = append(pts, drive(20, 60, 10.0, 0.0, 0.005, 0)...)
	pts = append(pts, still(8, 5, 10.4, 0.0, 0.0001, 3000)...) // 8 точек за 35 с

	assert.Empty(t, FindIslands(pts), "кусок из восьми точек огрызком не считаем")
}

func TestFindIslands_IslandInTheMiddle(t *testing.T) {
	// Огрызок между двумя разрывами, посреди трека.
	var pts []geo.Point
	pts = append(pts, drive(15, 60, 10.0, 0.0, 0.005, 0)...)
	island := len(pts)
	pts = append(pts, still(3, 10, 10.5, 0.0, 0.0001, 2000)...) // 3 точки, 20 с
	pts = append(pts, drive(15, 60, 11.0, 0.0, 0.005, 4000)...)

	got := FindIslands(pts)
	require.Len(t, got, 3)
	for i := island; i < island+3; i++ {
		assert.Contains(t, got, i)
	}
}

// Зеркальная проверка ко всем предыдущим: без разрывов резать нечего.
// Без неё правило, объявляющее огрызком что попало, прошло бы тесты.
func TestFindIslands_ContinuousTrackHasNoIslands(t *testing.T) {
	pts := drive(100, 60, 10.0, 0.0, 0.005, 0) // шаг 556 м, разрывов нет
	assert.Empty(t, FindIslands(pts))
}

func TestFindIslands_ShortTrackIsUntouched(t *testing.T) {
	// На треке короче трёх точек резать нечего.
	for _, n := range []int{0, 1, 2} {
		assert.Empty(t, FindIslands(drive(n, 60, 10, 0, 0.005, 0)))
	}
}

func TestFindIslands_WholeTrackIsOneSmallIsland(t *testing.T) {
	// Весь трек — три точки за десять секунд. Резать не по чему, но и
	// огрызком целый трек объявлять нельзя: выбрасывать станет нечего.
	pts := still(3, 5, 10.0, 0.0, 0.0001, 0)
	got := FindIslands(pts)
	assert.Len(t, got, 3, "формально это огрызок, и правило обязано быть последовательным")
}

func TestFindIslands_AllThreeMeasuresMustBeSmall(t *testing.T) {
	// Проверяем каждую мерку по отдельности: достаточно одной, чтобы уцелеть.
	base := drive(20, 60, 10.0, 0.0, 0.005, 0)

	cases := []struct {
		name string
		tail []geo.Point
		want bool // огрызок?
	}{
		{"мало всего", still(2, 5, 10.5, 0.0, 0.0001, 3000), true},
		{"много точек", still(7, 5, 10.5, 0.0, 0.0001, 3000), false},
		{"долго стоял", still(2, 400, 10.5, 0.0, 0.0001, 3000), false},
		{"ездил внутри", drive(3, 20, 10.5, 0.0, 0.006, 3000), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pts := append(append([]geo.Point{}, base...), c.tail...)
			got := FindIslands(pts)
			if c.want {
				assert.NotEmpty(t, got)
			} else {
				assert.Empty(t, got)
			}
		})
	}
}

// --------------------------------------------------------------- общее

func TestRules_DoNotModifyInput(t *testing.T) {
	// Правила только читают трек. Порча входа сломала бы все последующие
	// правила, которые считаются по тем же точкам.
	pts := trap(60, 7, 0.0018, 10.0, 0.0, 0)
	before := make([]geo.Point, len(pts))
	copy(before, pts)

	FindTraps(pts)
	FindIslands(pts)

	assert.Equal(t, before, pts)
}

// Оба замера — на потолке входа (50 000 точек). Правила зовутся на каждом
// проходе ядра, а проходов бывает до дюжины: лишний множитель здесь съедает
// бюджет задачи целиком.
func BenchmarkFindTraps(b *testing.B) {
	pts := drive(50000, 5, 10.0, 0.0, 0.0005, 0)
	b.ReportAllocs()
	for b.Loop() {
		FindTraps(pts)
	}
}

func BenchmarkFindIslands(b *testing.B) {
	pts := drive(50000, 5, 10.0, 0.0, 0.0005, 0)
	b.ReportAllocs()
	for b.Loop() {
		FindIslands(pts)
	}
}

// ------------------------------------------------- защита от негодного входа
//
// Эти случаи в настоящих данных не встречаются, но приходят от битого трекера
// и от собственных ошибок в вызывающем коде. Молчаливая порча памяти хуже
// любого неверного ответа, поэтому границы проверяются явно.

func TestTrustedStop_InvalidRangeIsRejected(t *testing.T) {
	pts := still(10, 120, 10.0, 0.0, 0.0002, 0)
	bad := []StopRange{
		{-1, 5},             // начало за левой границей
		{0, len(pts)},       // конец за правой
		{5, 2},              // вывернут
		{0, len(pts) + 100}, // далеко за правой
	}
	for _, s := range bad {
		assert.False(t, TrustedStop(pts, s, 10, true), "интервал %v негоден", s)
	}
}

func TestSpanMeters_InvalidRangeIsZero(t *testing.T) {
	pts := still(10, 120, 10.0, 0.0, 0.0002, 0)
	for _, s := range []StopRange{{-1, 5}, {0, len(pts)}, {7, 3}} {
		assert.Zero(t, spanMeters(pts, s), "интервал %v негоден", s)
	}
}

func TestExitPlausible_SubSecondSteps(t *testing.T) {
	// Точки с интервалом меньше секунды: делить на такое время нельзя, иначе
	// любой шаг превращается в тысячи километров в час. Секунда — нижняя
	// планка, как в прототипе.
	pts := []geo.Point{
		at(0, 10.0, 0.0),
		{Time: t0.Add(300 * time.Millisecond), Lon: 10.004, Lat: 0.0}, // 445 м
	}
	assert.NotPanics(t, func() { exitPlausible(pts, 0) })
}

func TestFindDual_BackwardTimeIsIgnored(t *testing.T) {
	// Время идёт назад: так выглядит переупорядоченная пачка выгрузки буфера.
	// Судить о скорости по отрицательному времени нельзя.
	var pts []geo.Point
	for k := range 30 {
		p := town
		if k%2 == 1 {
			p = poset
		}
		// каждая следующая точка на минуту РАНЬШЕ предыдущей
		pts = append(pts, at(10000-k*60, p.lon, p.lat))
	}
	assert.NotPanics(t, func() { FindDual(pts) })
}

func TestFindDual_JumpBetweenIdenticalPlaces(t *testing.T) {
	// Прыжки есть, но «два места» оказались одним и тем же: эпизод разбирать
	// нечего, и правило обязано пройти мимо, а не делить точку саму с собой.
	var pts []geo.Point
	sec := 0
	for range 20 {
		pts = append(pts, at(sec, town.lon, town.lat))
		sec += 30
		pts = append(pts, at(sec, town.lon+0.0001, town.lat))
		sec += 30
	}
	assert.Empty(t, FindDual(pts))
}

// Пустой вход — рабочий случай, а не ошибка: медиану зовут на окне, которое
// у края трека может не собрать ни одного соседа.
func TestMedianInPlace_Empty(t *testing.T) {
	assert.Zero(t, medianInPlace(nil))
	assert.Zero(t, medianInPlace([]float64{}))
}
