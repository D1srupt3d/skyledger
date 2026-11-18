package geo

import "math"

// Simplify thins pts with Douglas-Peucker: points closer than tol nm to the line between their
// neighbours are dropped. A closed ring (first point repeated last) is measured from that
// point instead of a line.
func Simplify(pts []Point, tol float64) []Point {
	if len(pts) < 3 {
		return pts
	}
	keep := make([]bool, len(pts))
	keep[0], keep[len(pts)-1] = true, true
	stack := [][2]int{{0, len(pts) - 1}}
	for len(stack) > 0 {
		i, j := stack[len(stack)-1][0], stack[len(stack)-1][1]
		stack = stack[:len(stack)-1]
		a, b := pts[i], pts[j]
		dx, dy := b.E-a.E, b.N-a.N
		norm := math.Hypot(dx, dy)
		best, idx := -1.0, -1
		for k := i + 1; k < j; k++ {
			var d float64
			if norm > 1e-9 {
				d = math.Abs(dy*pts[k].E-dx*pts[k].N+b.E*a.N-b.N*a.E) / norm
			} else {
				d = math.Hypot(pts[k].E-a.E, pts[k].N-a.N)
			}
			if d > best {
				best, idx = d, k
			}
		}
		if idx >= 0 && best > tol {
			keep[idx] = true
			stack = append(stack, [2]int{i, idx}, [2]int{idx, j})
		}
	}
	var out []Point
	for k, p := range pts {
		if keep[k] {
			out = append(out, p)
		}
	}
	return out
}
