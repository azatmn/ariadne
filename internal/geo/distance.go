package geo

import "math"

// Средний радиус Земли по IUGG (R1), метры
const earthRadius = 6_371_008.8

func Haversine(a, b Point) float64 {
	lat1 := a.Lat * math.Pi / 180
	lat2 := b.Lat * math.Pi / 180
	dlat := (b.Lat - a.Lat) * math.Pi / 180
	dlon := (b.Lon - a.Lon) * math.Pi / 180

	h := math.Sin(dlat/2)*math.Sin(dlat/2) +
		math.Cos(lat1)*math.Cos(lat2)*
			math.Sin(dlon/2)*math.Sin(dlon/2)

	return 2 * earthRadius * math.Atan2(math.Sqrt(h), math.Sqrt(1-h))
}

// CrossTrackDistance — перпендикулярное расстояние (в метрах) от точки p
// до дуги большого круга между a и b.
func CrossTrackDistance(p, a, b Point) float64 {
	distAP := Haversine(a, p) / earthRadius
	bearAB := bearing(a, b)
	bearAP := bearing(a, p)

	return math.Abs(math.Asin(math.Sin(distAP)*math.Sin(bearAP-bearAB))) * earthRadius
}

// BearingDegrees — направление из a в b: градусы по часовой стрелке от севера,
// всегда в диапазоне 0…360.
//
// Нужно, чтобы подсказать маршрутизатору сторону разделённой трассы. Точка от
// трекера ложится между двумя проезжими частями (на ЦКАД это 4–8 метров до
// каждой), маршрутизатор выбирает сторону сам и, промахнувшись, ведёт до
// ближайшего разворота: 25.7 км вместо 2.2.
//
// Отдельная функция, а не bearing: тот отдаёт радианы и может быть
// отрицательным, а наружу нужны именно градусы 0…360 — в другом виде
// маршрутизатор подсказку не принимает.
func BearingDegrees(a, b Point) float64 {
	deg := bearing(a, b) * 180 / math.Pi
	return math.Mod(deg+360, 360)
}

func bearing(a, b Point) float64 {
	lat1 := a.Lat * math.Pi / 180
	lat2 := b.Lat * math.Pi / 180
	dlon := (b.Lon - a.Lon) * math.Pi / 180

	y := math.Sin(dlon) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(dlon)
	return math.Atan2(y, x)
}

func TotalLength(points []Point) float64 {
	var total float64
	for i := 1; i < len(points); i++ {
		total += Haversine(points[i-1], points[i])
	}
	return total
}
