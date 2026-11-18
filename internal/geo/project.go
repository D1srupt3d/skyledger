// Package geo projects map data around the receiver, the same way the demo's map.json was made.
package geo

import "math"

const (
	// Box is the nm either side of the receiver that map data is clipped to; the rose shows 300.
	Box = 340.0
	// Tolerance is the Douglas-Peucker tolerance, in nm.
	Tolerance = 0.3
	// CityRange is how far from the receiver a city may be, in nm.
	CityRange = 285.0
	// MaxCities is the most cities the map labels.
	MaxCities = 18
	// MinCityPop is the smallest population a city needs to be labelled.
	MinCityPop = 200000

	earthRadiusNM = 3440.065
)

// Point is a position in nm east and north of the receiver.
type Point struct{ E, N float64 }

// Projector returns an azimuthal equidistant projection centred on (lat0, lon0). The
// function it returns takes a lon and lat in degrees and gives the point plus its
// great-circle distance from the centre in nm.
func Projector(lat0, lon0 float64) func(lon, lat float64) (Point, float64) {
	p0, l0 := lat0*math.Pi/180, lon0*math.Pi/180
	return func(lon, lat float64) (Point, float64) {
		p, dl := lat*math.Pi/180, lon*math.Pi/180-l0
		cosc := math.Sin(p0)*math.Sin(p) + math.Cos(p0)*math.Cos(p)*math.Cos(dl)
		c := math.Acos(max(-1, min(1, cosc)))
		k := 1.0
		if c >= 1e-9 {
			k = c / math.Sin(c)
		}
		x := k * math.Cos(p) * math.Sin(dl)
		y := k * (math.Cos(p0)*math.Sin(p) - math.Sin(p0)*math.Cos(p)*math.Cos(dl))
		return Point{x * earthRadiusNM, y * earthRadiusNM}, c * earthRadiusNM
	}
}
