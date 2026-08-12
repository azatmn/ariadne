package core

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"ariadne/internal/geo"
	"ariadne/internal/logger"
)

// grumpySnap отвечает про точки и жалуется — так ведёт себя настоящий клиент,
// когда часть батчей не удалась.
type grumpySnap struct{ complaint string }

func (g grumpySnap) Snap(_ context.Context, pts []geo.Point) ([]float64, []bool, []string) {
	d, ok := make([]float64, len(pts)), make([]bool, len(pts))
	for i := range pts {
		d[i], ok[i] = 5, true
	}
	return d, ok, []string{g.complaint}
}

// Жалоба маршрутизатора обязана попасть в лог.
//
// Раньше все три места, где ядро зовёт маршрутизатор, глотали её в `_`. При
// отказе разбирающий видел «ответил про слишком мало точек: 45 % из 1200» —
// и ни слова о причине: лежит ли OSRM, отвечает ли мусором, кончился ли бюджет.
// Одна и та же строка на все случаи, разбирать нечем.
func TestSnapWarnings_ReachTheLog(t *testing.T) {
	var buf bytes.Buffer
	ctx := logger.ToContext(context.Background(),
		slog.New(slog.NewJSONHandler(&buf, nil)))

	c := &Core{Snap: grumpySnap{complaint: "snap: не удалось получить снэп для 137 точек из 1200 (connection refused)"}}
	pts := make([]geo.Point, 8)
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for i := range pts {
		pts[i] = geo.Point{Time: t0.Add(time.Duration(i) * time.Minute), Lon: 37.6 + float64(i)*0.01, Lat: 55.75}
	}

	_, _, err := c.Run(ctx, pts)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "connection refused",
		"причина отказа маршрутизатора обязана быть в логе, иначе разбирать нечего")
	assert.Contains(t, out, `"where":"snap"`, "и должно быть видно, откуда жалоба")
}

// Молчит маршрутизатор — молчим и мы: лог не должен шуметь на каждый вызов.
func TestSnapWarnings_QuietWhenNoComplaints(t *testing.T) {
	var buf bytes.Buffer
	ctx := logger.ToContext(context.Background(),
		slog.New(slog.NewJSONHandler(&buf, nil)))

	logRoadWarnings(ctx, "snap", nil)
	logRoadWarnings(ctx, "road", []string{})

	assert.Empty(t, buf.String(), "пустой список жалоб — не повод для записи в лог")
}
