package core

import (
	"compress/gzip"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
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
