package core

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Сверка с прототипом на настоящих треках.
//
// Тесты выше проверяют, что реализация делает то, что я задумал. Здесь
// проверяется другое и более важное: что она делает то же самое, что делает
// отлаженный Python. Разница видна только на живых данных — синтетический трек
// не порождает ни залипаний, ни выгрузок буфера, ни медленного дрейфа.
//
// Файлы в testdata выгружены прототипом (`stops.py`) на округлённых до пяти
// знаков координатах — ровно на тех числах, которые читает Go, иначе сравнение
// шло бы по разным входам.

// goldenTrack — трек и найденные на нём прототипом стоянки.
type goldenTrack struct {
	UID    string       `json:"uid"`
	From   int          `json:"from"`
	To     int          `json:"to"`
	Points [][3]float64 `json:"points"` // unix-секунды, долгота, широта
	Stops  [][2]int     `json:"stops"`
}

func (g goldenTrack) points() []geo.Point {
	pts := make([]geo.Point, len(g.Points))
	for i, p := range g.Points {
		pts[i] = geo.Point{
			Time: time.Unix(int64(p[0]), 0).UTC(),
			Lon:  p[1],
			Lat:  p[2],
		}
	}
	return pts
}

// Векторы лежат сжатыми: самый большой трек в исходном виде занимает 3.6 МБ,
// а держать такое в истории репозитория незачем — распаковка стоит миллисекунды.
func loadGolden(t *testing.T) []goldenTrack {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "stops_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "золотые векторы не найдены")

	out := make([]goldenTrack, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err, "открытие %s", p)

		zr, err := gzip.NewReader(f)
		require.NoError(t, err, "распаковка %s", p)

		var g goldenTrack
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)

		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

// Главная сверка: те же интервалы стоянок, что у прототипа, без единого
// расхождения. Допуска здесь нет и быть не может — это множество индексов.
func TestGolden_FindStopsMatchesPrototype(t *testing.T) {
	for _, g := range loadGolden(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := g.points()
			got := FindStops(pts, StopRadiusM, StopMinStay)

			require.Len(t, got, len(g.Stops),
				"число стоянок разошлось: Go %d, прототип %d", len(got), len(g.Stops))

			for i, want := range g.Stops {
				assert.Equal(t, want[0], got[i].Start,
					"стоянка %d: начало разошлось (точка %d против %d)", i, got[i].Start, want[0])
				assert.Equal(t, want[1], got[i].End,
					"стоянка %d: конец разошёлся (точка %d против %d)", i, got[i].End, want[1])
			}

			t.Logf("%s: %d точек, %d стоянок — совпало", g.UID, len(pts), len(got))
		})
	}
}

// Свойства, которые обязаны выполняться на любых настоящих данных.
// Тесты выше проверяют их на выдуманных треках; здесь — на живых.
func TestGolden_StopInvariants(t *testing.T) {
	for _, g := range loadGolden(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := g.points()
			stops := FindStops(pts, StopRadiusM, StopMinStay)

			prevEnd := -1
			for i, s := range stops {
				assert.GreaterOrEqual(t, s.Start, 0)
				assert.Less(t, s.End, len(pts))
				assert.Less(t, s.Start, s.End, "стоянка %d вырождена в точку", i)
				assert.Greater(t, s.Start, prevEnd,
					"стоянка %d налезает на предыдущую", i)
				prevEnd = s.End

				dur := pts[s.End].Time.Sub(pts[s.Start].Time)
				assert.GreaterOrEqual(t, dur, StopMinStay,
					"стоянка %d короче порога: %s", i, dur)
			}
		})
	}
}

// Доверие стоянкам считается без обращения к сети, поэтому его тоже можно
// сверить на живых данных. Здесь проверяется не совпадение с прототипом
// (для него нужны снэпы), а осмысленность: доверенных должно быть меньше, чем
// всех, и каждая доверенная обязана удовлетворять своим условиям.
func TestGolden_TrustedStopsAreSubset(t *testing.T) {
	for _, g := range loadGolden(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := g.points()
			stops := FindStops(pts, StopRadiusM, StopMinStay)

			trusted, frozen := 0, 0
			for _, s := range stops {
				if IsFrozen(pts, s) {
					frozen++
				}
				if !TrustedStop(pts, s, 0, false) {
					continue
				}
				trusted++
				// Доверенная стоянка обязана быть длинной и дрожащей.
				assert.GreaterOrEqual(t, pts[s.End].Time.Sub(pts[s.Start].Time), StopTrustStay)
				assert.GreaterOrEqual(t, spanMeters(pts, s), StopSpanMinM)
				assert.False(t, IsFrozen(pts, s), "залипание не может быть доверенным")
			}
			assert.LessOrEqual(t, trusted, len(stops))
			t.Logf("%s: стоянок %d, доверенных %d, залипаний %d",
				g.UID, len(stops), trusted, frozen)
		})
	}
}

// Снэп влияет на доверие только в одну сторону: чем дальше от дороги, тем
// меньше доверия. Проверяем, что порог работает и что молчание OSRM не строже
// известного хорошего снэпа.
func TestGolden_SnapOnlyRestrictsTrust(t *testing.T) {
	for _, g := range loadGolden(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := g.points()
			for i, s := range FindStops(pts, StopRadiusM, StopMinStay) {
				silent := TrustedStop(pts, s, 0, false)
				good := TrustedStop(pts, s, 10, true)
				bad := TrustedStop(pts, s, StopMaxSnapM+1, true)

				assert.Equal(t, silent, good,
					"стоянка %d: хороший снэп не должен менять вердикт молчания", i)
				assert.False(t, bad,
					"стоянка %d: дальше %.0f м от дороги доверия быть не может",
					i, StopMaxSnapM)
			}
		})
	}
}

// Сопоставление «точка → стоянка» на живых данных: ни одна точка не может
// принадлежать двум стоянкам, и все точки интервалов покрыты.
func TestGolden_StopOwnerCoversRanges(t *testing.T) {
	for _, g := range loadGolden(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := g.points()
			stops := FindStops(pts, StopRadiusM, StopMinStay)
			owner := StopOwner(len(pts), stops)

			// Проверяем напрямую, а не через assert в цикле: на стоянке в
			// 91 тысячу точек рефлексия testify превращает проверку в квадрат
			// (замер: 178 секунд против доли секунды).
			covered := 0
			var missing, wrong int
			for k, s := range stops {
				for i := s.Start; i <= s.End; i++ {
					got, in := owner[i]
					switch {
					case !in:
						missing++
					case got != k:
						wrong++
					}
					covered++
				}
			}
			assert.Zero(t, missing, "точек стоянок не попало в сопоставление")
			assert.Zero(t, wrong, "точки приписаны не той стоянке")
			assert.Len(t, owner, covered, "лишних точек в сопоставлении быть не должно")
		})
	}
}

// goldenWeights — снэпы с настоящего OSRM и посчитанные прототипом веса.
type goldenWeights struct {
	UID    string       `json:"uid"`
	Points [][3]float64 `json:"points"`
	Snaps  []*float64   `json:"snaps"` // null = OSRM промолчал
	Sigma  float64      `json:"sigma"`
	Raw    []float64    `json:"raw"`
	Final  []float64    `json:"final"`
}

func loadGoldenWeights(t *testing.T) []goldenWeights {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "weights_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "золотые векторы весов не найдены")

	out := make([]goldenWeights, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err)
		zr, err := gzip.NewReader(f)
		require.NoError(t, err)
		var g goldenWeights
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)
		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

func (g goldenWeights) points() []geo.Point {
	pts := make([]geo.Point, len(g.Points))
	for i, p := range g.Points {
		pts[i] = geo.Point{Time: time.Unix(int64(p[0]), 0).UTC(), Lon: p[1], Lat: p[2]}
	}
	return pts
}

// snapSlices превращает null-able снэпы прототипа в пару (значение, отвечен ли).
func (g goldenWeights) snapSlices() ([]float64, []bool) {
	snaps := make([]float64, len(g.Snaps))
	ok := make([]bool, len(g.Snaps))
	for i, s := range g.Snaps {
		if s != nil {
			snaps[i], ok[i] = *s, true
		}
	}
	return snaps, ok
}

// Веса — числа с плавающей точкой, поэтому сверяем с допуском. Он взят с
// запасом к округлению выгрузки (12 знаков) и к возможной разнице в последнем
// бите между реализациями экспоненты.
const weightEps = 1e-9

func TestGolden_SigmaMatchesPrototype(t *testing.T) {
	for _, g := range loadGoldenWeights(t) {
		t.Run(g.UID, func(t *testing.T) {
			snaps, ok := g.snapSlices()
			assert.InDelta(t, g.Sigma, SigmaOf(snaps, ok), 1e-12,
				"оценка точности прибора разошлась")
		})
	}
}

func TestGolden_RawWeightsMatchPrototype(t *testing.T) {
	for _, g := range loadGoldenWeights(t) {
		t.Run(g.UID, func(t *testing.T) {
			snaps, ok := g.snapSlices()
			sigma := SigmaOf(snaps, ok)
			require.Len(t, g.Raw, len(snaps))

			worst, at := 0.0, -1
			for i := range snaps {
				got := Weight(snaps[i], ok[i], sigma)
				if d := math.Abs(got - g.Raw[i]); d > worst {
					worst, at = d, i
				}
			}
			assert.Less(t, worst, weightEps,
				"наибольшее расхождение сырого веса %g в точке %d", worst, at)
			t.Logf("%s: сырые веса, наибольшее расхождение %g", g.UID, worst)
		})
	}
}

// Главная сверка по весам: итог после сглаживания на понижение. Здесь
// складываются все три шага сразу, и любая ошибка в любом из них видна.
func TestGolden_FinalWeightsMatchPrototype(t *testing.T) {
	for _, g := range loadGoldenWeights(t) {
		t.Run(g.UID, func(t *testing.T) {
			snaps, ok := g.snapSlices()
			got := PointWeights(g.points(), snaps, ok)
			require.Len(t, got, len(g.Final))

			worst, at := 0.0, -1
			for i := range got {
				if d := math.Abs(got[i] - g.Final[i]); d > worst {
					worst, at = d, i
				}
			}
			assert.Less(t, worst, weightEps,
				"наибольшее расхождение итогового веса %g в точке %d "+
					"(Go %.12f, прототип %.12f)", worst, at, got[at], g.Final[at])
			t.Logf("%s: итоговые веса на %d точках, наибольшее расхождение %g",
				g.UID, len(got), worst)
		})
	}
}

// Знак веса решает, брать точку или обходить. Даже если числа разойдутся в
// последних битах, знак обязан совпадать всюду — иначе цепочка пойдёт иначе.
func TestGolden_WeightSignsMatchExactly(t *testing.T) {
	for _, g := range loadGoldenWeights(t) {
		t.Run(g.UID, func(t *testing.T) {
			snaps, ok := g.snapSlices()
			got := PointWeights(g.points(), snaps, ok)

			var flipped, borderline int
			for i := range got {
				if (got[i] > 0) != (g.Final[i] > 0) {
					flipped++
				}
				if math.Abs(g.Final[i]) < 1e-6 {
					borderline++
				}
			}
			assert.Zero(t, flipped, "знак веса разошёлся у %d точек", flipped)
			t.Logf("%s: точек ровно на границе доверия — %d", g.UID, borderline)
		})
	}
}

// goldenRules — какие точки уличили правила прототипа.
type goldenRules struct {
	UID     string       `json:"uid"`
	Points  [][3]float64 `json:"points"`
	Traps   []int        `json:"traps"`
	Islands []int        `json:"islands"`
}

func loadGoldenRules(t *testing.T) []goldenRules {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "rules_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "золотые векторы правил не найдены")

	out := make([]goldenRules, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err)
		zr, err := gzip.NewReader(f)
		require.NoError(t, err)
		var g goldenRules
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)
		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

func (g goldenRules) points() []geo.Point {
	pts := make([]geo.Point, len(g.Points))
	for i, p := range g.Points {
		pts[i] = geo.Point{Time: time.Unix(int64(p[0]), 0).UTC(), Lon: p[1], Lat: p[2]}
	}
	return pts
}

// diffSets возвращает, чего Go нашёл лишнего и чего недосчитался.
func diffSets(got map[int]struct{}, want []int) (extra, missing []int) {
	w := make(map[int]struct{}, len(want))
	for _, i := range want {
		w[i] = struct{}{}
	}
	for i := range got {
		if _, ok := w[i]; !ok {
			extra = append(extra, i)
		}
	}
	for _, i := range want {
		if _, ok := got[i]; !ok {
			missing = append(missing, i)
		}
	}
	slices.Sort(extra)
	slices.Sort(missing)
	return extra, missing
}

func TestGolden_TrapsMatchPrototype(t *testing.T) {
	for _, g := range loadGoldenRules(t) {
		t.Run(g.UID, func(t *testing.T) {
			extra, missing := diffSets(FindTraps(g.points()), g.Traps)
			assert.Empty(t, extra, "Go уличил лишние точки")
			assert.Empty(t, missing, "Go недосчитался уличённых точек")
			t.Logf("%s: ловушек %d — совпало", g.UID, len(g.Traps))
		})
	}
}

func TestGolden_IslandsMatchPrototype(t *testing.T) {
	for _, g := range loadGoldenRules(t) {
		t.Run(g.UID, func(t *testing.T) {
			extra, missing := diffSets(FindIslands(g.points()), g.Islands)
			assert.Empty(t, extra, "Go уличил лишние точки")
			assert.Empty(t, missing, "Go недосчитался уличённых точек")
			t.Logf("%s: островов %d — совпало", g.UID, len(g.Islands))
		})
	}
}

// goldenDual — точки, уличённые прототипом как слабый поток.
type goldenDual struct {
	UID    string       `json:"uid"`
	Points [][3]float64 `json:"points"`
	Dual   []int        `json:"dual"`
}

func loadGoldenDual(t *testing.T) []goldenDual {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "dual_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "золотые векторы двух потоков не найдены")

	out := make([]goldenDual, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err)
		zr, err := gzip.NewReader(f)
		require.NoError(t, err)
		var g goldenDual
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)
		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

func (g goldenDual) points() []geo.Point {
	pts := make([]geo.Point, len(g.Points))
	for i, p := range g.Points {
		pts[i] = geo.Point{Time: time.Unix(int64(p[0]), 0).UTC(), Lon: p[1], Lat: p[2]}
	}
	return pts
}

// Самая придирчивая из сверок: правило перебирает места, раздвигает границы
// эпизода и сравнивает доли времени. Ошибись хоть в одном шаге — множества
// разойдутся на сотни точек.
func TestGolden_DualMatchesPrototype(t *testing.T) {
	for _, g := range loadGoldenDual(t) {
		t.Run(g.UID, func(t *testing.T) {
			extra, missing := diffSets(FindDual(g.points()), g.Dual)
			assert.Empty(t, extra, "Go уличил лишние точки (первые: %v)", head(extra))
			assert.Empty(t, missing, "Go недосчитался точек (первые: %v)", head(missing))
			t.Logf("%s: уличено %d из %d — совпало", g.UID, len(g.Dual), len(g.Points))
		})
	}
}

func head(v []int) []int {
	if len(v) > 10 {
		return v[:10]
	}
	return v
}

// goldenSplit — точки, уличённые прототипом как раздвоение.
type goldenSplit struct {
	UID    string       `json:"uid"`
	Points [][3]float64 `json:"points"`
	Split  []int        `json:"split"`
}

func loadGoldenSplit(t *testing.T) []goldenSplit {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "split_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "золотые векторы раздвоения не найдены")

	out := make([]goldenSplit, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err)
		zr, err := gzip.NewReader(f)
		require.NoError(t, err)
		var g goldenSplit
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)
		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

func (g goldenSplit) points() []geo.Point {
	pts := make([]geo.Point, len(g.Points))
	for i, p := range g.Points {
		pts[i] = geo.Point{Time: time.Unix(int64(p[0]), 0).UTC(), Lon: p[1], Lat: p[2]}
	}
	return pts
}

// Раздвоение — самое многоярусное правило: скользящее окно, кластеры, возвраты,
// растяжка границ, доли слотов, повторные проходы. Ошибка в любом ярусе
// разъезжается на сотни точек, поэтому сверка здесь особенно ценна.
func TestGolden_SplitMatchesPrototype(t *testing.T) {
	for _, g := range loadGoldenSplit(t) {
		t.Run(g.UID, func(t *testing.T) {
			extra, missing := diffSets(FindSplit(g.points()), g.Split)
			assert.Empty(t, extra, "Go уличил лишние точки (первые: %v)", head(extra))
			assert.Empty(t, missing, "Go недосчитался точек (первые: %v)", head(missing))
			t.Logf("%s: уличено %d из %d — совпало", g.UID, len(g.Split), len(g.Points))
		})
	}
}

// goldenReorder — порядок точек, восстановленный прототипом.
type goldenReorder struct {
	UID    string       `json:"uid"`
	Points [][3]float64 `json:"points"`
	Perm   []int        `json:"perm"`
}

func loadGoldenReorder(t *testing.T) []goldenReorder {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "reorder_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "золотые векторы перестановки не найдены")

	out := make([]goldenReorder, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err)
		zr, err := gzip.NewReader(f)
		require.NoError(t, err)
		var g goldenReorder
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)
		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

func (g goldenReorder) points() []geo.Point {
	pts := make([]geo.Point, len(g.Points))
	for i, p := range g.Points {
		pts[i] = geo.Point{Time: time.Unix(int64(p[0]), 0).UTC(), Lon: p[1], Lat: p[2]}
	}
	return pts
}

func TestGolden_ReorderMatchesPrototype(t *testing.T) {
	for _, g := range loadGoldenReorder(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := g.points()
			got := ReorderBatches(pts)
			require.Len(t, got, len(g.Perm))

			var diff int
			firstAt := -1
			for i := range got {
				if got[i] != g.Perm[i] {
					diff++
					if firstAt < 0 {
						firstAt = i
					}
				}
			}
			assert.Zero(t, diff,
				"порядок разошёлся в %d позициях, первая — %d", diff, firstAt)

			// И отдельно: километраж после перестановки обязан совпасть.
			// Порядок внутри пачки мог бы отличаться при равных путях, но
			// длина трека от этого не изменится.
			goLen := pathOf(pts, got)
			pyLen := pathOf(pts, g.Perm)
			assert.InDelta(t, pyLen, goLen, 0.001,
				"километраж после перестановки разошёлся")

			moved := 0
			for i := range got {
				if got[i] != i {
					moved++
				}
			}
			t.Logf("%s: переставлено %d точек — совпало", g.UID, moved)
		})
	}
}

// goldenChain — веса на входе и цепочка, выбранная прототипом.
type goldenChain struct {
	UID     string       `json:"uid"`
	Points  [][3]float64 `json:"points"`
	Weights []float64    `json:"weights"`
	Chain   []int        `json:"chain"`
}

func loadGoldenChain(t *testing.T) []goldenChain {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "chain_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "золотые векторы цепочки не найдены")

	out := make([]goldenChain, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err)
		zr, err := gzip.NewReader(f)
		require.NoError(t, err)
		var g goldenChain
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)
		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

func (g goldenChain) points() []geo.Point {
	pts := make([]geo.Point, len(g.Points))
	for i, p := range g.Points {
		pts[i] = geo.Point{Time: time.Unix(int64(p[0]), 0).UTC(), Lon: p[1], Lat: p[2]}
	}
	return pts
}

// Сверка сердца алгоритма. Веса берутся готовые, из прототипа, — так
// проверяется именно выбор цепочки, а не всё сразу.
func TestGolden_ChainMatchesPrototype(t *testing.T) {
	for _, g := range loadGoldenChain(t) {
		t.Run(g.UID, func(t *testing.T) {
			got := BuildChain(g.points(), g.Weights, nil)
			require.Len(t, got, len(g.Chain),
				"длина цепочки разошлась: Go %d, прототип %d", len(got), len(g.Chain))

			for i := range got {
				require.Equal(t, g.Chain[i], got[i],
					"цепочка разошлась на позиции %d", i)
			}
			t.Logf("%s: цепочка из %d точек — совпала", g.UID, len(got))
		})
	}
}

// Свойства цепочки на живых данных.
func TestGolden_ChainInvariants(t *testing.T) {
	for _, g := range loadGoldenChain(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := g.points()
			chain := BuildChain(pts, g.Weights, nil)
			require.NotEmpty(t, chain)

			for k := 1; k < len(chain); k++ {
				assert.Greater(t, chain[k], chain[k-1], "цепочка обязана возрастать")
				assert.True(t, Reachable(pts[chain[k-1]], pts[chain[k]], nil),
					"переход %d→%d физически невозможен", chain[k-1], chain[k])
			}

			// Сумма весов цепочки не может быть меньше веса лучшей одиночной
			// точки: иначе выгоднее было бы взять только её.
			var sum, bestSingle float64
			for _, i := range chain {
				sum += g.Weights[i]
			}
			for _, x := range g.Weights {
				bestSingle = max(bestSingle, x)
			}
			assert.GreaterOrEqual(t, sum, bestSingle-1e-9,
				"цепочка легче одной лучшей точки — выбор неоптимален")
		})
	}
}

// goldenRoad — проверка по дорогам вместе с плёнкой ответов OSRM.
//
// Без плёнки сверка бессмысленна: Go спросил бы живой сервер, получил другие
// числа, и любое расхождение списывалось бы на сеть. С плёнкой проверка
// офлайновая и полностью повторяемая — и промах по плёнке сам по себе сигнал,
// что Go спросил то, чего не спрашивал прототип.
type goldenRoad struct {
	UID     string              `json:"uid"`
	Points  [][3]float64        `json:"points"`
	Weights []float64           `json:"weights"`
	Chain   []int               `json:"chain"`
	Bans    []goldenBan         `json:"bans"`
	Penalty map[string]float64  `json:"penalty"`
	Tape    map[string]*float64 `json:"tape"`
	Added   int                 `json:"added"`
}

type goldenBan struct {
	From [2]int  `json:"from"`
	To   [2]int  `json:"to"`
	Need float64 `json:"need"`
}

func loadGoldenRoad(t *testing.T) []goldenRoad {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "road_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "золотые векторы проверки по дорогам не найдены")

	out := make([]goldenRoad, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err)
		zr, err := gzip.NewReader(f)
		require.NoError(t, err)
		var g goldenRoad
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)
		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

func (g goldenRoad) points() []geo.Point {
	pts := make([]geo.Point, len(g.Points))
	for i, p := range g.Points {
		pts[i] = geo.Point{Time: time.Unix(int64(p[0]), 0).UTC(), Lon: p[1], Lat: p[2]}
	}
	return pts
}

// tapeRoads отвечает из записи, а при промахе валит тест: спросить то, чего
// прототип не спрашивал, — само по себе расхождение.
type tapeRoads struct {
	t    *testing.T
	tape map[string]*float64
	miss int
}

func (r *tapeRoads) PairDistance(_ context.Context, pairs []Pair) ([]float64, []bool, []string) {
	dist := make([]float64, len(pairs))
	ok := make([]bool, len(pairs))
	for i, p := range pairs {
		key := fmt.Sprintf("%.5f,%.5f;%.5f,%.5f", p.A.Lon, p.A.Lat, p.B.Lon, p.B.Lat)
		v, found := r.tape[key]
		if !found {
			r.miss++
			continue
		}
		if v != nil {
			dist[i], ok[i] = *v, true
		}
	}
	return dist, ok, nil
}

func TestGolden_RoadCheckMatchesPrototype(t *testing.T) {
	for _, g := range loadGoldenRoad(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := g.points()
			roads := &tapeRoads{t: t, tape: g.Tape}
			st := NewRoadState()

			added := CheckByRoad(context.Background(), roads, pts, g.Chain, st)

			assert.Zero(t, roads.miss,
				"Go спросил %d пар, которых прототип не спрашивал", roads.miss)
			assert.Equal(t, g.Added, added, "число новых запретов разошлось")

			// Запреты: те же переходы и то же требуемое время.
			require.Len(t, st.Banned, len(g.Bans), "число запретов разошлось")
			for _, b := range g.Bans {
				key := BanID{fromY: b.From[0], fromX: b.From[1], toY: b.To[0], toX: b.To[1]}
				need, found := st.Banned[key]
				require.True(t, found, "запрет %v потерян", key)
				assert.InDelta(t, b.Need, need, 1e-6, "требуемое время разошлось")
			}

			// Штрафы: те же точки и те же величины.
			require.Len(t, st.Penalty, len(g.Penalty), "число наказанных точек разошлось")
			for k, want := range g.Penalty {
				i, err := strconv.Atoi(k)
				require.NoError(t, err)
				assert.InDelta(t, want, st.Penalty[i], 1e-9, "штраф точки %d разошёлся", i)
			}

			t.Logf("%s: запретов %d, наказано %d точек, плёнка %d пар — совпало",
				g.UID, len(st.Banned), len(st.Penalty), len(g.Tape))
		})
	}
}
