package geo

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestBuildAroundTheDemoReceiver(t *testing.T) {
	m, err := Build(50.90, -0.11)
	if err != nil {
		t.Fatal(err)
	}
	if m.Center == nil || *m.Center != [2]float64{50.90, -0.11} {
		t.Errorf("center = %v", m.Center)
	}
	if len(m.Land) < 10 || len(m.Borders) < 1 {
		t.Errorf("land %d, borders %d; want 10+ and 1+", len(m.Land), len(m.Borders))
	}
	for _, set := range [][][][2]float64{m.Land, m.Borders} {
		for _, path := range set {
			for _, p := range path {
				if math.Abs(p[0]) > Box+20 || math.Abs(p[1]) > Box+20 {
					t.Fatalf("point %v outside the box", p)
				}
			}
		}
	}
	for _, ring := range m.Land {
		if ring[0] != ring[len(ring)-1] {
			t.Fatalf("ring not closed: %v ... %v", ring[0], ring[len(ring)-1])
		}
	}
	if len(m.Cities) > MaxCities {
		t.Errorf("%d cities, want at most %d", len(m.Cities), MaxCities)
	}
	if !slices.IsSortedFunc(m.Cities, func(a, b City) int { return b.Pop - a.Pop }) {
		t.Errorf("cities not sorted by population: %v", m.Cities)
	}
	i := slices.IndexFunc(m.Cities, func(c City) bool { return c.Name == "London" })
	if i < 0 {
		t.Fatalf("no London in %v", m.Cities)
	}
	if d := math.Hypot(m.Cities[i].E, m.Cities[i].N); d > 60 {
		t.Errorf("London is %.0f nm away, want within 60", d)
	}
}

func TestBuildMidOcean(t *testing.T) {
	m, err := Build(40.0, -45.0)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Land) != 0 || len(m.Cities) != 0 {
		t.Errorf("land %d, cities %d; want none", len(m.Land), len(m.Cities))
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"land":[]`, `"borders":[]`, `"cities":[]`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s missing from %s", want, b)
		}
	}
}

func TestBuildMatchesTheDemoMap(t *testing.T) {
	raw, err := os.ReadFile("../../site/demo/map.json")
	if err != nil {
		t.Fatal(err)
	}
	var demo Map
	if err := json.Unmarshal(raw, &demo); err != nil {
		t.Fatal(err)
	}
	m, err := Build(50.90, -0.11)
	if err != nil {
		t.Fatal(err)
	}
	names := func(cs []City) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Name)
		}
		return out
	}
	if got, want := names(m.Cities), names(demo.Cities); !slices.Equal(got, want) {
		t.Errorf("cities = %v, want %v", got, want)
	}
	count := func(rings [][][2]float64) (n int) {
		for _, r := range rings {
			n += len(r)
		}
		return n
	}
	got, want := count(m.Land), count(demo.Land)
	if math.Abs(float64(got-want)) > 0.1*float64(want) {
		t.Errorf("land points = %d, demo has %d (more than 10%% apart)", got, want)
	}
	t.Logf("land points %d vs demo %d", got, want)
}
