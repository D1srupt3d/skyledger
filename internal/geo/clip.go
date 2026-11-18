package geo

import "math"

// ClipPolygon clips a ring to the square [-box, box]^2 (Sutherland-Hodgman).
func ClipPolygon(ring []Point, box float64) []Point {
	edges := []struct {
		axis int
		val  float64
		sign float64
	}{{0, -box, 1}, {0, box, -1}, {1, -box, 1}, {1, box, -1}}
	out := ring
	for _, e := range edges {
		if len(out) == 0 {
			break
		}
		src := out
		out = nil
		inside := func(p Point) bool { return (coord(p, e.axis)-e.val)*e.sign >= 0 }
		for i, cur := range src {
			prev := src[(i+len(src)-1)%len(src)]
			switch {
			case inside(cur):
				if !inside(prev) {
					out = append(out, cross(prev, cur, e.axis, e.val))
				}
				out = append(out, cur)
			case inside(prev):
				out = append(out, cross(prev, cur, e.axis, e.val))
			}
		}
	}
	return out
}

func coord(p Point, axis int) float64 {
	if axis == 0 {
		return p.E
	}
	return p.N
}

// cross is where the segment a-b meets the line axis == val.
func cross(a, b Point, axis int, val float64) Point {
	t := (val - coord(a, axis)) / (coord(b, axis) - coord(a, axis))
	return Point{a.E + (b.E-a.E)*t, a.N + (b.N-a.N)*t}
}

// ClipLine splits a polyline into the runs of 2 or more points that stay within box + 20 nm
// (the margin keeps edges whole).
func ClipLine(line []Point, box float64) [][]Point {
	var runs [][]Point
	var cur []Point
	flush := func() {
		if len(cur) > 1 {
			runs = append(runs, cur)
		}
		cur = nil
	}
	for _, p := range line {
		if math.Abs(p.E) <= box+20 && math.Abs(p.N) <= box+20 {
			cur = append(cur, p)
		} else {
			flush()
		}
	}
	flush()
	return runs
}
