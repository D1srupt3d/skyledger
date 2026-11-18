package geo

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
)

//go:embed world.json.gz
var worldGz []byte

// City is a labelled place, in nm east and north of the receiver.
type City struct {
	Name    string  `json:"name"`
	E       float64 `json:"e"`
	N       float64 `json:"n"`
	Pop     int     `json:"pop"`
	Capital bool    `json:"capital"`
}

// Map is the land, borders and cities around a receiver; points are [east, north] in nm.
type Map struct {
	Center  *[2]float64    `json:"center"`
	Land    [][][2]float64 `json:"land"`
	Borders [][][2]float64 `json:"borders"`
	Cities  []City         `json:"cities"`
}

// world is the file made by tools/genworld; coordinates are [lon, lat].
type world struct {
	Land    [][][2]float64 `json:"land"`
	Borders [][][2]float64 `json:"borders"`
	Cities  []struct {
		Name    string  `json:"name"`
		Lon     float64 `json:"lon"`
		Lat     float64 `json:"lat"`
		Pop     int     `json:"pop"`
		Capital bool    `json:"capital"`
	} `json:"cities"`
}

// Build projects the embedded world around (lat, lon), as the demo's map.json was made.
// It decodes the world on every call, so call it rarely.
func Build(lat, lon float64) (Map, error) {
	var w world
	zr, err := gzip.NewReader(bytes.NewReader(worldGz))
	if err == nil {
		err = json.NewDecoder(zr).Decode(&w)
	}
	if err != nil {
		return Map{}, fmt.Errorf("geo: decode world: %w", err)
	}
	proj := Projector(lat, lon)
	m := Map{Center: &[2]float64{lat, lon}, Land: [][][2]float64{}, Borders: [][][2]float64{}, Cities: []City{}}

	for _, ring := range w.Land {
		pts := make([]Point, len(ring))
		near := math.Inf(1)
		for i, c := range ring {
			var d float64
			pts[i], d = proj(c[0], c[1])
			near = math.Min(near, d)
		}
		if near > Box*1.5 {
			continue // nowhere near the receiver
		}
		if clipped := ClipPolygon(pts, Box); len(clipped) >= 3 {
			m.Land = append(m.Land, round(Simplify(append(clipped, clipped[0]), Tolerance)))
		}
	}
	for _, line := range w.Borders {
		pts := make([]Point, len(line))
		for i, c := range line {
			pts[i], _ = proj(c[0], c[1])
		}
		for _, run := range ClipLine(pts, Box) {
			m.Borders = append(m.Borders, round(Simplify(run, Tolerance)))
		}
	}
	for _, c := range w.Cities {
		p, d := proj(c.Lon, c.Lat)
		if d <= CityRange && c.Pop >= MinCityPop {
			m.Cities = append(m.Cities, City{c.Name, round1(p.E), round1(p.N), c.Pop, c.Capital})
		}
	}
	slices.SortStableFunc(m.Cities, func(a, b City) int { return b.Pop - a.Pop })
	m.Cities = m.Cities[:min(len(m.Cities), MaxCities)]
	return m, nil
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

func round(pts []Point) [][2]float64 {
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		out[i] = [2]float64{round1(p.E), round1(p.N)}
	}
	return out
}
