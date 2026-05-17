package geo

func SegmentsIntersect(a1, a2, b1, b2 Point) bool {
	d1 := cross(a1, a2, b1)
	d2 := cross(a1, a2, b2)
	d3 := cross(b1, b2, a1)
	d4 := cross(b1, b2, a2)

	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) &&
		((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}

	if d1 == 0 && onSegment(a1, a2, b1) {
		return true
	}
	if d2 == 0 && onSegment(a1, a2, b2) {
		return true
	}
	if d3 == 0 && onSegment(b1, b2, a1) {
		return true
	}
	if d4 == 0 && onSegment(b1, b2, a2) {
		return true
	}

	return false
}

func cross(o, a, b Point) float64 {
	return (a.Lon-o.Lon)*(b.Lat-o.Lat) - (b.Lon-o.Lon)*(a.Lat-o.Lat)
}

func onSegment(p, q, r Point) bool {
	return min(p.Lon, q.Lon) <= r.Lon && r.Lon <= max(p.Lon, q.Lon) &&
		min(p.Lat, q.Lat) <= r.Lat && r.Lat <= max(p.Lat, q.Lat)
}
