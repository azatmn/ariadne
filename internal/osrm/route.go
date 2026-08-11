package osrm

import (
	"context"
	"encoding/json"
	"fmt"

	"ariadne/internal/geo"
)

// Route — путь по дорогам между двумя точками вместе с его геометрией.
type Route struct {
	// Distance — длина пути в метрах.
	Distance float64

	// Duration — сколько по мнению OSRM ехать, в секундах. Мы этим временем
	// не пользуемся при решении (у грузовика свои скорости), но храним:
	// оно помогает разбирать спорные случаи вручную.
	Duration float64

	// Coords — вершины пути, долгота и широта. Первая и последняя совпадают
	// с посаженными на дорогу концами.
	Coords [][2]float64

	// SnapA, SnapB — концы, посаженные на дорогу. Приходят тем же ответом,
	// отдельный запрос к /nearest не нужен — и это важно, потому что на
	// боевом сервере /nearest закрыт.
	SnapA, SnapB [2]float64
	HasSnapA     bool
	HasSnapB     bool
}

// RouteGeometry строит путь по дорогам между двумя точками.
//
// Второе значение — признак «путь есть». false означает, что проезда нет или
// сервер промолчал; дорисовка в этом случае оставляет прямую, то есть ведёт
// себя ровно так, как если бы её не было вовсе.
func (c *Client) RouteGeometry(ctx context.Context, a, b geo.Point) (*Route, bool) {
	buf := make([]byte, 0, 128)
	buf = append(buf, "/route/v1/driving/"...)
	buf = appendCoord(buf, a.Lon, a.Lat)
	buf = append(buf, ';')
	buf = appendCoord(buf, b.Lon, b.Lat)
	buf = append(buf, "?overview=full&geometries=geojson&steps=false"...)

	body, err := c.get(ctx, string(buf))
	if err != nil {
		return nil, false
	}

	var r routeResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, false
	}
	if r.Code != "Ok" || len(r.Routes) == 0 {
		return nil, false
	}

	best := r.Routes[0]
	out := &Route{
		Distance: best.Distance,
		Duration: best.Duration,
		Coords:   best.Geometry.Coordinates,
	}
	if len(r.Waypoints) > 0 {
		out.SnapA, out.HasSnapA = r.Waypoints[0].Location, true
	}
	if len(r.Waypoints) > 1 {
		out.SnapB, out.HasSnapB = r.Waypoints[1].Location, true
	}
	return out, true
}

// Ping проверяет, что сервис отвечает и умеет строить маршруты.
//
// Вызывается на старте: сервис, который не может достучаться до OSRM, обязан
// сказать об этом сразу в логе, а не выяснять это на первой же задаче, когда
// разбираться будет некому.
func (c *Client) Ping(ctx context.Context) error {
	// Две точки в центре Москвы, между которыми заведомо есть проезд.
	a := geo.Point{Lon: 37.6173, Lat: 55.7558}
	b := geo.Point{Lon: 37.6273, Lat: 55.7658}
	if _, ok := c.RouteGeometry(ctx, a, b); !ok {
		return fmt.Errorf("osrm: %s не отвечает или не строит маршруты", c.baseURL)
	}
	return nil
}
