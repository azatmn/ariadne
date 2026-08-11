package core

import (
	"time"

	"testing"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Перестановка пачек выгрузки буфера.
//
// Трекер копит записи без связи и выгружает пачкой, ставя всем одно время
// отправки — и нередко в ОБРАТНОМ порядке. Найдено на `573f42bf` (07.07,
// Ставрополь): трек идёт на восток, а внутри каждой пачки курс развёрнут на
// запад. Путь по точкам 6.80 км при прямой 2.78 км — крюк вдвое с лишним из
// чистой перестановки, при снэпах 1–8 м. Координаты верные, сломан только
// порядок.

// pathOf — длина пути по точкам в заданном порядке.
func pathOf(pts []geo.Point, order []int) float64 {
	seq := make([]geo.Point, len(order))
	for i, k := range order {
		seq[i] = pts[k]
	}
	return geo.TotalLength(seq)
}

func TestReorder_EmptyAndTiny(t *testing.T) {
	for _, n := range []int{0, 1, 2} {
		pts := drive(n, 30, 10, 0, 0.001, 0)
		got := ReorderBatches(pts)
		require.Len(t, got, n)
		for i := range got {
			assert.Equal(t, i, got[i], "трогать нечего")
		}
	}
}

func TestReorder_UniqueTimestampsUnchanged(t *testing.T) {
	// У каждой точки своё время — пачек нет, порядок известен и менять его
	// нельзя ни при каких обстоятельствах.
	pts := drive(50, 30, 10.0, 0.0, 0.002, 0)
	got := ReorderBatches(pts)
	for i := range got {
		assert.Equal(t, i, got[i], "точка %d сдвинулась без причины", i)
	}
}

func TestReorder_FixesReversedBatch(t *testing.T) {
	// Ровно случай Ставрополя: трек идёт на восток, а пачка развёрнута назад.
	pts := []geo.Point{
		at(0, 10.000, 0), // до пачки
		// пачка: одно время на всех, порядок обратный
		at(60, 10.004, 0),
		at(60, 10.003, 0),
		at(60, 10.002, 0),
		at(60, 10.001, 0),
		at(120, 10.005, 0), // после пачки
	}

	before := pathOf(pts, []int{0, 1, 2, 3, 4, 5})
	got := ReorderBatches(pts)
	after := pathOf(pts, got)

	assert.Less(t, after, before, "перестановка обязана укорачивать путь")
	assert.InDelta(t, geo.Haversine(pts[0], pts[5]), after, 1.0,
		"на прямой трассе путь обязан сойтись к прямой")
}

func TestReorder_KeepsCorrectOrder(t *testing.T) {
	// Пачка уже в правильном порядке — переставлять нечего.
	pts := []geo.Point{
		at(0, 10.000, 0),
		at(60, 10.001, 0),
		at(60, 10.002, 0),
		at(60, 10.003, 0),
		at(120, 10.004, 0),
	}
	got := ReorderBatches(pts)
	assert.Equal(t, []int{0, 1, 2, 3, 4}, got)
}

func TestReorder_OnlyPermutesWithinBatch(t *testing.T) {
	// Точки пачки могут меняться местами между собой, но не покидать свои
	// позиции в треке: иначе перемешались бы разные моменты времени.
	pts := []geo.Point{
		at(0, 10.000, 0),
		at(60, 10.004, 0), at(60, 10.003, 0), at(60, 10.002, 0),
		at(120, 10.010, 0),
		at(180, 10.014, 0), at(180, 10.013, 0), at(180, 10.012, 0),
		at(240, 10.020, 0),
	}
	got := ReorderBatches(pts)

	// Одиночные точки на своих местах.
	for _, i := range []int{0, 4, 8} {
		assert.Equal(t, i, got[i], "точка вне пачки сдвинулась")
	}
	// Каждая пачка — перестановка своих же индексов.
	assert.ElementsMatch(t, []int{1, 2, 3}, got[1:4])
	assert.ElementsMatch(t, []int{5, 6, 7}, got[5:8])
}

func TestReorder_IsPermutation(t *testing.T) {
	// Ни одна точка не теряется и не дублируется — свойство, без которого
	// всё дальнейшее посыплется.
	pts := []geo.Point{at(0, 10.0, 0)}
	for k := range 20 {
		for j := range 5 {
			pts = append(pts, at(60+k*60, 10.0+float64(20-j)*0.001, 0))
		}
	}
	got := ReorderBatches(pts)

	seen := make(map[int]int, len(got))
	for _, v := range got {
		seen[v]++
	}
	require.Len(t, seen, len(pts), "перестановка обязана быть взаимно однозначной")
	for i := range pts {
		assert.Equal(t, 1, seen[i], "индекс %d встречается не один раз", i)
	}
}

func TestReorder_HugeBatchIsLeftAlone(t *testing.T) {
	// Пачка больше дюжины — это не выгрузка буфера, а свалка: в одном треке
	// до шестнадцати точек на секунду с разлётом 1.6 км, то есть два потока,
	// и порядок им не поможет. Жадный перебор там уже ненадёжен.
	var pts []geo.Point
	pts = append(pts, at(0, 10.0, 0))
	for k := range 16 {
		pts = append(pts, at(60, 10.0+float64(16-k)*0.01, 0))
	}
	pts = append(pts, at(120, 10.2, 0))

	got := ReorderBatches(pts)
	for i := range got {
		assert.Equal(t, i, got[i], "большую пачку трогать нельзя")
	}
}

func TestReorder_BatchAtTrackStart(t *testing.T) {
	// Пачка в самом начале: предыдущей точки нет, отталкиваться не от чего.
	pts := []geo.Point{
		at(0, 10.003, 0), at(0, 10.002, 0), at(0, 10.001, 0),
		at(60, 10.010, 0),
	}
	got := ReorderBatches(pts)
	assert.Len(t, got, len(pts))
	assert.Less(t, pathOf(pts, got), pathOf(pts, []int{0, 1, 2, 3})+1,
		"путь не должен вырасти")
}

func TestReorder_BatchAtTrackEnd(t *testing.T) {
	pts := []geo.Point{
		at(0, 10.000, 0),
		at(60, 10.003, 0), at(60, 10.002, 0), at(60, 10.001, 0),
	}
	got := ReorderBatches(pts)
	assert.Len(t, got, len(pts))
	assert.LessOrEqual(t, pathOf(pts, got), pathOf(pts, []int{0, 1, 2, 3})+1)
}

func TestReorder_WholeTrackIsOneBatch(t *testing.T) {
	// Вырожденный случай: у всего трека одно время. Соседей нет ни с одной
	// стороны — жадный обход стартует с первой точки.
	pts := []geo.Point{
		at(0, 10.003, 0), at(0, 10.001, 0), at(0, 10.004, 0), at(0, 10.002, 0),
	}
	got := ReorderBatches(pts)
	assert.Len(t, got, 4)
	seen := map[int]bool{}
	for _, v := range got {
		seen[v] = true
	}
	assert.Len(t, seen, 4)
}

func TestReorder_NeverLengthensPath(t *testing.T) {
	// Главное свойство: выбирается порядок с МИНИМАЛЬНЫМ путём среди
	// рассмотренных, и исходный порядок всегда среди них. Значит путь не может
	// вырасти ни на одном треке.
	cases := [][]geo.Point{
		// пачка развёрнута
		{at(0, 10.0, 0), at(60, 10.004, 0), at(60, 10.003, 0), at(60, 10.002, 0), at(120, 10.01, 0)},
		// пачка вперемешку
		{at(0, 10.0, 0), at(60, 10.003, 0), at(60, 10.001, 0), at(60, 10.004, 0), at(60, 10.002, 0), at(120, 10.01, 0)},
		// пачка разлетелась в стороны
		{at(0, 10.0, 0), at(60, 10.01, 0.01), at(60, 10.01, -0.01), at(60, 10.02, 0), at(120, 10.03, 0)},
	}
	for k, pts := range cases {
		before := pathOf(pts, rangeIdx(0, len(pts)-1))
		after := pathOf(pts, ReorderBatches(pts))
		assert.LessOrEqual(t, after, before+1e-6, "случай %d: путь вырос", k)
	}
}

func TestReorder_DoesNotModifyInput(t *testing.T) {
	pts := []geo.Point{
		at(0, 10.0, 0), at(60, 10.004, 0), at(60, 10.002, 0), at(120, 10.01, 0),
	}
	before := make([]geo.Point, len(pts))
	copy(before, pts)
	ReorderBatches(pts)
	assert.Equal(t, before, pts, "перестановка возвращает индексы, а не двигает точки")
}

func TestReorder_IdenticalCoordinatesInBatch(t *testing.T) {
	// Залипший трекер: вся пачка в одной координате. Любой порядок даёт
	// нулевой путь, и выбор не должен зависеть от случайности.
	pts := []geo.Point{
		at(0, 10.0, 0),
		at(60, 10.001, 0), at(60, 10.001, 0), at(60, 10.001, 0),
		at(120, 10.002, 0),
	}
	assert.NotPanics(t, func() { ReorderBatches(pts) })
}

func BenchmarkReorderBatches(b *testing.B) {
	// Трек с частыми пачками по пять точек — как на настоящем грязном треке.
	var pts []geo.Point
	for k := range 10000 {
		if k%5 == 0 {
			for j := range 5 {
				pts = append(pts, at(k*30, 10.0+float64(k)*0.001+float64(5-j)*0.0001, 0))
			}
			continue
		}
		pts = append(pts, at(k*30, 10.0+float64(k)*0.001, 0))
	}
	b.ReportAllocs()
	for b.Loop() {
		ReorderBatches(pts)
	}
}

func TestReorderBatches_MirroredCandidatesAreATie(t *testing.T) {
	// Точка до пачки и точка после стоят в ОДНОМ месте. Тогда жадный обход от
	// начала и жадный обход от конца дают зеркальные пути — одни и те же
	// отрезки, сложенные в обратном порядке.
	//
	// Геометрически это ничья, и побеждать должен первый кандидат. Но сложение
	// вещественных чисел не ассоциативно: суммы расходятся в последнем бите, и
	// без порога выигрывал тот, кому повезло с округлением. Найдено сквозной
	// сверкой на `00d2e77a`, где из-за этого разъезжался весь трек.
	same := geo.Point{Time: t0, Lon: 38.267095, Lat: 53.156318}
	pts := []geo.Point{
		{Time: t0, Lon: same.Lon, Lat: same.Lat},
		at(2, 38.267095, 53.156318),
		at(2, 38.266687, 53.156023),
		at(2, 38.266572, 53.156348),
		at(2, 38.266543, 53.156183),
		at(2, 38.266472, 53.155938),
		at(2, 38.265662, 53.156138),
		at(2, 38.264957, 53.156347),
		at(2, 38.264292, 53.156302),
		{Time: t0.Add(121 * time.Second), Lon: same.Lon, Lat: same.Lat},
	}

	got := ReorderBatches(pts)

	// Порядок обязан быть тем же при каждом прогоне — на этом и держится
	// воспроизводимость километража.
	for range 20 {
		assert.Equal(t, got, ReorderBatches(pts), "выбор порядка должен быть устойчивым")
	}

	// И это должен быть жадный обход ОТ НАЧАЛА: первый из равных кандидатов.
	assert.Equal(t, []int{0, 1, 3, 4, 2, 5, 6, 7, 8, 9}, got)
}
