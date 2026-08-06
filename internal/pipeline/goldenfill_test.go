package pipeline

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ariadne/internal/geo"
	"ariadne/internal/osrm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Сверка дорисовки с прототипом на настоящих треках.
//
// Тесты выше проверяют, что стадия делает то, что я задумал. Здесь — что она
// делает то же самое, что делает отлаженный Python: те же дыры признаны
// дырами, те же приняты, та же геометрия легла в трек и те же точки помечены
// выдуманными.

type goldenFill struct {
	UID    string       `json:"uid"`
	Points [][3]float64 `json:"points"` // вход стадии: очищенные точки
	Stops  []int        `json:"stops"`  // какие из них — стоянки
	Tape   map[string]*struct {
		Distance float64      `json:"distance"`
		Coords   [][2]float64 `json:"coords"`
		SnapA    *[2]float64  `json:"snap_a"`
		SnapB    *[2]float64  `json:"snap_b"`
	} `json:"tape"`
	Out    [][3]float64 `json:"out"`
	Flags  []bool       `json:"flags"`
	Report struct {
		Gaps     int            `json:"gaps"`
		Filled   int            `json:"filled"`
		AddedPts int            `json:"added_pts"`
		AddedM   float64        `json:"added_m"`
		Reasons  map[string]int `json:"reasons"`
	} `json:"report"`
}

func loadGoldenFill(t *testing.T) []goldenFill {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "fill_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "золотые векторы дорисовки не найдены")

	out := make([]goldenFill, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err)
		zr, err := gzip.NewReader(f)
		require.NoError(t, err)
		var g goldenFill
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)
		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

// secToTime — питоновская метка времени в дробных секундах.
func secToTime(sec float64) time.Time {
	whole := math.Floor(sec)
	return time.Unix(int64(whole), int64((sec-whole)*1e9)).UTC()
}

func fillPoints(rows [][3]float64) []geo.Point {
	pts := make([]geo.Point, len(rows))
	for i, p := range rows {
		pts[i] = geo.Point{Time: secToTime(p[0]), Lon: p[1], Lat: p[2]}
	}
	return pts
}

// tapeRoutes отвечает из записи, а при промахе валит тест: спросить то, чего
// прототип не спрашивал, — само по себе расхождение.
// Счётчики под замком: дорисовка спрашивает дыры восемью потоками, и
// подставка обязана это выдерживать. Без замка тест недосчитывал вопросы —
// поймано гонкой.
type tapeRoutes struct {
	t     *testing.T
	g     goldenFill
	mu    sync.Mutex
	asked int
	miss  int
}

func (r *tapeRoutes) RouteGeometry(_ context.Context, a, b geo.Point) (*osrm.Route, bool) {
	r.mu.Lock()
	r.asked++
	r.mu.Unlock()
	key := fmt.Sprintf("%.5f,%.5f;%.5f,%.5f", a.Lon, a.Lat, b.Lon, b.Lat)
	rec, found := r.g.Tape[key]
	if !found {
		r.mu.Lock()
		r.miss++
		r.mu.Unlock()
		return nil, false
	}
	if rec == nil {
		return nil, false
	}

	out := &osrm.Route{Distance: rec.Distance, Coords: rec.Coords}
	if rec.SnapA != nil {
		out.SnapA, out.HasSnapA = *rec.SnapA, true
	}
	if rec.SnapB != nil {
		out.SnapB, out.HasSnapB = *rec.SnapB, true
	}
	return out, true
}

func TestGoldenFill_MatchesPrototype(t *testing.T) {
	for _, g := range loadGoldenFill(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := fillPoints(g.Points)

			must := make(map[PointKey]struct{}, len(g.Stops))
			for _, k := range g.Stops {
				must[KeyOf(pts[k])] = struct{}{}
			}
			st := &RunState{Must: must}
			routes := &tapeRoutes{t: t, g: g}

			got, _, err := FillGaps{Routes: routes, State: st}.Apply(context.Background(), pts)
			require.NoError(t, err)

			// Сперва — про что спрашивали: если дыры определены иначе, по
			// итоговому треку этого не понять.
			assert.Zero(t, routes.miss,
				"Go спросил %d дыр, которых прототип не спрашивал", routes.miss)
			assert.Equal(t, len(g.Tape), routes.asked, "число заданных вопросов")

			assert.Equal(t, g.Report.Gaps, st.Fill.Gaps, "найдено дыр")
			assert.Equal(t, g.Report.Filled, st.Fill.Filled, "дорисовано дыр")
			assert.Equal(t, g.Report.Reasons, st.Fill.Reasons, "причины отказов")
			assert.InDelta(t, g.Report.AddedM, st.Fill.AddedM, 1e-6, "прибавка в метрах")
			assert.Equal(t, g.Report.AddedPts, st.Fill.AddedPts, "добавлено точек")

			// И сам трек — точка в точку.
			require.Len(t, got, len(g.Out), "длина итогового трека")
			for i, want := range g.Out {
				assert.InDelta(t, want[0], float64(got[i].Time.UnixNano())/1e9, 1e-6,
					"точка %d: время", i)
				assert.InDelta(t, want[1], got[i].Lon, 1e-12, "точка %d: долгота", i)
				assert.InDelta(t, want[2], got[i].Lat, 1e-12, "точка %d: широта", i)
			}

			require.Len(t, st.Synthetic, len(g.Flags), "длина пометок")
			for i, want := range g.Flags {
				assert.Equal(t, want, st.Synthetic[i], "точка %d: пометка «выдумана»", i)
			}

			t.Logf("%s: %d → %d точек, дыр %d, дорисовано %d — совпало",
				g.UID, len(pts), len(got), st.Fill.Gaps, st.Fill.Filled)
		})
	}
}
