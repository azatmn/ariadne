package core

import (
	"math"
	"testing"
	"time"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------------ median

func TestMedian_OddLength(t *testing.T) {
	assert.Equal(t, 3.0, median([]float64{5, 1, 3, 2, 4}))
}

func TestMedian_EvenLengthAveragesMiddleTwo(t *testing.T) {
	// Как в питоновской statistics.median: на чётной длине это среднее двух
	// средних, а не нижнее из них. Расхождение здесь сдвинуло бы все веса.
	assert.Equal(t, 2.5, median([]float64{1, 2, 3, 4}))
	assert.Equal(t, 3.0, median([]float64{1, 2, 4, 8}))
}

func TestMedian_SingleAndEmpty(t *testing.T) {
	assert.Equal(t, 7.0, median([]float64{7}))
	assert.Equal(t, 0.0, median(nil))
}

func TestMedian_DoesNotMutateInput(t *testing.T) {
	in := []float64{5, 1, 3}
	median(in)
	assert.Equal(t, []float64{5, 1, 3}, in, "сглаживание зовёт медиану тысячи раз подряд")
}

// ------------------------------------------------------------------ SigmaOf

func TestSigmaOf_NoSnapsFallsBackToDefault(t *testing.T) {
	assert.Equal(t, SnapSigmaM, SigmaOf(nil, nil))
	assert.Equal(t, SnapSigmaM, SigmaOf([]float64{5, 7}, []bool{false, false}),
		"молчание OSRM — не повод менять оценку точности прибора")
}

func TestSigmaOf_GoodReceiverKeepsLowerBound(t *testing.T) {
	// Медиана 4 м даёт 8 м, но ниже базовых 25 м не опускаемся: адаптация
	// нужна, чтобы ОСЛАБИТЬ требования на плохом приёмнике, а не ужесточить
	// их на хорошем. С границей 15 м выброс на честном МКАД подрос с 3.1 % до 4.1 %.
	got := SigmaOf([]float64{3, 4, 4, 5}, []bool{true, true, true, true})
	assert.Equal(t, SigmaMinM, got)
}

func TestSigmaOf_PoorReceiverWidensSigma(t *testing.T) {
	// Медиана 20 м → σ = 40 м: у прибора разброс больше, придирки ослабляем.
	got := SigmaOf([]float64{18, 20, 20, 22}, []bool{true, true, true, true})
	assert.InDelta(t, 40.0, got, 1e-9)
}

func TestSigmaOf_CappedFromAbove(t *testing.T) {
	// Медиана 76 м дала бы 152 м — на сплошь заспуфленном треке это узаконило
	// бы спуфинг. Потолок 60 м.
	got := SigmaOf([]float64{70, 76, 80, 300}, []bool{true, true, true, true})
	assert.Equal(t, SigmaMaxM, got)
}

func TestSigmaOf_IgnoresUnanswered(t *testing.T) {
	// Неотвеченные точки в оценку не входят вовсе — иначе ноль вместо
	// «неизвестно» занизил бы медиану.
	snaps := []float64{0, 20, 0, 20, 0}
	ok := []bool{false, true, false, true, false}
	assert.InDelta(t, 40.0, SigmaOf(snaps, ok), 1e-9)
}

func TestSigmaOf_ShorterOkSliceIsSafe(t *testing.T) {
	assert.NotPanics(t, func() { SigmaOf([]float64{5, 6, 7}, []bool{true}) })
}

// ------------------------------------------------------------------- Weight

func TestWeight_OnRoadIsFullTrust(t *testing.T) {
	assert.InDelta(t, 1.0, Weight(0, true, SnapSigmaM), 1e-9,
		"точка ровно на дороге — полное доверие")
}

func TestWeight_UnansweredIsNeutral(t *testing.T) {
	// Ноль означал бы «на дороге», то есть довод ЗА точку. Молчание OSRM
	// не довод ни за, ни против — ровно ноль веса.
	assert.Zero(t, Weight(0, false, SnapSigmaM))
	assert.Zero(t, Weight(999, false, SnapSigmaM))
}

func TestWeight_CrossesZeroNearThirtyMeters(t *testing.T) {
	// Свойство, на котором держится вся цепочка: вес обязан уходить в МИНУС.
	// При любых положительных весах выгодно взять всё, до чего дотягивается
	// физика, и цепочка вбирала бы спуфинг.
	assert.Positive(t, Weight(25, true, SnapSigmaM))
	assert.Negative(t, Weight(35, true, SnapSigmaM))

	// Ноль лежит примерно на тридцати метрах.
	assert.InDelta(t, 0.0, Weight(29.4, true, SnapSigmaM), 0.05)
}

func TestWeight_DecreasesMonotonically(t *testing.T) {
	// Строго убывает там, где решение ещё принимается — до полутора сотен
	// метров. Дальше вес прижимается к своему пределу −SnapPenalty, и разница
	// между 480 и 485 метрами уже за пределами точности чисел: обе точки
	// одинаково безнадёжны, и различать их не нужно.
	prev := math.Inf(1)
	for snap := 0.0; snap <= 150; snap += 5 {
		w := Weight(snap, true, SnapSigmaM)
		assert.Less(t, w, prev, "вес обязан убывать с расстоянием: снэп %.0f", snap)
		prev = w
	}

	// А на всём диапазоне — хотя бы не растёт.
	prev = math.Inf(1)
	for snap := 0.0; snap <= 2000; snap += 5 {
		w := Weight(snap, true, SnapSigmaM)
		assert.LessOrEqual(t, w, prev, "вес не должен расти: снэп %.0f", snap)
		prev = w
	}
}

func TestWeight_BoundedFromBelow(t *testing.T) {
	// Как бы далеко точка ни была, штраф ограничен: иначе одна дикая точка
	// перевесила бы весь трек.
	for _, snap := range []float64{1e3, 1e5, 1e9} {
		w := Weight(snap, true, SnapSigmaM)
		assert.GreaterOrEqual(t, w, -SnapPenalty-1e-9, "снэп %.0f", snap)
		assert.InDelta(t, -SnapPenalty, w, 1e-6)
	}
}

func TestWeight_WiderSigmaIsMoreForgiving(t *testing.T) {
	// Один и тот же снэп на плохом приёмнике должен наказываться слабее.
	tight := Weight(50, true, SigmaMinM)
	loose := Weight(50, true, SigmaMaxM)
	assert.Less(t, tight, loose, "при большей σ тот же снэп менее подозрителен")
}

func TestWeight_NonPositiveSigmaDoesNotExplode(t *testing.T) {
	// Защита от деления на ноль: σ приходит из оценки, и в вырожденном
	// случае она может оказаться нулевой.
	assert.NotPanics(t, func() {
		w := Weight(10, true, 0)
		assert.False(t, math.IsNaN(w), "вес не должен быть NaN")
		assert.False(t, math.IsInf(w, 0), "вес не должен быть бесконечным")
	})
}

// ------------------------------------------------------------------- Smooth

// track builds n points with the given time step, all at the same place.
func evenTrack(n, stepSec int) []geo.Point {
	out := make([]geo.Point, n)
	for i := range out {
		out[i] = at(i*stepSec, 10.0, 0.0)
	}
	return out
}

func TestSmooth_EmptyAndSingle(t *testing.T) {
	assert.Empty(t, Smooth(nil, nil, SmoothWindow))
	assert.Equal(t, []float64{0.5}, Smooth([]float64{0.5}, evenTrack(1, 60), SmoothWindow))
}

func TestSmooth_LoneGoodPointAmongBadIsPulledDown(t *testing.T) {
	// Ровно случай Шереметьева: спуфинг-облако случайно накрыло дорогу, и одна
	// его точка получила полный вес. Одинокая хорошая точка среди плохих —
	// совпадение, а не свидетельство.
	w := []float64{-1, -1, -1, 1, -1, -1, -1}
	got := Smooth(w, evenTrack(7, 30), SmoothWindow)
	assert.InDelta(t, -1.0, got[3], 1e-9, "одинокий плюс среди минусов не выживает")
}

func TestSmooth_DenseRecordingWidensWindowByTime(t *testing.T) {
	// Точки капают раз в пять секунд. Окно в ±2 соседа (10 секунд) вырождается
	// и не перебивает пятёрку случайно легших на дорогу точек — так на
	// `ab681145` в цепочке остались изолированные точки посреди поля.
	// Окно ±60 секунд захватывает весь кусок.
	w := make([]float64, 41)
	for i := range w {
		w[i] = -1 // всё поле
	}
	for i := 18; i <= 22; i++ {
		w[i] = 1 // пятёрка, случайно легшая на грунтовку
	}

	got := Smooth(w, evenTrack(41, 5), SmoothWindow)
	assert.InDelta(t, -1.0, got[20], 1e-9,
		"при записи раз в пять секунд окно обязано быть шире пяти точек")
}

func TestSmooth_SparseRecordingKeepsMinimumNeighbours(t *testing.T) {
	// Обратный случай: запись раз в десять минут, окно по времени не
	// захватывает никого. Соседей берём минимум по счёту, иначе сглаживание
	// выродится в тождество и перестанет работать вовсе.
	w := []float64{-1, -1, 1, -1, -1}
	got := Smooth(w, evenTrack(5, 600), SmoothWindow)
	assert.InDelta(t, -1.0, got[2], 1e-9, "±2 соседа берём даже при редкой записи")
}

func TestSmooth_UsesMedianNotMean(t *testing.T) {
	// На стыке честного участка с глючным среднее утягивает вниз последние
	// правильные точки, медиана их удерживает.
	w := []float64{1, 1, 1, -10, -10}
	got := Smooth(w, evenTrack(5, 30), SmoothWindow)
	assert.Greater(t, got[1], 0.0, "среднее дало бы минус, медиана держит плюс")
}

func TestSmooth_LimitsNeighbourCount(t *testing.T) {
	// При очень частой записи окно по времени захватило бы сотни точек, и
	// медиана размазала бы весь трек. Потолок — SmoothMaxNeighbours в каждую сторону.
	n := 500
	w := make([]float64, n)
	for i := range w {
		w[i] = -1
	}
	// Плюсовой участок шире потолка соседей: он обязан уцелеть.
	for i := 200; i < 300; i++ {
		w[i] = 1
	}
	got := Smooth(w, evenTrack(n, 1), SmoothWindow) // запись раз в секунду

	assert.InDelta(t, 1.0, got[250], 1e-9,
		"участок шире окна не должен быть размазан соседями")
}

func TestSmooth_ZeroWindowFallsBackToNeighbourCount(t *testing.T) {
	w := []float64{-1, -1, 1, -1, -1}
	got := Smooth(w, evenTrack(5, 30), 0)
	assert.InDelta(t, -1.0, got[2], 1e-9, "с выключенным окном работает счёт соседей")
}

func TestSmooth_NilPointsFallsBackToNeighbourCount(t *testing.T) {
	w := []float64{-1, -1, 1, -1, -1}
	assert.NotPanics(t, func() {
		got := Smooth(w, nil, SmoothWindow)
		assert.InDelta(t, -1.0, got[2], 1e-9)
	})
}

func TestSmooth_LengthMismatchIsSafe(t *testing.T) {
	assert.NotPanics(t, func() {
		got := Smooth([]float64{1, 2, 3}, evenTrack(1, 60), SmoothWindow)
		assert.Len(t, got, 3)
	})
}

func TestSmooth_KeepsLength(t *testing.T) {
	w := []float64{1, -1, 0.5, -0.5, 0}
	assert.Len(t, Smooth(w, evenTrack(5, 30), SmoothWindow), len(w))
}

// ------------------------------------------------------------ PointWeights

func TestPointWeights_SmoothingOnlyLowers(t *testing.T) {
	// Сглаживание работает ТОЛЬКО НА ПОНИЖЕНИЕ. Само по себе оно симметрично и
	// так же вытягивает наверх одинокую ПЛОХУЮ точку среди хороших: на
	// `749dc894` у Фенино так выживала точка со снэпом 132 м — сырой вес −1.00,
	// после сглаживания +0.98. Проверка дорисовкой показала, что это не
	// косметика: маршрут через неё давал двенадцать лишних километров.
	snaps := []float64{2, 2, 2, 132, 2, 2, 2}
	ok := []bool{true, true, true, true, true, true, true}

	got := PointWeights(evenTrack(7, 30), snaps, ok)
	assert.Negative(t, got[3],
		"плохая точка среди хороших обязана остаться плохой")
}

func TestPointWeights_LoneGoodAmongBadIsLowered(t *testing.T) {
	snaps := []float64{300, 300, 300, 2, 300, 300, 300}
	ok := make([]bool, 7)
	for i := range ok {
		ok[i] = true
	}
	got := PointWeights(evenTrack(7, 30), snaps, ok)
	assert.Negative(t, got[3], "одинокая хорошая точка среди плохих — совпадение")
}

func TestPointWeights_NeverAboveRaw(t *testing.T) {
	// Свойство целиком: сглаженный вес не может превысить сырой ни в одной точке.
	snaps := []float64{1, 400, 3, 250, 5, 180, 7, 90, 9, 40}
	ok := make([]bool, len(snaps))
	for i := range ok {
		ok[i] = true
	}
	pts := evenTrack(len(snaps), 20)
	sigma := SigmaOf(snaps, ok)

	got := PointWeights(pts, snaps, ok)
	require.Len(t, got, len(snaps))
	for i := range got {
		raw := Weight(snaps[i], ok[i], sigma)
		assert.LessOrEqual(t, got[i], raw+1e-9,
			"точка %d: итоговый вес выше сырого", i)
	}
}

func TestPointWeights_AllUnansweredAreZero(t *testing.T) {
	// Полное молчание OSRM. Веса нули — и это как раз тот случай, из-за
	// которого цепочка не связывается вовсе; выше по течению стоит проверка,
	// не дающая запустить ядро без снэпов.
	pts := evenTrack(5, 30)
	got := PointWeights(pts, make([]float64, 5), make([]bool, 5))
	for i, w := range got {
		assert.Zero(t, w, "точка %d", i)
	}
}

func TestPointWeights_EmptyAndMismatched(t *testing.T) {
	assert.Empty(t, PointWeights(nil, nil, nil))
	assert.NotPanics(t, func() {
		got := PointWeights(evenTrack(3, 30), []float64{5}, []bool{true})
		assert.Len(t, got, 3, "длина результата равна числу точек")
	})
}

func TestPointWeights_MatchesManualComposition(t *testing.T) {
	// PointWeights обязана быть ровно композицией трёх шагов, а не чем-то
	// своим: сигма из трека, сырой вес, минимум со сглаженным.
	snaps := []float64{3, 8, 150, 12, 5, 400, 6}
	ok := make([]bool, len(snaps))
	for i := range ok {
		ok[i] = true
	}
	pts := evenTrack(len(snaps), 25)

	sigma := SigmaOf(snaps, ok)
	raw := make([]float64, len(snaps))
	for i := range snaps {
		raw[i] = Weight(snaps[i], ok[i], sigma)
	}
	sm := Smooth(raw, pts, SmoothWindow)

	got := PointWeights(pts, snaps, ok)
	for i := range got {
		assert.InDelta(t, math.Min(raw[i], sm[i]), got[i], 1e-12, "точка %d", i)
	}
}

func BenchmarkPointWeights(b *testing.B) {
	const n = 50000
	pts := evenTrack(n, 5)
	snaps := make([]float64, n)
	ok := make([]bool, n)
	for i := range snaps {
		snaps[i] = float64(i%200) / 2
		ok[i] = true
	}
	b.ReportAllocs()
	for b.Loop() {
		PointWeights(pts, snaps, ok)
	}
}

var _ = time.Second

func TestMedianInPlace_SortsInput(t *testing.T) {
	// Эта версия переставляет вход намеренно — вызывающий обязан передавать
	// копию. Тест фиксирует контракт, чтобы его случайно не нарушили.
	in := []float64{5, 1, 3}
	assert.Equal(t, 3.0, medianInPlace(in))
	assert.Equal(t, []float64{1, 3, 5}, in)
}
