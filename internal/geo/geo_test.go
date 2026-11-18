package geo

import (
	"math"
	"slices"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v +/- %v", name, got, want, tol)
	}
}

func TestProjectorCenterAndNorth(t *testing.T) {
	proj := Projector(50.90, -0.11)
	p, d := proj(-0.11, 50.90)
	if p != (Point{0, 0}) || d != 0 {
		t.Errorf("center = %v, %v; want origin, 0", p, d)
	}
	p, d = proj(-0.11, 51.90)
	near(t, "E", p.E, 0, 0.01)
	near(t, "N", p.N, 60.0, 0.1)
	near(t, "d", d, 60.0, 0.1)
}

func TestProjectorEast(t *testing.T) {
	p, _ := Projector(0, 0)(1, 0)
	near(t, "E", p.E, 60.04, 0.1)
	near(t, "N", p.N, 0, 0.01)
}

func TestClipPolygonInsideUnchanged(t *testing.T) {
	ring := []Point{{-5, -5}, {5, -5}, {5, 5}, {-5, 5}}
	if got := ClipPolygon(ring, Box); !slices.Equal(got, ring) {
		t.Errorf("got %v, want %v", got, ring)
	}
}

func TestClipPolygonCutsAtBox(t *testing.T) {
	got := ClipPolygon([]Point{{-500, 0}, {500, 0}, {500, 10}, {-500, 10}}, 340)
	for _, p := range got {
		if math.Abs(p.E) > 340 {
			t.Errorf("point %v outside box", p)
		}
	}
	for _, want := range []Point{{-340, 0}, {340, 10}} {
		if !slices.Contains(got, want) {
			t.Errorf("%v missing from %v", want, got)
		}
	}
}

func TestClipPolygonOutsideEmpty(t *testing.T) {
	if got := ClipPolygon([]Point{{400, 0}, {500, 0}, {500, 10}, {400, 10}}, Box); len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestClipLineSplitsRuns(t *testing.T) {
	got := ClipLine([]Point{{0, 0}, {10, 0}, {1000, 0}, {20, 0}, {30, 0}}, 340)
	if len(got) != 2 || len(got[0]) != 2 || len(got[1]) != 2 {
		t.Errorf("got %v, want two runs of 2 points", got)
	}
}

func TestClipLineDropsSinglePointRuns(t *testing.T) {
	if got := ClipLine([]Point{{0, 0}, {1000, 0}, {20, 0}}, 340); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

func TestSimplifyDropsNearlyStraightPoints(t *testing.T) {
	pts := []Point{{0, 0}, {5, 0.1}, {10, 0}}
	if got, want := Simplify(pts, 0.3), []Point{{0, 0}, {10, 0}}; !slices.Equal(got, want) {
		t.Errorf("tol 0.3: got %v, want %v", got, want)
	}
	if got := Simplify(pts, 0.05); !slices.Equal(got, pts) {
		t.Errorf("tol 0.05: got %v, want %v", got, pts)
	}
}

func TestSimplifyClosedRing(t *testing.T) {
	ring := []Point{{0, 0}, {5, 0.1}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}
	want := []Point{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}}
	if got := Simplify(ring, 0.3); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSimplifyShortUnchanged(t *testing.T) {
	pts := []Point{{0, 0}, {1, 1}}
	if got := Simplify(pts, 0.3); !slices.Equal(got, pts) {
		t.Errorf("got %v", got)
	}
}
