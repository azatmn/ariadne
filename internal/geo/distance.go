package geo

import "math"

// Средний радиус Земли по IUGG (R1), метры. Ровно то же число, что в
// прототипе на Python: сверка Go с эталоном идёт число в число, а не с
// допуском — допуск прячет настоящие ошибки переноса.
//
// Прежнее значение 6 371 000 — это оно же, округлённое до пяти значащих
// цифр; разница 1.4 ppm (миллионных), то есть 1.4 м на тысячу километров.
// Спорить о том, какое «точнее», не о чем: сам гаверсинус считает Землю
// шаром, а она сплюснута, и на этом врёт до 0.5 % — в три с половиной тысячи
// раз больше.
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
