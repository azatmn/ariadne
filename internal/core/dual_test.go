package core

import (
	"testing"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Два источника в одном треке.
//
// Найдено на `ab681145` (Саратов, 14–15.07): трек мечется между Сторожевкой и
// Саратовом 54 раза, прыжки по 20 км за секунды. Это не глюк отдельных точек,
// а ДВА ИСТОЧНИКА, пишущих в один трек. Прежние правила бессильны: сами прыжки
// запрещаются проверкой достижимости, но цепочка после этого просто СШИВАЕТ
// куски обоих потоков переходами, которые выглядят законно — 20 км за 37 минут
// это нормальная езда. Каждый отдельный шаг безупречен, ложна вся конструкция.

// place — координаты места, вокруг которого пишет источник.
type place struct{ lon, lat float64 }

var (
	// Два места в двадцати километрах друг от друга.
	town  = place{10.00, 0.0}
	poset = place{10.18, 0.0} // ~20 км восточнее
)

// weave строит трек, где два источника пишут по очереди блоками.
// blockA/blockB — по сколько точек подряд даёт каждый источник,
// stepSec — период записи, blocks — сколько раз чередуются.
func weave(a, b place, blockA, blockB, stepSec, blocks int) []geo.Point {
	var out []geo.Point
	sec := 0
	jitter := 0.0002 // дрожание в пределах места, ~22 м
	for k := range blocks {
		for i := range blockA {
			sign := 1.0
			if i%2 == 1 {
				sign = -1
			}
			out = append(out, at(sec, a.lon+sign*jitter, a.lat+sign*jitter))
			sec += stepSec
		}
		for i := range blockB {
			sign := 1.0
			if i%2 == 1 {
				sign = -1
			}
			out = append(out, at(sec, b.lon+sign*jitter, b.lat+sign*jitter))
			sec += stepSec
		}
		_ = k
	}
	return out
}

// ------------------------------------------------------------- FindDual

func TestFindDual_CatchesWeakerStream(t *testing.T) {
	// Один источник даёт длинные блоки, второй — короткие вспышки.
	// Машина физически в одном месте, поэтому настоящий поток покрывает время
	// эпизода сплошь, а подделка вспыхивает и гаснет.
	pts := weave(town, poset, 20, 2, 30, 8)

	got := FindDual(pts)
	require.NotEmpty(t, got, "мечущийся трек обязан уличаться")

	// Уличается слабый поток — тот, что в посёлке.
	inPoset, inTown := 0, 0
	for i := range got {
		if geo.Haversine(pts[i], geo.Point{Lon: poset.lon, Lat: poset.lat}) < DualNearM {
			inPoset++
		}
		if geo.Haversine(pts[i], geo.Point{Lon: town.lon, Lat: town.lat}) < DualNearM {
			inTown++
		}
	}
	assert.Positive(t, inPoset, "слабый поток обязан быть уличён")
	assert.Zero(t, inTown, "сильный поток трогать нельзя")
}

func TestFindDual_HonestTrackIsClean(t *testing.T) {
	// Честная езда: точки идут подряд, прыжков нет вовсе.
	pts := drive(300, 30, 10.0, 0.0, 0.002, 0)
	assert.Empty(t, FindDual(pts))
}

func TestFindDual_SingleTripBetweenTownsIsClean(t *testing.T) {
	// Машина съездила из города в посёлок и обратно — по одному разу.
	// Переключений мало, это переезд, а не два источника.
	var pts []geo.Point
	pts = append(pts, still(20, 30, town.lon, town.lat, 0.0002, 0)...)
	pts = append(pts, drive(30, 60, 10.01, 0.0, 0.006, 700)...)
	pts = append(pts, still(20, 30, poset.lon, poset.lat, 0.0002, 2600)...)

	assert.Empty(t, FindDual(pts), "один переезд — не метание")
}

func TestFindDual_SlowJumpsAreNotCounted(t *testing.T) {
	// Те же два места, но переходы занимают часы: это законные поездки
	// туда-обратно, а не мгновенные скачки.
	pts := weave(town, poset, 20, 2, 3600, 8) // шаг час
	assert.Empty(t, FindDual(pts), "медленные переходы не улика")
}

func TestFindDual_ShortJumpsAreNotCounted(t *testing.T) {
	// Прыжки быстрые, но короткие — дрожание на месте, а не два источника.
	near := place{10.001, 0.0} // ~110 м
	pts := weave(town, near, 20, 2, 30, 8)
	assert.Empty(t, FindDual(pts), "дрожание в сотню метров не считается")
}

func TestFindDual_TooFewSwitches(t *testing.T) {
	// Меньше шести переключений — случайность, а не система.
	pts := weave(town, poset, 20, 2, 30, 2)
	assert.Empty(t, FindDual(pts))
}

func TestFindDual_TooShortTrack(t *testing.T) {
	for _, n := range []int{0, 1, 5, 19} {
		assert.Empty(t, FindDual(drive(n, 30, 10, 0, 0.002, 0)))
	}
}

func TestFindDual_EqualCoverageIsNotJudged(t *testing.T) {
	// Оба места держатся одинаково — разделения нет, и гадать нельзя.
	// Лучше пропустить, чем угадывать: на честных треках таких эпизодов нет.
	pts := weave(town, poset, 10, 10, 30, 8)

	got := FindDual(pts)
	// Уличать по признаку «слабее вдвое» тут нечего; правило либо молчит,
	// либо срабатывает по другому основанию (много мест), которого здесь нет.
	assert.Empty(t, got, "при равном покрытии двух мест правило обязано молчать")
}

func TestFindDual_ManyPlacesWithNoLeaderKillsEpisode(t *testing.T) {
	// Случай `ab681145`: за 47 часов машина «была» в пяти местах вокруг
	// Саратова одновременно. Покрытие лучшего 25 %, второго 15 % — правило на
	// два места молчит. Но альтернативного места, где машина могла стоять, в
	// данных нет вовсе: достоверных данных за период нет, и показывать
	// «лучшее из ложных» нельзя — это выдуманный километраж.
	places := []place{
		{10.00, 0.0}, {10.18, 0.0}, {10.36, 0.0}, {10.54, 0.0}, {10.72, 0.0},
	}
	// Блоки по две минуты, как на настоящем треке: полный круг по пяти местам
	// занимает десять минут, и каждое место молчит между визитами дольше, чем
	// длится разрыв непрерывного присутствия. Сделай круг быстрее — и каждое
	// место выглядело бы присутствующим сплошь, что и есть настоящее поведение
	// одной машины.
	var pts []geo.Point
	sec := 0
	for range 12 { // по кругу
		for _, p := range places {
			for i := range 3 {
				sign := 1.0
				if i%2 == 1 {
					sign = -1
				}
				pts = append(pts, at(sec, p.lon+sign*0.0002, p.lat+sign*0.0002))
				sec += 60
			}
		}
	}

	got := FindDual(pts)
	assert.NotEmpty(t, got, "пять мест одновременно и ни одного убедительного")
	assert.Greater(t, len(got), len(pts)/2,
		"при отсутствии лидера выбрасывается весь период")
}

func TestFindDual_ReturnsValidIndices(t *testing.T) {
	pts := weave(town, poset, 20, 2, 30, 8)
	for i := range FindDual(pts) {
		assert.GreaterOrEqual(t, i, 0)
		assert.Less(t, i, len(pts))
	}
}

func TestFindDual_DoesNotModifyInput(t *testing.T) {
	pts := weave(town, poset, 20, 2, 30, 8)
	before := make([]geo.Point, len(pts))
	copy(before, pts)
	FindDual(pts)
	assert.Equal(t, before, pts)
}

func TestFindDual_ZeroTimeJumps(t *testing.T) {
	// Выгрузка буфера: у пачки одно время, и прыжок «за ноль секунд» даёт
	// деление на ноль, если не подстраховаться.
	var pts []geo.Point
	for k := range 30 {
		p := town
		if k%2 == 1 {
			p = poset
		}
		pts = append(pts, at(0, p.lon, p.lat))
	}
	assert.NotPanics(t, func() { FindDual(pts) })
}

func BenchmarkFindDual(b *testing.B) {
	pts := weave(town, poset, 20, 2, 30, 400) // ~17 тысяч точек
	b.ReportAllocs()
	for b.Loop() {
		FindDual(pts)
	}
}
