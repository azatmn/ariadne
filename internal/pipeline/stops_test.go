package pipeline

import (
	"context"
	"testing"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// стоп-детектор для тестов: пятно 50 м, стоянка от 5 точек.
var stops = CollapseStops{RadiusMeters: 50, MinPoints: 5}

func TestCollapseStops_Name(t *testing.T) {
	assert.Equal(t, "collapse_stops", stops.Name())
}

func TestCollapseStops_CleanTrackUnchanged(t *testing.T) {
	// Трасса: шаг ~640 м (> радиуса 50 м) — стоянок нет.
	in := []geo.Point{
		pt(55.0, 37.00), pt(55.0, 37.01), pt(55.0, 37.02),
		pt(55.0, 37.03), pt(55.0, 37.04),
	}
	got, _, err := stops.Apply(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, in, got, "трасса без стоянок не меняется")
}

func TestCollapseStops_CollapsesStop(t *testing.T) {
	// Трасса, потом стоянка (6 точек дрожат в пятне ~20 м вокруг 37.02), потом уезд.
	in := []geo.Point{
		pt(55.00, 37.00), pt(55.00, 37.01), // трасса
		pt(55.0000, 37.0200), pt(55.0001, 37.0201), pt(55.0002, 37.0200),
		pt(55.0001, 37.0199), pt(55.0000, 37.0202), pt(55.0002, 37.0201), // стоянка: 6 точек
		pt(55.00, 37.03), pt(55.00, 37.04), // уезд
	}
	got, _, err := stops.Apply(context.Background(), in)
	require.NoError(t, err)
	// стоянка (6 точек) свёрнута в 1: было 10 -> стало 5.
	assert.Len(t, got, 5)
}

func TestCollapseStops_KeepsPassThrough(t *testing.T) {
	// Медленный проезд через зону: 3 близкие точки (< MinPoints) — не стоянка, не сворачиваем.
	in := []geo.Point{
		pt(55.0, 37.0000), pt(55.0, 37.0003), pt(55.0, 37.0006), // ~19 м шаги, всего 3
		pt(55.0, 37.01), pt(55.0, 37.02),
	}
	got, _, err := stops.Apply(context.Background(), in)
	require.NoError(t, err)
	assert.Len(t, got, len(in), "проезд через зону (< MinPoints) не сворачиваем")
}

func TestCollapseStops_MultipleStops(t *testing.T) {
	in := []geo.Point{
		pt(55.00, 37.00),
		// стоянка 1 (5 точек вокруг 37.01)
		pt(55.0000, 37.0100), pt(55.0001, 37.0101), pt(55.0002, 37.0100),
		pt(55.0001, 37.0099), pt(55.0000, 37.0102),
		pt(55.00, 37.02), // переезд
		// стоянка 2 (5 точек вокруг 37.03)
		pt(55.0000, 37.0300), pt(55.0001, 37.0301), pt(55.0002, 37.0300),
		pt(55.0001, 37.0299), pt(55.0000, 37.0302),
		pt(55.00, 37.04),
	}
	got, _, err := stops.Apply(context.Background(), in)
	require.NoError(t, err)
	// 13 точек: две стоянки по 5 свёрнуты в 1 каждая (−4 и −4) → остаётся 5.
	assert.Len(t, got, 5)
}

func TestCollapseStops_TooFewPoints(t *testing.T) {
	in := []geo.Point{pt(55.0, 37.0), pt(55.0, 37.01)}
	got, _, err := stops.Apply(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, in, got)
}

func TestCollapseStops_Empty(t *testing.T) {
	got, _, err := stops.Apply(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}
