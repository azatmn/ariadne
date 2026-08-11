package pipeline

import (
	"context"
	"testing"
	"time"

	"ariadne/internal/core"
	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Тесты стража достижимости.
//
// Упаковка судит по геометрии и про физику не знает. Страж проверяет не
// «пачки» и не «стоянки», а само условие достижимости — то же, что использует
// ядро, — и возвращает точки там, где переход стал невозможным.

// at2 — точка через sec секунд от начала отсчёта стадий.
func at2(sec int, lon, lat float64) geo.Point {
	return geo.Point{Time: stageT0.Add(time.Duration(sec) * time.Second), Lon: lon, Lat: lat}
}

// packed — состояние прогона со снимком трека до упаковки.
func packed(before []geo.Point) *RunState {
	return &RunState{BeforePacking: before}
}

// isSubsequence — является ли got подпоследовательностью full.
func isSubsequence(got, full []geo.Point) bool {
	k := 0
	for _, p := range got {
		for k < len(full) && full[k] != p {
			k++
		}
		if k == len(full) {
			return false
		}
		k++
	}
	return true
}

// ------------------------------------------------------------------ основное

func TestGuard_Name(t *testing.T) {
	assert.Equal(t, "reachability_guard", ReachabilityGuard{}.Name())
}

func TestGuard_RestoresImpossibleTransition(t *testing.T) {
	// Настоящий случай с `da72a9aa`: трекер выгрузил буфер пачкой, поставив
	// десятку записей ОДНО время. Ядро провело цепочку через всю пачку —
	// каждый шаг внутри допуска. Упрощение увидело, что промежуточные точки
	// лежат на прямой, и сняло их, а между оставшимися оказалось 450 метров
	// за ноль секунд.
	before := []geo.Point{
		at2(0, 10.0000, 0),
		at2(60, 10.0000, 0),
		at2(60, 10.0020, 0), // те же 60 секунд
		at2(60, 10.0040, 0),
		at2(60, 10.0060, 0),
		at2(120, 10.0080, 0),
	}
	// Упаковка сняла середину пачки: между 10.0000 и 10.0060 в один и тот же
	// момент времени 667 метров.
	after := []geo.Point{before[0], before[1], before[4], before[5]}

	st := packed(before)
	got, warns, err := ReachabilityGuard{State: st}.Apply(context.Background(), after)
	require.NoError(t, err)

	assert.Equal(t, before, got, "промежуток обязан вернуться целиком")
	assert.Equal(t, 2, st.Guarded, "вернули две точки")
	assert.NotEmpty(t, warns, "о возврате надо сказать: это чинится не здесь")
}

func TestGuard_LeavesPossibleTransitionsAlone(t *testing.T) {
	// Честный перегон упаковка сжимает законно, и трогать это нельзя:
	// возвращать точки «на всякий случай» значит отменять упаковку.
	before := road(50, 60, 10, 0, 0.01, 0)
	after := []geo.Point{before[0], before[20], before[49]}

	st := packed(before)
	got, warns, err := ReachabilityGuard{State: st}.Apply(context.Background(), after)
	require.NoError(t, err)

	assert.Equal(t, after, got, "возможные переходы трогать не за что")
	assert.Zero(t, st.Guarded)
	assert.Empty(t, warns)
}

func TestGuard_RestoresOnlyBrokenSpans(t *testing.T) {
	// Возвращаем ровно те промежутки, где переход невозможен, а не весь трек.
	before := []geo.Point{
		at2(0, 10.00, 0),
		at2(600, 10.05, 0), // законно: 5.5 км за 10 минут
		at2(1200, 10.10, 0),
		at2(1200, 10.13, 0), // пачка с одним временем
		at2(1200, 10.16, 0),
		at2(1800, 10.20, 0),
	}
	after := []geo.Point{before[0], before[2], before[4], before[5]}

	st := packed(before)
	got, _, err := ReachabilityGuard{State: st}.Apply(context.Background(), after)
	require.NoError(t, err)

	// Между 0 и 2 переход законен — точка 1 не возвращается.
	// Между 2 и 4 — 3.3 км за ноль секунд, возвращается точка 3.
	want := []geo.Point{before[0], before[2], before[3], before[4], before[5]}
	assert.Equal(t, want, got)
	assert.Equal(t, 1, st.Guarded)
}

func TestGuard_SeveralBrokenSpans(t *testing.T) {
	// Разрывов может быть несколько, и каждый чинится сам по себе.
	before := []geo.Point{
		at2(0, 10.00, 0),
		at2(0, 10.02, 0),
		at2(0, 10.04, 0),
		at2(600, 10.10, 0),
		at2(1200, 10.20, 0),
		at2(1200, 10.22, 0),
		at2(1200, 10.24, 0),
	}
	after := []geo.Point{before[0], before[2], before[3], before[4], before[6]}

	st := packed(before)
	got, _, err := ReachabilityGuard{State: st}.Apply(context.Background(), after)
	require.NoError(t, err)

	assert.Equal(t, before, got, "оба разрыва обязаны закрыться")
	assert.Equal(t, 2, st.Guarded)
}

func TestGuard_NothingBetweenToRestore(t *testing.T) {
	// Переход невозможен, но между точками ничего и не было: упаковка тут ни
	// при чём, чинить нечем. Молча оставляем как есть — выдумывать точки
	// страж не имеет права.
	before := []geo.Point{
		at2(0, 10.00, 0),
		at2(0, 10.20, 0), // 22 км за ноль секунд, соседи по исходному треку
	}
	after := before

	st := packed(before)
	got, _, err := ReachabilityGuard{State: st}.Apply(context.Background(), after)
	require.NoError(t, err)

	assert.Equal(t, before, got)
	assert.Zero(t, st.Guarded)
}

func TestGuard_ResultStaysSubsequence(t *testing.T) {
	// Страж только ВОЗВРАЩАЕТ точки из снимка. Выдумать точку или переставить
	// порядок он не может — иначе километраж поедет.
	before := []geo.Point{
		at2(0, 10.00, 0), at2(0, 10.02, 0), at2(0, 10.04, 0),
		at2(600, 10.10, 0), at2(1200, 10.20, 0),
	}
	after := []geo.Point{before[0], before[2], before[3], before[4]}

	got, _, err := ReachabilityGuard{State: packed(before)}.Apply(context.Background(), after)
	require.NoError(t, err)

	assert.True(t, isSubsequence(got, before), "результат обязан лежать внутри снимка")
	assert.True(t, isSubsequence(after, got), "уже оставленное выбрасывать нельзя")
}

// -------------------------------------------------------- вырожденные случаи

func TestGuard_DegenerateInput(t *testing.T) {
	before := road(10, 60, 10, 0, 0.01, 0)

	cases := []struct {
		name  string
		state *RunState
		in    []geo.Point
	}{
		{"нет блокнота", nil, before[:3]},
		{"пустой снимок", &RunState{}, before[:3]},
		{"пустой вход", packed(before), nil},
		{"одна точка", packed(before), before[:1]},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _, err := ReachabilityGuard{State: c.state}.Apply(context.Background(), c.in)
			require.NoError(t, err)
			assert.Equal(t, c.in, got, "трогать нечего")
		})
	}
}

func TestGuard_InputNotFromSnapshot(t *testing.T) {
	// Вход не является подпоследовательностью снимка — значит снимок не от
	// этого прогона. Гадать нельзя: пропускаем как есть.
	before := road(10, 60, 10, 0, 0.01, 0)
	alien := []geo.Point{at2(0, 99, 99), at2(60, 99.5, 99)}

	st := packed(before)
	got, warns, err := ReachabilityGuard{State: st}.Apply(context.Background(), alien)
	require.NoError(t, err)

	assert.Equal(t, alien, got)
	assert.Zero(t, st.Guarded)
	assert.NotEmpty(t, warns, "о нестыковке надо сказать")
}

func TestGuard_DoesNotMutateInput(t *testing.T) {
	before := []geo.Point{
		at2(0, 10.00, 0), at2(0, 10.02, 0), at2(0, 10.04, 0), at2(600, 10.10, 0),
	}
	after := []geo.Point{before[0], before[2], before[3]}
	copyAfter := append([]geo.Point{}, after...)
	copyBefore := append([]geo.Point{}, before...)

	_, _, err := ReachabilityGuard{State: packed(before)}.Apply(context.Background(), after)
	require.NoError(t, err)

	assert.Equal(t, copyAfter, after, "входной срез трогать нельзя")
	assert.Equal(t, copyBefore, before, "снимок трогать нельзя")
}

// --------------------------------------------------------- условие то же, что в ядре

func TestGuard_UsesSameReachabilityAsCore(t *testing.T) {
	// Страж обязан судить ровно тем же условием, что и ядро. Иначе он либо
	// начнёт возвращать то, что ядро выбросило осознанно, либо пропустит
	// разрыв, который ядро считает невозможным.
	//
	// Проверяем на границе допуска: `ChainSlackM` метров за секунду — ещё
	// законно, вдвое дальше — уже нет.
	slack := core.ChainSlackM

	okPair := []geo.Point{at2(0, 10, 0), at2(1, 10+slack/111320.0, 0)}
	badPair := []geo.Point{at2(0, 10, 0), at2(1, 10+2*slack/111320.0, 0)}

	require.True(t, core.Reachable(okPair[0], okPair[1], nil), "опыт построен неверно")
	require.False(t, core.Reachable(badPair[0], badPair[1], nil), "опыт построен неверно")

	for _, c := range []struct {
		name    string
		pair    []geo.Point
		restore bool
	}{
		{"в допуске", okPair, false},
		{"за допуском", badPair, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			mid := geo.Point{
				Time: c.pair[0].Time,
				Lon:  (c.pair[0].Lon + c.pair[1].Lon) / 2,
				Lat:  0,
			}
			before := []geo.Point{c.pair[0], mid, c.pair[1]}

			st := packed(before)
			got, _, err := ReachabilityGuard{State: st}.Apply(context.Background(), c.pair)
			require.NoError(t, err)

			if c.restore {
				assert.Equal(t, before, got, "невозможный переход обязан чиниться")
				return
			}
			assert.Equal(t, c.pair, got, "возможный переход трогать не за что")
		})
	}
}

func BenchmarkGuard(b *testing.B) {
	before := road(50000, 30, 10, 0, 0.0005, 0)
	after := make([]geo.Point, 0, len(before)/3)
	for i := 0; i < len(before); i += 3 {
		after = append(after, before[i])
	}
	st := packed(before)

	b.ReportAllocs()
	for b.Loop() {
		ReachabilityGuard{State: st}.Apply(context.Background(), after)
	}
}

func TestGuard_RepeatedPointsKeepTheirPlaces(t *testing.T) {
	// Трекер повторяет одну и ту же запись байт в байт, и в снимке такие точки
	// неразличимы по значению. Позиции поэтому ищутся ходом двумя указателями,
	// а не поиском «где-нибудь»: иначе вторая копия найдётся на месте первой,
	// границы промежутка съедут, и страж вернёт точку, которая никуда не
	// девалась, — то есть создаст дубликат на ровном месте.
	same := at2(0, 10.00, 0)
	far := at2(0, 10.20, 0) // 22 км за ноль секунд — переход невозможен

	before := []geo.Point{same, same, far}
	after := []geo.Point{same, same, far}

	st := packed(before)
	got, _, err := ReachabilityGuard{State: st}.Apply(context.Background(), after)
	require.NoError(t, err)

	assert.Equal(t, after, got, "возвращать нечего: между точками ничего не снимали")
	assert.Zero(t, st.Guarded)
}

func TestGuard_RestoresAroundRepeatedPoints(t *testing.T) {
	// То же, но снятое всё-таки было: повторы не должны мешать вернуть его
	// ровно один раз.
	same := at2(0, 10.00, 0)
	mid := at2(0, 10.10, 0)
	far := at2(0, 10.20, 0)

	before := []geo.Point{same, same, mid, far}
	after := []geo.Point{same, same, far}

	st := packed(before)
	got, _, err := ReachabilityGuard{State: st}.Apply(context.Background(), after)
	require.NoError(t, err)

	assert.Equal(t, before, got)
	assert.Equal(t, 1, st.Guarded)
}
