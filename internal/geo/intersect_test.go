package geo

import "testing"

func TestIntersectCross(t *testing.T) {
	//   b1
	//   |
	// a1---a2
	//   |
	//   b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 2, Lat: 0}
	b1 := Point{Lon: 1, Lat: 1}
	b2 := Point{Lon: 1, Lat: -1}

	if !SegmentsIntersect(a1, a2, b1, b2) {
		t.Error("expected intersection for crossing segments")
	}
}

func TestIntersectParallel(t *testing.T) {
	// a1---a2
	// b1---b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 2, Lat: 0}
	b1 := Point{Lon: 0, Lat: 1}
	b2 := Point{Lon: 2, Lat: 1}

	if SegmentsIntersect(a1, a2, b1, b2) {
		t.Error("parallel segments should not intersect")
	}
}

func TestIntersectNoOverlap(t *testing.T) {
	// a1--a2   b1--b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 1, Lat: 0}
	b1 := Point{Lon: 3, Lat: 0}
	b2 := Point{Lon: 4, Lat: 0}

	if SegmentsIntersect(a1, a2, b1, b2) {
		t.Error("separated segments should not intersect")
	}
}

func TestIntersectTouchEndpoint(t *testing.T) {
	// a1---a2/b1---b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 1, Lat: 0}
	b1 := Point{Lon: 1, Lat: 0}
	b2 := Point{Lon: 2, Lat: 0}

	if !SegmentsIntersect(a1, a2, b1, b2) {
		t.Error("segments sharing endpoint should intersect")
	}
}

func TestIntersectLShape(t *testing.T) {
	// a1---a2
	//      |
	//      b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 1, Lat: 0}
	b1 := Point{Lon: 1, Lat: 0}
	b2 := Point{Lon: 1, Lat: -1}

	if !SegmentsIntersect(a1, a2, b1, b2) {
		t.Error("L-shaped segments touching at corner should intersect")
	}
}

func TestIntersectCollinearOverlap(t *testing.T) {
	// a1------a2
	//     b1------b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 4, Lat: 0}
	b1 := Point{Lon: 2, Lat: 0}
	b2 := Point{Lon: 6, Lat: 0}

	if !SegmentsIntersect(a1, a2, b1, b2) {
		t.Error("collinear overlapping segments should intersect")
	}
}

func TestIntersectDiagonal(t *testing.T) {
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 4, Lat: 4}
	b1 := Point{Lon: 0, Lat: 4}
	b2 := Point{Lon: 4, Lat: 0}

	if !SegmentsIntersect(a1, a2, b1, b2) {
		t.Error("diagonal crossing segments should intersect")
	}
}

func TestIntersectAlmostTouch(t *testing.T) {
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 1, Lat: 0}
	b1 := Point{Lon: 0.5, Lat: 0.001}
	b2 := Point{Lon: 0.5, Lat: 1}

	if SegmentsIntersect(a1, a2, b1, b2) {
		t.Error("almost touching segments should not intersect")
	}
}
