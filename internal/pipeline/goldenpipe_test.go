package pipeline

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
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

// Сквозная сверка ВСЕГО конвейера с прототипом.
//
// Части сверены по отдельности: ядро целиком и дорисовка. Здесь проверяется
// то, чего не проверяет ни одна из них, — СТЫК между стадиями. Упаковка
// получает выход ядра, страж — снимок до упаковки, дорисовка — список стоянок;
// разойдись что-нибудь на границе, по частям этого не увидеть.

type goldenPipe struct {
	UID    string              `json:"uid"`
	Points [][3]float64        `json:"points"`
	Snaps  map[string]*float64 `json:"snaps"`
	Pairs  map[string]*float64 `json:"pairs"`
	Routes map[string]*struct {
		Distance float64      `json:"distance"`
		Coords   [][2]float64 `json:"coords"`
		SnapA    *[2]float64  `json:"snap_a"`
		SnapB    *[2]float64  `json:"snap_b"`
	} `json:"routes"`
	Out     [][3]float64 `json:"out"`
	Flags   []bool       `json:"flags"`
	KmAfter float64      `json:"kmAfter"`
}

func loadGoldenPipe(t *testing.T) []goldenPipe {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "pipe_*.json.gz"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "сквозные золотые векторы конвейера не найдены")

	out := make([]goldenPipe, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		require.NoError(t, err)
		zr, err := gzip.NewReader(f)
		require.NoError(t, err)
		var g goldenPipe
		require.NoError(t, json.NewDecoder(zr).Decode(&g), "разбор %s", p)
		require.NoError(t, zr.Close())
		require.NoError(t, f.Close())
		out = append(out, g)
	}
	return out
}

// tapeRouter отвечает из записи на все три вопроса конвейера. Промах — это
// расхождение само по себе: спросили то, чего прототип не спрашивал.
type tapeRouter struct {
	g    goldenPipe
	mu   sync.Mutex
	miss int
}

func (r *tapeRouter) fail() {
	r.mu.Lock()
	r.miss++
	r.mu.Unlock()
}

func (r *tapeRouter) Snap(_ context.Context, pts []geo.Point) ([]float64, []bool, []string) {
	snaps, ok := make([]float64, len(pts)), make([]bool, len(pts))
	for i, p := range pts {
		v, found := r.g.Snaps[fmt.Sprintf("%.5f,%.5f", p.Lon, p.Lat)]
		if !found {
			r.fail()
			continue
		}
		if v != nil {
			snaps[i], ok[i] = *v, true
		}
	}
	return snaps, ok, nil
}

func (r *tapeRouter) PairDistance(_ context.Context, pairs []osrm.Pair) ([]float64, []bool, []string) {
	dist, ok := make([]float64, len(pairs)), make([]bool, len(pairs))
	for i, p := range pairs {
		v, found := r.g.Pairs[pipeKey(p.A, p.B)]
		if !found {
			r.fail()
			continue
		}
		if v != nil {
			dist[i], ok[i] = *v, true
		}
	}
	return dist, ok, nil
}

func (r *tapeRouter) RouteGeometry(_ context.Context, a, b geo.Point) (*osrm.Route, bool) {
	rec, found := r.g.Routes[pipeKey(a, b)]
	if !found {
		r.fail()
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

func pipeKey(a, b geo.Point) string {
	return fmt.Sprintf("%.5f,%.5f;%.5f,%.5f", a.Lon, a.Lat, b.Lon, b.Lat)
}

func TestGoldenPipe_MatchesPrototype(t *testing.T) {
	for _, g := range loadGoldenPipe(t) {
		t.Run(g.UID, func(t *testing.T) {
			pts := make([]geo.Point, len(g.Points))
			for i, p := range g.Points {
				pts[i] = geo.Point{Time: time.Unix(int64(p[0]), 0).UTC(), Lon: p[1], Lat: p[2]}
			}

			router := &tapeRouter{g: g}
			// Параметры — те же дефолты, на которых считает прототип
			// (`gostages.py`, раздел «дефолты из config.go»).
			pl := New(Params{
				DedupDistanceMeters: 2.0,
				DedupTimeGap:        60 * time.Second,
				StopRadiusMeters:    50.0,
				StopMinPoints:       5,
				SimplifyMinMeters:   5.0,
			}, router)

			got, _, _, err := pl.Run(context.Background(), pts)
			require.NoError(t, err)

			assert.Zero(t, router.miss,
				"Go спросил %d раз то, чего прототип не спрашивал", router.miss)

			require.Len(t, got, len(g.Out), "длина итогового трека")
			for i, want := range g.Out {
				assert.InDelta(t, want[0], float64(got[i].Time.UnixNano())/1e9, 1e-6,
					"точка %d: время", i)
				assert.InDelta(t, want[1], got[i].Lon, 1e-12, "точка %d: долгота", i)
				assert.InDelta(t, want[2], got[i].Lat, 1e-12, "точка %d: широта", i)
			}

			syn := pl.State().Synthetic
			require.Len(t, syn, len(g.Flags), "длина пометок «выдумана»")
			for i, want := range g.Flags {
				assert.Equal(t, want, syn[i], "точка %d: пометка", i)
			}

			assert.InDelta(t, g.KmAfter, geo.TotalLength(got)/1000, 1e-6, "километраж")

			t.Logf("%s: %d → %d точек, %.0f км — совпало",
				g.UID, len(pts), len(got), geo.TotalLength(got)/1000)
		})
	}
}
