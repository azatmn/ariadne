package core

import (
	"testing"
	"time"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Точки берём у экватора: там один градус по обеим осям это ровно 111.2 км,
// поэтому расстояния в тестах считаются в уме и не зависят от широты.
// 0.001° = 111.2 м, 0.0001° = 11.1 м.

var t0 = time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)

// at — точка через sec секунд от начала отсчёта.
func at(sec int, lon, lat float64) geo.Point {
	return geo.Point{Time: t0.Add(time.Duration(sec) * time.Second), Lon: lon, Lat: lat}
}

// still — n точек, стоящих на месте с дрожанием amp градусов, с шагом step секунд.
// Дрожание чередуется по знаку, чтобы пятно не уползало.
func still(n, step int, lon, lat, amp float64, startSec int) []geo.Point {
	out := make([]geo.Point, n)
	for i := range out {
		sign := 1.0
		if i%2 == 1 {
			sign = -1.0
		}
		out[i] = at(startSec+i*step, lon+sign*amp, lat+sign*amp)
	}
	return out
}

// drive — n точек, едущих на восток с шагом stepDeg градусов.
func drive(n, stepSec int, lon, lat, stepDeg float64, startSec int) []geo.Point {
	out := make([]geo.Point, n)
	for i := range out {
		out[i] = at(startSec+i*stepSec, lon+float64(i)*stepDeg, lat)
	}
	return out
}

// ------------------------------------------------------------- FindStops

func TestFindStops_EmptyAndTiny(t *testing.T) {
	// Вырожденные длины не должны ни падать, ни находить стоянку.
	// Двух точек здесь нет намеренно: две точки в одном месте с разрывом
	// дольше порога — это как раз стоянка, см. TestFindStops_TwoPointsAreEnough.
	for _, pts := range [][]geo.Point{
		nil,
		{},
		{at(0, 10, 0)},
	} {
		got := FindStops(pts, StopRadiusM, StopMinStay)
		assert.Empty(t, got, "на %d точках стоянок быть не может", len(pts))
	}
}

func TestFindStops_TwoPointsAreEnough(t *testing.T) {
	// Минимальная стоянка — две точки: приехал и через десять минут уехал.
	// Меньше двух быть не может, потому что стоянка это интервал.
	pts := []geo.Point{at(0, 10.0, 0.0), at(600, 10.0, 0.0)}
	got := FindStops(pts, StopRadiusM, StopMinStay)
	require.Len(t, got, 1)
	assert.Equal(t, StopRange{0, 1}, got[0])
}

func TestFindStops_TwoPointsFarApartAreNotStop(t *testing.T) {
	// Те же две точки, но в разных местах — это переезд, а не стоянка.
	pts := []geo.Point{at(0, 10.0, 0.0), at(600, 10.05, 0.0)} // 5.6 км
	assert.Empty(t, FindStops(pts, StopRadiusM, StopMinStay))
}

func TestFindStops_MovingTrackHasNone(t *testing.T) {
	// Шаг 0.005° = 556 м между точками, это заведомо больше радиуса 150 м.
	pts := drive(20, 30, 10.0, 0.0, 0.005, 0)
	assert.Empty(t, FindStops(pts, StopRadiusM, StopMinStay))
}

func TestFindStops_FindsSingleStop(t *testing.T) {
	var pts []geo.Point
	pts = append(pts, drive(3, 60, 10.0, 0.0, 0.005, 0)...)       // подъезд
	pts = append(pts, still(10, 120, 10.02, 0.0, 0.0002, 300)...) // стоянка 18 мин
	pts = append(pts, drive(3, 60, 10.05, 0.0, 0.005, 1600)...)   // отъезд

	got := FindStops(pts, StopRadiusM, StopMinStay)
	require.Len(t, got, 1, "одна стоянка посреди поездки")
	assert.Equal(t, 3, got[0].Start, "стоянка начинается там, где машина встала")
	assert.Equal(t, 12, got[0].End, "и кончается последней неподвижной точкой")
}

func TestFindStops_TooShortIsNotStop(t *testing.T) {
	// Точки стоят на месте, но всего четыре минуты — это светофор, а не стоянка.
	var pts []geo.Point
	pts = append(pts, drive(2, 60, 10.0, 0.0, 0.005, 0)...)
	pts = append(pts, still(5, 60, 10.01, 0.0, 0.0002, 200)...) // 4 минуты
	pts = append(pts, drive(2, 60, 10.02, 0.0, 0.005, 700)...)

	assert.Empty(t, FindStops(pts, StopRadiusM, StopMinStay),
		"короче пяти минут — не стоянка")
}

func TestFindStops_ExactlyAtThresholdCounts(t *testing.T) {
	// Ровно пять минут — граница включительно, как в прототипе (>=).
	pts := []geo.Point{
		at(0, 10.0, 0.0),
		at(150, 10.0001, 0.0001),
		at(300, 10.0, 0.0),
	}
	got := FindStops(pts, StopRadiusM, StopMinStay)
	require.Len(t, got, 1, "ровно порог засчитывается")
	assert.Equal(t, 0, got[0].Start)
	assert.Equal(t, 2, got[0].End)
}

func TestFindStops_StopAtTrackStart(t *testing.T) {
	var pts []geo.Point
	pts = append(pts, still(8, 120, 10.0, 0.0, 0.0002, 0)...)
	pts = append(pts, drive(4, 60, 10.01, 0.0, 0.005, 1000)...)

	got := FindStops(pts, StopRadiusM, StopMinStay)
	require.Len(t, got, 1)
	assert.Equal(t, 0, got[0].Start, "стоянка в самом начале трека тоже стоянка")
}

func TestFindStops_StopAtTrackEnd(t *testing.T) {
	var pts []geo.Point
	pts = append(pts, drive(4, 60, 10.0, 0.0, 0.005, 0)...)
	pts = append(pts, still(8, 120, 10.03, 0.0, 0.0002, 300)...)

	got := FindStops(pts, StopRadiusM, StopMinStay)
	require.Len(t, got, 1)
	assert.Equal(t, len(pts)-1, got[0].End, "стоянка в конце доходит до последней точки")
}

func TestFindStops_TwoSeparateStops(t *testing.T) {
	var pts []geo.Point
	pts = append(pts, still(6, 120, 10.0, 0.0, 0.0002, 0)...)     // стоянка 1
	pts = append(pts, drive(4, 60, 10.02, 0.0, 0.01, 800)...)     // переезд
	pts = append(pts, still(6, 120, 10.10, 0.0, 0.0002, 1200)...) // стоянка 2

	got := FindStops(pts, StopRadiusM, StopMinStay)
	require.Len(t, got, 2, "две стоянки, разделённые переездом")
	assert.Less(t, got[0].End, got[1].Start, "интервалы не пересекаются")
}

func TestFindStops_RangesNeverOverlap(t *testing.T) {
	// Свойство, на которое опирается всё дальнейшее: каждая точка принадлежит
	// не более чем одной стоянке. Иначе сопоставление «точка → стоянка»
	// становится неоднозначным.
	var pts []geo.Point
	for k := range 5 {
		pts = append(pts, still(6, 120, 10.0+float64(k)*0.1, 0.0, 0.0002, k*2000)...)
		pts = append(pts, drive(3, 60, 10.05+float64(k)*0.1, 0.0, 0.01, k*2000+800)...)
	}

	got := FindStops(pts, StopRadiusM, StopMinStay)
	require.NotEmpty(t, got)
	for i := range got {
		assert.LessOrEqual(t, got[i].Start, got[i].End, "интервал не вывернут")
		if i > 0 {
			assert.Less(t, got[i-1].End, got[i].Start,
				"стоянки %d и %d перекрываются", i-1, i)
		}
	}
}

func TestFindStops_SlowDriftLeavesTheSpot(t *testing.T) {
	// Машина ползёт: каждая точка недалеко от предыдущей, но пятно уезжает.
	// Считать надо от ЦЕНТРА накопленного пятна, иначе медленный ход
	// зачтётся за стоянку и трек потеряет километры.
	pts := drive(40, 60, 10.0, 0.0, 0.0008, 0) // 89 м за минуту = 5.3 км/ч

	got := FindStops(pts, StopRadiusM, StopMinStay)
	for _, s := range got {
		span := geo.Haversine(pts[s.Start], pts[s.End])
		assert.Less(t, span, 400.0,
			"стоянка не должна растягиваться на весь путь: %.0f м", span)
	}
}

func TestFindStops_FrozenCoordinateIsStop(t *testing.T) {
	// Залипший трекер повторяет координату байт в байт. По длительности и
	// радиусу это стоянка — и находиться она обязана; отличать её от честной
	// будут отдельные проверки (см. IsFrozen и TrustedStop).
	pts := make([]geo.Point, 20)
	for i := range pts {
		pts[i] = at(i*60, 10.0, 0.0)
	}
	got := FindStops(pts, StopRadiusM, StopMinStay)
	require.Len(t, got, 1)
	assert.Equal(t, 0, got[0].Start)
	assert.Equal(t, 19, got[0].End)
}

func TestFindStops_HugeTimeGapInsideSpot(t *testing.T) {
	// Две точки в одном месте, между ними сутки молчания. Формально это
	// «стоянка», и находить её надо — но доверия ей не будет (TrustedStop).
	pts := []geo.Point{
		at(0, 10.0, 0.0),
		at(86400, 10.0001, 0.0),
	}
	got := FindStops(pts, StopRadiusM, StopMinStay)
	require.Len(t, got, 1)
}

func TestFindStops_CustomRadiusAndStay(t *testing.T) {
	// Параметры обязаны работать: те же точки при другом радиусе дают другой ответ.
	pts := still(10, 60, 10.0, 0.0, 0.0005, 0) // дрожание ±55 м, размах ~157 м

	wide := FindStops(pts, 200, 5*time.Minute)
	assert.Len(t, wide, 1, "при радиусе 200 м пятно целиком внутри")

	narrow := FindStops(pts, 20, 5*time.Minute)
	assert.Empty(t, narrow, "при радиусе 20 м дрожание выходит за пятно")
}

// ------------------------------------------------------------ TrustedStop

// trustCase — стоянка с настраиваемыми свойствами и хвостом после неё.
func trustCase(durSec, n int, amp float64, tail []geo.Point) ([]geo.Point, StopRange) {
	step := durSec / max(n-1, 1)
	pts := still(n, step, 10.0, 0.0, amp, 0)
	s := StopRange{Start: 0, End: len(pts) - 1}
	pts = append(pts, tail...)
	return pts, s
}

func TestTrustedStop_NormalStopIsTrusted(t *testing.T) {
	// Час на месте, дрожание ±11 м (размах ~31 м), выезд с земной скоростью.
	pts, s := trustCase(3600, 30, 0.0001, drive(3, 60, 10.01, 0.0, 0.005, 3700))
	assert.True(t, TrustedStop(pts, s, 15, true), "обычная часовая стоянка")
}

func TestTrustedStop_TooShortIsNotTrusted(t *testing.T) {
	// Четверть часа — порог; всё, что короче, привилегий не получает.
	pts, s := trustCase(600, 10, 0.0001, drive(3, 60, 10.01, 0.0, 0.005, 700))
	assert.False(t, TrustedStop(pts, s, 15, true), "десять минут — мало")
}

func TestTrustedStop_ExactlyAtDurationThreshold(t *testing.T) {
	pts, s := trustCase(900, 16, 0.0001, drive(3, 60, 10.01, 0.0, 0.005, 1000))
	assert.True(t, TrustedStop(pts, s, 15, true), "ровно четверть часа засчитывается")
}

func TestTrustedStop_FrozenCoordinateIsNotTrusted(t *testing.T) {
	// Размах меньше 20 м: приёмник всегда шумит, а залипший трекер — нет.
	// На разметке пользователя это разделило идеально: из 96 настоящих стоянок
	// ни одной с разбросом меньше 20 м.
	pts, s := trustCase(3600, 30, 0.00001, drive(3, 60, 10.01, 0.0, 0.005, 3700))
	assert.False(t, TrustedStop(pts, s, 15, true), "координата не дрожит — это залипание")
}

func TestTrustedStop_FarFromAnyRoadIsNotTrusted(t *testing.T) {
	// «Стоянка» в чистом поле привилегии не получает: 700 м без дороги фура
	// не проедет. Порог взят по разметке — у настоящих стоянок максимум 515 м.
	pts, s := trustCase(3600, 30, 0.0001, drive(3, 60, 10.01, 0.0, 0.005, 3700))
	assert.False(t, TrustedStop(pts, s, 1500, true), "1.5 км от дороги — не стоянка")
	assert.True(t, TrustedStop(pts, s, 600, true), "600 м — ещё в допуске")
}

func TestTrustedStop_UnknownSnapDoesNotDisqualify(t *testing.T) {
	// Снэпа нет (OSRM промолчал). Это не довод против стоянки.
	pts, s := trustCase(3600, 30, 0.0001, drive(3, 60, 10.01, 0.0, 0.005, 3700))
	assert.True(t, TrustedStop(pts, s, 0, false), "молчание OSRM не улика")
}

func TestTrustedStop_GapEatsMostOfTheTime(t *testing.T) {
	// «Стоянка» из двух точек по краям суточного молчания — это не стоянка,
	// а два куска движения по краям обрыва связи.
	pts := []geo.Point{
		at(0, 10.0, 0.0),
		at(100, 10.0002, 0.0002),
		at(86400, 10.0001, 0.0001),
	}
	s := StopRange{Start: 0, End: 2}
	assert.False(t, TrustedStop(pts, s, 15, true), "одна дыра съела почти всё время")
}

func TestTrustedStop_ImpossibleExitIsNotTrusted(t *testing.T) {
	// Из стоянки трек уходит на 47 км за 19 секунд — 8900 км/ч.
	// Так уезжают только подделки.
	pts, s := trustCase(3600, 30, 0.0001, []geo.Point{
		at(3619, 10.42, 0.0), // ~46.7 км за 19 с
		at(3700, 10.43, 0.0),
	})
	assert.False(t, TrustedStop(pts, s, 15, true), "выезд быстрее 150 км/ч невозможен")
}

func TestTrustedStop_NearbyJitterIsNotAnExit(t *testing.T) {
	// Сразу за стоянкой точки продолжают топтаться в паре сотен метров.
	// Это ещё стоянка, а не выезд, и судить по ним нельзя.
	pts, s := trustCase(3600, 30, 0.0001, []geo.Point{
		at(3601, 10.0005, 0.0), // 55 м за секунду = 200 км/ч, но с места не ушёл
		at(3602, 10.0008, 0.0),
	})
	assert.True(t, TrustedStop(pts, s, 15, true),
		"дрожание рядом с местом не считается выездом")
}

func TestTrustedStop_SingleGlitchAfterStopDoesNotCondemn(t *testing.T) {
	// Сразу за честной стоянкой стоит одиночный глюк, а следом нормальный
	// отъезд. Берём ЛУЧШИЙ из нескольких кандидатов, поэтому стоянка цела.
	pts, s := trustCase(3600, 30, 0.0001, []geo.Point{
		at(3610, 10.5, 0.0),   // глюк: 55 км за 10 с
		at(3700, 10.005, 0.0), // а на деле уехал спокойно
		at(3800, 10.010, 0.0),
	})
	assert.True(t, TrustedStop(pts, s, 15, true),
		"одиночный глюк после стоянки не приговор")
}

func TestTrustedStop_NoExitAtAllIsTrusted(t *testing.T) {
	// Стоянка в конце трека: выезжать некуда, и это не улика.
	pts, s := trustCase(3600, 30, 0.0001, nil)
	assert.True(t, TrustedStop(pts, s, 15, true), "стоянка в конце трека годится")
}

func TestTrustedStop_ExitBeyondWindowIsIgnored(t *testing.T) {
	// Точки после стоянки идут позже, чем через четверть часа — это уже
	// другой эпизод, судить стоянку по ним нельзя.
	pts, s := trustCase(3600, 30, 0.0001, []geo.Point{
		at(3600+1000, 10.5, 0.0), // далеко и поздно
	})
	assert.True(t, TrustedStop(pts, s, 15, true), "за пределами окна не смотрим")
}

// --------------------------------------------------------------- IsFrozen

func TestIsFrozen_RepeatedCoordinate(t *testing.T) {
	pts := make([]geo.Point, 20)
	for i := range pts {
		pts[i] = at(i*60, 10.0, 0.0)
	}
	assert.True(t, IsFrozen(pts, StopRange{0, 19}), "координата байт в байт — залипание")
}

func TestIsFrozen_RealJitterIsNotFrozen(t *testing.T) {
	pts := still(20, 60, 10.0, 0.0, 0.0001, 0) // размах ~31 м
	assert.False(t, IsFrozen(pts, StopRange{0, 19}), "живой приёмник всегда шумит")
}

func TestIsFrozen_AtThreshold(t *testing.T) {
	// Размах ровно около порога в 5 м — проверяем обе стороны.
	tiny := still(10, 60, 10.0, 0.0, 0.00001, 0) // размах ~3.1 м
	assert.True(t, IsFrozen(tiny, StopRange{0, 9}))

	bigger := still(10, 60, 10.0, 0.0, 0.00005, 0) // размах ~15.7 м
	assert.False(t, IsFrozen(bigger, StopRange{0, 9}))
}

func TestIsFrozen_SinglePoint(t *testing.T) {
	pts := []geo.Point{at(0, 10.0, 0.0)}
	assert.True(t, IsFrozen(pts, StopRange{0, 0}), "одна точка размаха не имеет")
}

// ------------------------------------------------------------ StopRange

func TestStopRange_Len(t *testing.T) {
	assert.Equal(t, 1, StopRange{5, 5}.Len(), "интервал включительный")
	assert.Equal(t, 6, StopRange{5, 10}.Len())
}

func TestStopOwner_MapsEveryPointOfEveryStop(t *testing.T) {
	// Сопоставление «точка → номер стоянки» нужно дорисовке: переход внутри
	// одной стоянки дырой не считается, а выезд из неё — считается.
	stops := []StopRange{{2, 5}, {9, 11}}
	owner := StopOwner(15, stops)

	for i := 2; i <= 5; i++ {
		require.Contains(t, owner, i)
		assert.Equal(t, 0, owner[i], "точка %d принадлежит первой стоянке", i)
	}
	for i := 9; i <= 11; i++ {
		require.Contains(t, owner, i)
		assert.Equal(t, 1, owner[i], "точка %d принадлежит второй стоянке", i)
	}
	for _, i := range []int{0, 1, 6, 7, 8, 12, 13, 14} {
		assert.NotContains(t, owner, i, "точка %d вне стоянок", i)
	}
}

func TestStopOwner_Empty(t *testing.T) {
	assert.Empty(t, StopOwner(10, nil))
	assert.Empty(t, StopOwner(0, []StopRange{{0, 0}}))
}

// ------------------------------------------------------- производительность

// Вырожденный случай: тысячи точек в одном пятне, но каждая серия короче
// порога. Внутренний цикл каждый раз доходит до конца пятна, а внешний
// сдвигается на единицу — это квадрат. На пределе MAX_POINTS=50000 такой
// трек может прийти от залипшего трекера, и обработка не должна вставать.
func TestFindStops_DegenerateClusterIsNotQuadratic(t *testing.T) {
	// 20 000 точек в пятне 30 м, все за 4 минуты — стоянкой не признаются
	// (короче порога), но пятно не разрывается ни разу.
	const n = 20000
	pts := make([]geo.Point, n)
	for i := range pts {
		sign := 1.0
		if i%2 == 1 {
			sign = -1.0
		}
		// время идёт мелкими долями, чтобы весь сгусток уложился в 4 минуты
		pts[i] = geo.Point{
			Time: t0.Add(time.Duration(i*12) * time.Millisecond),
			Lon:  10.0 + sign*0.0001,
			Lat:  0.0 + sign*0.0001,
		}
	}

	done := make(chan []StopRange, 1)
	go func() { done <- FindStops(pts, StopRadiusM, StopMinStay) }()

	select {
	case got := <-done:
		assert.Empty(t, got, "сгусток короче порога стоянкой не является")
	case <-time.After(5 * time.Second):
		t.Fatal("поиск стоянок не уложился в пять секунд на 20 тысячах точек — " +
			"это квадратичный рост, на пределе в 50 тысяч сервис встанет")
	}
}

func BenchmarkFindStops_RealisticTrack(b *testing.B) {
	// Смесь езды и стоянок, как на настоящем треке.
	var pts []geo.Point
	for k := range 200 {
		pts = append(pts, drive(40, 30, 10.0+float64(k)*0.05, 0.0, 0.001, k*4000)...)
		pts = append(pts, still(20, 60, 10.04+float64(k)*0.05, 0.0, 0.0002, k*4000+1300)...)
	}
	b.ReportAllocs()
	for b.Loop() {
		FindStops(pts, StopRadiusM, StopMinStay)
	}
}
