package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenworldWritesCompactWorld(t *testing.T) {
	out := filepath.Join(t.TempDir(), "world.json.gz")
	if err := run("testdata", out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var w world
	if err := json.NewDecoder(zr).Decode(&w); err != nil {
		t.Fatal(err)
	}
	if len(w.Land) != 2 || len(w.Borders) != 1 || len(w.Cities) != 1 {
		t.Fatalf("land %d borders %d cities %d, want 2, 1, 1", len(w.Land), len(w.Borders), len(w.Cities))
	}
	if got := w.Land[0][1]; got != [2]float64{10.1235, 0} {
		t.Errorf("land point = %v, want [10.1235 0]", got)
	}
	if got := w.Borders[0][0]; got != [2]float64{1, 1.0001} {
		t.Errorf("border point = %v, want [1 1.0001]", got)
	}
	c := w.Cities[0]
	if c.Name != "Big" || c.Lon != 5.1235 || c.Lat != 6.6543 || c.Pop != 300000 || !c.Capital {
		t.Errorf("city = %+v", c)
	}
}

func TestGenworldMissingFile(t *testing.T) {
	err := run(t.TempDir(), filepath.Join(t.TempDir(), "w.gz"))
	if err == nil || !strings.Contains(err.Error(), "ne_10m_land.geojson") {
		t.Errorf("err = %v, want one naming ne_10m_land.geojson", err)
	}
}

func TestGenworldBadGeometry(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"ne_10m_land.geojson", "ne_10m_admin_0_boundary_lines_land.geojson", "ne_10m_populated_places_simple.geojson"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(`{"features":[{"geometry":{"type":"Point","coordinates":[1]}}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(dir, filepath.Join(t.TempDir(), "w.gz")); err == nil {
		t.Error("want an error for a Point in the land file")
	}
}

func TestPathsMultiAndBadShapes(t *testing.T) {
	got, err := paths("MultiLineString", []byte(`[[[0,0],[1,1]],[[2,2],[3,3]]]`), "LineString", "MultiLineString")
	if err != nil || len(got) != 2 || got[1][0] != [2]float64{2, 2} {
		t.Errorf("MultiLineString: got %v, %v", got, err)
	}
	got, err = paths("MultiPolygon", []byte(`[[[[0,0],[1,0],[0,0]]],[[[5,5],[6,5],[5,5]]]]`), "Polygon", "MultiPolygon")
	if err != nil || len(got) != 2 {
		t.Errorf("MultiPolygon: got %v, %v", got, err)
	}
	for _, c := range []struct{ typ, raw string }{
		{"LineString", `"x"`}, {"MultiLineString", `"x"`}, {"LineString", `[[1]]`}, {"Point", `[1,2]`},
	} {
		if _, err := paths(c.typ, []byte(c.raw), "LineString", "MultiLineString"); err == nil {
			t.Errorf("%s %s: want an error", c.typ, c.raw)
		}
	}
}

func TestGenworldBadFiles(t *testing.T) {
	names := []string{"ne_10m_land.geojson", "ne_10m_admin_0_boundary_lines_land.geojson", "ne_10m_populated_places_simple.geojson"}
	good := map[string]string{
		names[0]: `{"features":[]}`, names[1]: `{"features":[]}`, names[2]: `{"features":[]}`,
	}
	bad := map[string]string{
		names[0]: `not json`,
		names[1]: `{"features":[{"geometry":{"type":"Point","coordinates":[1,2]}}]}`,
		names[2]: `{"features":[{"properties":{"pop_max":300000},"geometry":{"type":"Point","coordinates":"x"}}]}`,
	}
	for name, content := range bad {
		dir := t.TempDir()
		for n, c := range good {
			if n == name {
				c = content
			}
			if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := run(dir, filepath.Join(t.TempDir(), "w.gz")); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestGenworldUnwritableOutput(t *testing.T) {
	if err := run("testdata", filepath.Join(t.TempDir(), "no", "such", "w.gz")); err == nil {
		t.Error("want an error")
	}
}
