package geo

import "time"

// Point — одна GPS-точка маршрута в WGS84.
//
// JSON-формат на проводе (как в backend):
//
//	{ "t": "2026-03-16T10:12:20+03:00", "pos": { "x": <lon>, "y": <lat> } }
//
// Внутри сервиса используем плоскую структуру — конвертация в codec.
type Point struct {
	Time time.Time
	Lon  float64 // долгота (pos.x)
	Lat  float64 // широта  (pos.y)
}
