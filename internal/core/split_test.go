package core

import (
	"slices"
	"testing"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Раздвоение — промежуток, в котором сигнал идёт сразу из нескольких мест.
//
// Обобщение правила двух потоков: без опоры на быстрые прыжки и без ограничения
// двумя местами. На `ab681145` двух мест мало — там с 14.07 13:16 и двадцать
// часов подряд ЧЕТЫРЕ источника пишут по очереди блоками ровно по четыре
// минуты, и внутри блока всё безупречно: снэпы 0–20 м, скорости 60–110 км/ч.

// spot — место, в котором сидит источник.
type spot struct{ lon, lat float64 }

// rotate строит трек, где источники пишут по очереди блоками.
// blockPts — сколько точек подряд даёт источник, stepSec — период записи.
func rotate(spots []spot, blockPts, stepSec, rounds int) []geo.Point {
	var out []geo.Point
	sec := 0
	for range rounds {
		for _, s := range spots {
			for i := range blockPts {
				sign := 1.0
				if i%2 == 1 {
					sign = -1
				}
				out = append(out, at(sec, s.lon+sign*0.0002, s.lat+sign*0.0002))
				sec += stepSec
			}
		}
	}
	return out
}

var (
	// Три места в пределах городской агломерации: 8 и 16 км от первого.
	spotA = spot{10.00, 0.0}
	spotB = spot{10.07, 0.0} // ~7.8 км
	spotC = spot{10.14, 0.0} // ~15.6 км
)

// ------------------------------------------------------------- FindSplit

// Главная зеркальная проверка правила. Пятьсот точек ровной езды: мест,
// соперничающих за одно время, тут нет ни одного, и раздвоение обязано быть
// пустым. Правило, объявляющее раздвоением всякий перегон, съело бы трек
// целиком — а найти виноватого оно и на таком входе «сумеет».
func TestFindSplit_HonestTrackIsClean(t *testing.T) {
	pts := drive(500, 30, 10.0, 0.0, 0.002, 0)
	assert.Empty(t, FindSplit(pts), "монотонная езда раздвоением не является")
}

// Порог по длине: на треке короче двадцати точек слотов не набрать, и
// «несколько мест за один промежуток» превращается в обычный разброс
// приёмника у светофора.
func TestFindSplit_TooShortTrack(t *testing.T) {
	for _, n := range []int{0, 1, 10, 19} {
		assert.Empty(t, FindSplit(drive(n, 30, 10, 0, 0.002, 0)))
	}
}

func TestFindSplit_CatchesRotationBetweenPlaces(t *testing.T) {
	// Три источника пишут по кругу блоками по четыре минуты, шесть часов
	// подряд. Ни у одного нет убедительного превосходства — значит достоверных
	// данных за период нет вовсе.
	pts := rotate([]spot{spotA, spotB, spotC}, 8, 30, 30)

	got := FindSplit(pts)
	require.NotEmpty(t, got, "метание по трём местам обязано уличаться")
	assert.Greater(t, len(got), len(pts)/2,
		"при отсутствии лидера уличается весь промежуток")
}

func TestFindSplit_LeaderSurvives(t *testing.T) {
	// Случай `5cde6306`: машина стоит на Пензенской восемь часов и пишет редко.
	// Подделка в это же время сыплет часто — но только первые два часа, потому
	// что генератор работал не весь период.
	//
	// Настоящее место отмечается во ВСЕХ слотах, подделка в четверти — доля
	// слотов у лидера вчетверо выше, и уличается только подделка.
	//
	// Важно: подделка обязана быть активной достаточно, чтобы возвраты вообще
	// нашлись. Размажь её равномерно по всему периоду — доли сравняются, и это
	// будет уже другой случай, «лидера нет» (см. CatchesRotationBetweenPlaces).
	var pts []geo.Point
	const hours = 8
	for sec := 0; sec < hours*3600; sec += 420 { // настоящая: раз в семь минут
		pts = append(pts, at(sec, spotA.lon, spotA.lat))
		if sec < 2*3600 { // подделка: только первые два часа, но густо
			for k := range 6 {
				pts = append(pts, at(sec+k*60, spotB.lon, spotB.lat))
				pts = append(pts, at(sec+k*60+30, spotA.lon, spotA.lat))
			}
		}
	}
	slices.SortFunc(pts, func(a, b geo.Point) int { return a.Time.Compare(b.Time) })

	got := FindSplit(pts)
	require.NotEmpty(t, got, "метание в первые два часа обязано уличаться")

	inA, inB := 0, 0
	for i := range got {
		if geo.Haversine(pts[i], geo.Point{Lon: spotA.lon, Lat: spotA.lat}) <= SplitNearM {
			inA++
		}
		if geo.Haversine(pts[i], geo.Point{Lon: spotB.lon, Lat: spotB.lat}) <= SplitNearM {
			inB++
		}
	}
	assert.Zero(t, inA, "место-лидер трогать нельзя")
	assert.Positive(t, inB, "слабое место обязано быть уличено")
}

func TestFindSplit_RareRealStopBeatsFrequentFake(t *testing.T) {
	// Смысл всего правила. Настоящая стоянка пишет РЕДКО: на `5cde6306` машина
	// стояла на Пензенской 17 часов и отметилась 144 раза — точка раз в семь
	// минут, тогда как подделка в это же время сыпала точку раз в пять секунд.
	//
	// Любая мера НЕПРЕРЫВНОСТИ работает против настоящего сигнала: у него
	// разрывы больше. Доля слотов от частоты записи не зависит вовсе — место
	// отмечается в слоте, сколько бы точек оно туда ни положило.
	var pts []geo.Point
	const slot = 900    // четверть часа
	for s := range 24 { // шесть часов
		base := s * slot
		// настоящая стоянка: две точки на слот
		pts = append(pts, at(base+10, spotA.lon, spotA.lat))
		pts = append(pts, at(base+450, spotA.lon+0.0001, spotA.lat))
		// подделка: сыплет часто, но только в каждом третьем слоте
		if s%3 == 0 {
			for k := range 60 {
				pts = append(pts, at(base+100+k*5, spotB.lon, spotB.lat))
			}
		}
	}

	got := FindSplit(pts)
	inA := 0
	for i := range got {
		if geo.Haversine(pts[i], geo.Point{Lon: spotA.lon, Lat: spotA.lat}) <= SplitNearM {
			inA++
		}
	}
	assert.Zero(t, inA,
		"редкая настоящая стоянка не должна проигрывать частой подделке")
}

func TestFindSplit_LongTripIsNotSplit(t *testing.T) {
	// Переезд через полстраны: точки едущей машины тоже рассыпаются по
	// «местам», но размах выдаёт переезд. Порог 150 км — по замеру: шестьдесят
	// отсекали настоящие случаи (Астрахань раскидывает точки по 127 км дельты).
	far := []spot{{10.0, 0.0}, {12.0, 0.0}, {14.0, 0.0}} // шаг ~222 км
	pts := rotate(far, 8, 30, 30)
	assert.Empty(t, FindSplit(pts), "размах больше агломерации — это переезд")
}

func TestFindSplit_FewReturnsIsNotSplit(t *testing.T) {
	// Машина съездила туда-обратно пару раз: это поездка, а не метание.
	// За час фура в покинутое место не возвращается десятки раз.
	pts := rotate([]spot{spotA, spotB}, 30, 30, 2)
	assert.Empty(t, FindSplit(pts), "два возврата — не улика")
}

func TestFindSplit_ShortEpisodeIsNotJudged(t *testing.T) {
	// Промежуток короче двух часов: слотов мало, у всех мест доля близка к
	// единице, и «лидера не видно» означает не подделку, а нехватку данных.
	pts := rotate([]spot{spotA, spotB, spotC}, 4, 20, 8) // ~16 минут
	assert.Empty(t, FindSplit(pts), "на коротком промежутке судить не о чем")
}

func TestFindSplit_SinglePlaceIsClean(t *testing.T) {
	// Все точки в одном месте — раздваиваться нечему.
	pts := still(500, 30, 10.0, 0.0, 0.0003, 0)
	assert.Empty(t, FindSplit(pts))
}

func TestFindSplit_NearbyPlacesAreNotSplit(t *testing.T) {
	// Два «места» в полукилометре друг от друга — это дрожание в пределах
	// одной площадки, а не два источника.
	near := []spot{{10.0, 0.0}, {10.005, 0.0}} // ~556 м
	pts := rotate(near, 8, 30, 30)
	assert.Empty(t, FindSplit(pts), "места ближе трёх километров не считаются разными")
}

// Номера уходят в штрафы. Здесь их особенно легко испортить: правило
// работает через кластеры, и наружу отдаются индексы ИСХОДНОГО трека, а не
// позиции внутри кластера.
func TestFindSplit_ReturnsValidIndices(t *testing.T) {
	pts := rotate([]spot{spotA, spotB, spotC}, 8, 30, 30)
	for i := range FindSplit(pts) {
		assert.GreaterOrEqual(t, i, 0)
		assert.Less(t, i, len(pts))
	}
}

// Внутри правило раскладывает точки по кластерам и считает доли слотов.
// Кластеризация — первое место, где хочется отсортировать вход на месте.
func TestFindSplit_DoesNotModifyInput(t *testing.T) {
	pts := rotate([]spot{spotA, spotB, spotC}, 8, 30, 30)
	before := make([]geo.Point, len(pts))
	copy(before, pts)
	FindSplit(pts)
	assert.Equal(t, before, pts)
}

func TestFindSplit_SameTimestampBatch(t *testing.T) {
	// Выгрузка буфера: у пачки записей одно время. Слоты считаются делением на
	// длительность промежутка, и нулевая длительность обязана быть безопасной.
	pts := make([]geo.Point, 60)
	for i := range pts {
		s := spotA
		if i%2 == 1 {
			s = spotB
		}
		pts[i] = at(0, s.lon, s.lat)
	}
	assert.NotPanics(t, func() { FindSplit(pts) })
}

// --------------------------------------------------- внутренние помощники

// Кластеризация по отдельности: четыре точки, попарно близкие, обязаны дать
// РОВНО два места по два. Разъехавшись здесь, правило дальше сравнивает
// покрытие несуществующих мест — и виноватого назначает наугад.
func TestSplitClusters_AssignsToNearestCentre(t *testing.T) {
	pts := []geo.Point{
		at(0, 10.0, 0.0),
		at(10, 10.0002, 0.0), // рядом с первой
		at(20, 10.07, 0.0),   // другое место
		at(30, 10.0701, 0.0), // рядом со вторым
	}
	cs := splitClusters(pts, []int{0, 1, 2, 3}, SplitNearM)
	require.Len(t, cs, 2)
	assert.Len(t, cs[0].idx, 2)
	assert.Len(t, cs[1].idx, 2)
}

// Пустой вход. Кластеризацию зовут из середины правила, где список
// подозреваемых уже отфильтрован и вполне может оказаться пустым.
func TestSplitClusters_Empty(t *testing.T) {
	assert.Empty(t, splitClusters(nil, nil, SplitNearM))
}

func TestSplitShare_IgnoresRecordingRate(t *testing.T) {
	// Доля слотов не зависит от того, сколько точек место положило в слот.
	pts := make([]geo.Point, 0, 100)
	var rare, dense []int
	for s := range 8 {
		base := s * 900
		rare = append(rare, len(pts))
		pts = append(pts, at(base+10, 10.0, 0.0))
		for k := range 10 {
			dense = append(dense, len(pts))
			pts = append(pts, at(base+100+k*5, 10.07, 0.0))
		}
	}
	t0, t1 := pts[0].Time, pts[len(pts)-1].Time

	assert.InDelta(t, splitShare(pts, rare, t0, t1), splitShare(pts, dense, t0, t1), 0.2,
		"место, отметившееся в тех же слотах, имеет ту же долю")
}

func TestSplitReturns_CountsOnlyComebacks(t *testing.T) {
	// Монотонный проезд через три места подряд — это не возвраты.
	pts := []geo.Point{
		at(0, 10.00, 0), at(10, 10.00, 0), at(20, 10.00, 0),
		at(30, 10.07, 0), at(40, 10.07, 0), at(50, 10.07, 0),
		at(60, 10.14, 0), at(70, 10.14, 0), at(80, 10.14, 0),
	}
	idx := make([]int, len(pts))
	for i := range idx {
		idx[i] = i
	}
	cs := splitClusters(pts, idx, SplitNearM)
	cnt, _ := splitReturns(pts, idx, cs, SplitMinDistM)
	assert.Zero(t, cnt, "проезд вперёд возвратов не содержит")
}

// Возвраты по отдельности: сигнал мечется между двумя местами шесть раз.
// Считается именно ВОЗВРАЩЕНИЕ в уже покинутое место, а не всякий переход, —
// иначе обычная езда по кругу дала бы тот же счёт.
//
// Проверяются обе стороны ответа: и сколько возвратов насчитали, и что
// замешанными признаны ОБА места. Одного числа мало — правило могло бы
// насчитать возвраты и обвинить в них одно место из двух.
func TestSplitReturns_CountsRealComebacks(t *testing.T) {
	pts := []geo.Point{
		at(0, 10.00, 0), at(10, 10.07, 0),
		at(20, 10.00, 0), at(30, 10.07, 0),
		at(40, 10.00, 0), at(50, 10.07, 0),
	}
	idx := []int{0, 1, 2, 3, 4, 5}
	cs := splitClusters(pts, idx, SplitNearM)
	cnt, guilty := splitReturns(pts, idx, cs, SplitMinDistM)
	assert.GreaterOrEqual(t, cnt, 4, "метание туда-обратно — это возвраты")
	assert.Len(t, guilty, 2, "замешаны оба места")
}

// Пять тысяч точек: три места по очереди, двести кругов. Внутри
// кластеризация и попарное сравнение покрытий — стоимость растёт быстрее
// длины трека, и следить за этим надо на глазок по замеру.
func BenchmarkFindSplit(b *testing.B) {
	pts := rotate([]spot{spotA, spotB, spotC}, 8, 30, 200) // ~4800 точек
	b.ReportAllocs()
	for b.Loop() {
		FindSplit(pts)
	}
}
