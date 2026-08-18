package geo

import "math"

// earthRadius — средний радиус Земли по IUGG (R1), метры.
const earthRadius = 6_371_008.8

// Haversine — расстояние между точками по дуге большого круга, метры.
//
// Формула гаверсинуса, а не теорема косинусов: последняя на близких точках
// теряет знаки в float64, а точки трека приходят раз в несколько секунд и
// отстоят на десятки метров.
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
//
// Нужно упрощению трека: точка, отстоящая от прямой между соседями меньше
// чем на порог, ничего к форме маршрута не добавляет и выбрасывается.
//
// Дуга считается бесконечной: если проекция p падает за пределы отрезка ab,
// вернётся расстояние до продолжения дуги, а не до ближайшего конца.
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

// bearing — начальный азимут из a в b, радианы от −π до π против севера.
// Внутренний: наружу отдаётся BearingDegrees, у него удобный диапазон.
func bearing(a, b Point) float64 {
	lat1 := a.Lat * math.Pi / 180
	lat2 := b.Lat * math.Pi / 180
	dlon := (b.Lon - a.Lon) * math.Pi / 180

	y := math.Sin(dlon) * math.Cos(lat2)
	x := math.Cos(lat1)*math.Sin(lat2) - math.Sin(lat1)*math.Cos(lat2)*math.Cos(dlon)
	return math.Atan2(y, x)
}

// TotalLength — длина ломаной по точкам, метры. Это и есть километраж,
// который сервис возвращает заказчику: сумма расстояний между соседями.
// Порядок точек берётся как есть — сортировать по времени должен вызывающий.
func TotalLength(points []Point) float64 {
	var total float64
	for i := 1; i < len(points); i++ {
		total += Haversine(points[i-1], points[i])
	}
	return total
}
