// Command genworld shrinks Natural Earth GeoJSON into internal/geo/world.json.gz.
//
// Usage: go run ./tools/genworld <natural-earth dir> internal/geo/world.json.gz
//
// The directory must hold ne_10m_land.geojson, ne_10m_admin_0_boundary_lines_land.geojson
// and ne_10m_populated_places_simple.geojson. Coordinates are rounded to 4 decimals and
// only cities with a population of 200000 or more are kept.
package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

const minPop = 200000

type world struct {
	Land    [][][2]float64 `json:"land"`
	Borders [][][2]float64 `json:"borders"`
	Cities  []city         `json:"cities"`
}

type city struct {
	Name    string  `json:"name"`
	Lon     float64 `json:"lon"`
	Lat     float64 `json:"lat"`
	Pop     int     `json:"pop"`
	Capital bool    `json:"capital"`
}

type collection struct {
	Features []struct {
		Properties struct {
			Name   string   `json:"name"`
			PopMax *float64 `json:"pop_max"`
			Cap    *float64 `json:"adm0cap"`
		} `json:"properties"`
		Geometry struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: genworld <natural-earth dir> <out world.json.gz>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dataDir, out string) error {
	var w world
	land, err := load(dataDir, "ne_10m_land.geojson")
	if err != nil {
		return err
	}
	for _, f := range land.Features {
		rings, err := paths(f.Geometry.Type, f.Geometry.Coordinates, "Polygon", "MultiPolygon")
		if err != nil {
			return err
		}
		w.Land = append(w.Land, rings...)
	}
	borders, err := load(dataDir, "ne_10m_admin_0_boundary_lines_land.geojson")
	if err != nil {
		return err
	}
	for _, f := range borders.Features {
		lines, err := paths(f.Geometry.Type, f.Geometry.Coordinates, "LineString", "MultiLineString")
		if err != nil {
			return err
		}
		w.Borders = append(w.Borders, lines...)
	}
	places, err := load(dataDir, "ne_10m_populated_places_simple.geojson")
	if err != nil {
		return err
	}
	for _, f := range places.Features {
		if f.Properties.PopMax == nil || *f.Properties.PopMax < minPop {
			continue
		}
		var c []float64
		if err := json.Unmarshal(f.Geometry.Coordinates, &c); err != nil || len(c) < 2 {
			return fmt.Errorf("city %q: bad coordinates", f.Properties.Name)
		}
		w.Cities = append(w.Cities, city{f.Properties.Name, round4(c[0]), round4(c[1]),
			int(*f.Properties.PopMax), f.Properties.Cap != nil && *f.Properties.Cap == 1})
	}
	if err := write(out, w); err != nil {
		return err
	}
	st, err := os.Stat(out)
	if err != nil {
		return err
	}
	fmt.Printf("land rings %d, border lines %d, cities %d, %s is %d bytes\n",
		len(w.Land), len(w.Borders), len(w.Cities), out, st.Size())
	return nil
}

func load(dir, name string) (collection, error) {
	var c collection
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", name, err)
	}
	return c, nil
}

// paths flattens a (Multi)Polygon's rings or a (Multi)LineString's lines into rounded paths.
// single is the one-shape type, multi the type holding a list of them.
func paths(typ string, raw json.RawMessage, single, multi string) ([][][2]float64, error) {
	var shapes [][][][]float64 // shape > path > point > lon, lat
	switch typ {
	case single:
		var s [][][]float64
		if err := json.Unmarshal(raw, &s); err != nil {
			// A LineString is one path, not a list of them.
			var l [][]float64
			if err2 := json.Unmarshal(raw, &l); err2 != nil {
				return nil, fmt.Errorf("bad %s coordinates: %w", typ, err)
			}
			s = [][][]float64{l}
		}
		shapes = [][][][]float64{s}
	case multi:
		if err := json.Unmarshal(raw, &shapes); err != nil {
			// MultiLineString is a list of paths.
			var s [][][]float64
			if err2 := json.Unmarshal(raw, &s); err2 != nil {
				return nil, fmt.Errorf("bad %s coordinates: %w", typ, err)
			}
			shapes = [][][][]float64{s}
		}
	default:
		return nil, fmt.Errorf("unexpected geometry %q, want %s or %s", typ, single, multi)
	}
	var out [][][2]float64
	for _, s := range shapes {
		for _, p := range s {
			var path [][2]float64
			for _, pt := range p {
				if len(pt) < 2 {
					return nil, fmt.Errorf("%s: point with fewer than 2 coordinates", typ)
				}
				path = append(path, [2]float64{round4(pt[0]), round4(pt[1])})
			}
			out = append(out, path)
		}
	}
	return out, nil
}

func round4(x float64) float64 { return math.Round(x*1e4) / 1e4 }

func write(out string, w world) (err error) {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(zw).Encode(w); err != nil {
		return err
	}
	return zw.Close()
}
