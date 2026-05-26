package geo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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

	assert.True(t, SegmentsIntersect(a1, a2, b1, b2), "expected intersection for crossing segments")
}

func TestIntersectParallel(t *testing.T) {
	// a1---a2
	// b1---b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 2, Lat: 0}
	b1 := Point{Lon: 0, Lat: 1}
	b2 := Point{Lon: 2, Lat: 1}

	assert.False(t, SegmentsIntersect(a1, a2, b1, b2), "parallel segments should not intersect")
}

func TestIntersectNoOverlap(t *testing.T) {
	// a1--a2   b1--b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 1, Lat: 0}
	b1 := Point{Lon: 3, Lat: 0}
	b2 := Point{Lon: 4, Lat: 0}

	assert.False(t, SegmentsIntersect(a1, a2, b1, b2), "separated segments should not intersect")
}

func TestIntersectTouchEndpoint(t *testing.T) {
	// a1---a2/b1---b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 1, Lat: 0}
	b1 := Point{Lon: 1, Lat: 0}
	b2 := Point{Lon: 2, Lat: 0}

	assert.True(t, SegmentsIntersect(a1, a2, b1, b2), "segments sharing endpoint should intersect")
}

func TestIntersectLShape(t *testing.T) {
	// a1---a2
	//      |
	//      b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 1, Lat: 0}
	b1 := Point{Lon: 1, Lat: 0}
	b2 := Point{Lon: 1, Lat: -1}

	assert.True(t, SegmentsIntersect(a1, a2, b1, b2), "L-shaped segments touching at corner should intersect")
}

func TestIntersectCollinearOverlap(t *testing.T) {
	// a1------a2
	//     b1------b2
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 4, Lat: 0}
	b1 := Point{Lon: 2, Lat: 0}
	b2 := Point{Lon: 6, Lat: 0}

	assert.True(t, SegmentsIntersect(a1, a2, b1, b2), "collinear overlapping segments should intersect")
}

func TestIntersectDiagonal(t *testing.T) {
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 4, Lat: 4}
	b1 := Point{Lon: 0, Lat: 4}
	b2 := Point{Lon: 4, Lat: 0}

	assert.True(t, SegmentsIntersect(a1, a2, b1, b2), "diagonal crossing segments should intersect")
}

func TestIntersectCollinearNotOnSegment(t *testing.T) {
	tests := []struct {
		name           string
		a1, a2, b1, b2 Point
	}{
		{
			name: "b2 collinear with a1-a2 but beyond a2",
			a1:   Point{Lon: 0, Lat: 0}, a2: Point{Lon: 2, Lat: 0},
			b1: Point{Lon: 3, Lat: 1}, b2: Point{Lon: 3, Lat: 0},
		},
		{
			name: "a1 collinear with b1-b2 but beyond b2",
			a1:   Point{Lon: 3, Lat: 0}, a2: Point{Lon: 3, Lat: 1},
			b1: Point{Lon: 0, Lat: 0}, b2: Point{Lon: 2, Lat: 0},
		},
		{
			name: "a2 collinear with b1-b2 but before b1",
			a1:   Point{Lon: -2, Lat: 1}, a2: Point{Lon: -1, Lat: 0},
			b1: Point{Lon: 0, Lat: 0}, b2: Point{Lon: 2, Lat: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, SegmentsIntersect(tt.a1, tt.a2, tt.b1, tt.b2), "collinear point outside segment should not cause intersection")
		})
	}
}

func TestIntersectAlmostTouch(t *testing.T) {
	a1 := Point{Lon: 0, Lat: 0}
	a2 := Point{Lon: 1, Lat: 0}
	b1 := Point{Lon: 0.5, Lat: 0.001}
	b2 := Point{Lon: 0.5, Lat: 1}

	assert.False(t, SegmentsIntersect(a1, a2, b1, b2), "almost touching segments should not intersect")
}
