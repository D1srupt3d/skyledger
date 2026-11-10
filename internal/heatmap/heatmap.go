// Package heatmap decodes readsb's heatmap replay files (what tar1090's history replay reads).
//
// Format, little-endian 16-byte records `int32 hex, int32 lat, int32 lon, int16 alt, int16 gs`:
// leading index records; slice markers (`hex == Magic`, ms timestamp in lat/lon words);
// callsign records (`lat >= 2^30`); positions otherwise. Writer: readsb globe_index.c;
// reader: tar1090 script.js. See docs/architecture.md.
package heatmap

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/D1srupt3d/skyledger/internal/feed"
)

const (
	Magic         = 0xE7F7C9D
	AltGround     = -123
	AltUnknown    = -124
	EarthRadiusNM = 3440.065
)

// Sources is readsb's addrtype_t, in enum order: the same names aircraft.json uses for
// `type`, so history rows and live rows share `source` values.
var Sources = []string{
	"adsb_icao", "adsb_icao_nt", "adsr_icao", "tisb_icao", "adsc", "mlat", "other",
	"mode_s", "adsb_other", "adsr_other", "tisb_trackfile", "tisb_other", "mode_ac", "unknown",
}

// Columns of history_positions, in Row.Values order.
var Columns = []string{"time", "hex", "flight", "squawk", "source", "lat", "lon", "alt_baro",
	"on_ground", "gs", "distance_nm", "bearing"}

// Receiver is where the receiver is; range and bearing are measured from it.
type Receiver struct{ Lat, Lon float64 }

// Row is one history_positions row. Nil pointers are NULLs.
type Row struct {
	Time           time.Time
	Hex            string
	Flight, Squawk *string
	Source         string
	Lat, Lon       float64
	AltBaro        *int32
	OnGround       bool
	GS             *float64
	DistanceNM     *float64
	Bearing        *float64
}

// Values is the row in Columns order, for COPY.
func (r Row) Values() []any {
	return []any{r.Time, r.Hex, r.Flight, r.Squawk, r.Source, r.Lat, r.Lon, r.AltBaro, r.OnGround,
		r.GS, r.DistanceNM, r.Bearing}
}

// RangeAndBearing is the great-circle distance (nm) and initial bearing (deg) from the receiver.
func RangeAndBearing(rxLat, rxLon, lat, lon float64) (float64, float64) {
	rad := math.Pi / 180
	p1, p2 := rxLat*rad, lat*rad
	dl := (lon - rxLon) * rad
	a := math.Pow(math.Sin((p2-p1)/2), 2) + math.Cos(p1)*math.Cos(p2)*math.Pow(math.Sin(dl/2), 2)
	dist := 2 * EarthRadiusNM * math.Asin(math.Sqrt(a))
	y := math.Sin(dl) * math.Cos(p2)
	x := math.Cos(p1)*math.Sin(p2) - math.Sin(p1)*math.Cos(p2)*math.Cos(dl)
	// Python's % is always non-negative; Go's math.Mod keeps the sign.
	brg := math.Mod(math.Atan2(y, x)/rad, 360)
	if brg < 0 {
		brg += 360
	}
	return dist, brg
}

// Address is the aircraft address: the low 24 bits, with "~" for a non-ICAO address (bit 24).
func Address(hexField int32) string {
	addr := fmt.Sprintf("%06x", uint32(hexField)&0xFFFFFF)
	if uint32(hexField)&(1<<24) != 0 {
		return "~" + addr
	}
	return addr
}

type ident struct{ flight, squawk *string }

type minuteKey struct {
	hex    string
	minute int64
}

// Decode turns heatmap file bytes (plain or gzipped) into rows, the first sample per aircraft
// per minute. A nil receiver keeps positions but leaves range and bearing NULL.
func Decode(raw []byte, rx *Receiver) ([]Row, error) {
	raw, err := feed.Gunzip(raw)
	if err != nil {
		return nil, err
	}
	if len(raw)%16 != 0 {
		return nil, fmt.Errorf("not a heatmap file: %d bytes", len(raw))
	}
	var (
		sliceMS     int64
		inSlice     bool
		latestIdent = map[string]ident{} // updated as callsign records appear
		firstIdent  = map[string]ident{} // the first one, for positions before it
		seen        = map[minuteKey]bool{}
		rows        []Row
	)
	le := binary.LittleEndian
	for off := 0; off < len(raw); off += 16 {
		rec := raw[off : off+16]
		hx := int32(le.Uint32(rec[0:]))
		lat := int32(le.Uint32(rec[4:]))
		lon := int32(le.Uint32(rec[8:]))
		alt := int16(le.Uint16(rec[12:]))
		gs := int16(le.Uint16(rec[14:]))
		if hx == Magic {
			sliceMS = int64(uint64(uint32(lat))<<32 | uint64(uint32(lon)))
			inSlice = true
			continue
		}
		if !inSlice {
			continue // index records before the first slice
		}
		key := Address(hx)
		// Southern latitudes are negative, so this can't match a position.
		if lat >= 1<<30 {
			name, _, _ := bytes.Cut(rec[8:16], []byte{0})
			id := ident{flight: nonEmpty(strings.TrimSpace(ascii(name))), squawk: ptr(fmt.Sprintf("%04d", lat&0xFFFF))}
			latestIdent[key] = id
			if _, ok := firstIdent[key]; !ok {
				firstIdent[key] = id
			}
			continue
		}
		mk := minuteKey{key, sliceMS / 60000}
		if seen[mk] {
			continue
		}
		seen[mk] = true
		r := Row{
			Time:     time.UnixMilli(sliceMS).UTC(),
			Hex:      key,
			Source:   "unknown",
			Lat:      float64(lat) / 1e6,
			Lon:      float64(lon) / 1e6,
			OnGround: alt == AltGround,
		}
		if src := uint32(hx) >> 27; int(src) < len(Sources) {
			r.Source = Sources[src]
		}
		if rx != nil {
			d, b := RangeAndBearing(rx.Lat, rx.Lon, r.Lat, r.Lon)
			r.DistanceNM, r.Bearing = &d, &b
		}
		if alt != AltGround && alt != AltUnknown {
			r.AltBaro = ptr(int32(alt) * 25)
		}
		if gs != -1 {
			r.GS = ptr(float64(gs) / 10)
		}
		id := latestIdent[key]
		r.Flight, r.Squawk = id.flight, id.squawk
		rows = append(rows, r)
	}
	// Positions seen before their aircraft's first callsign record in this file.
	for i := range rows {
		if id, ok := firstIdent[rows[i].Hex]; ok && rows[i].Flight == nil && rows[i].Squawk == nil {
			rows[i].Flight, rows[i].Squawk = id.flight, id.squawk
		}
	}
	return rows, nil
}

// ascii decodes like Python's decode("ascii", "replace").
func ascii(b []byte) string {
	var s strings.Builder
	for _, c := range b {
		if c < 0x80 {
			s.WriteByte(c)
		} else {
			s.WriteRune('�')
		}
	}
	return s.String()
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ptr[T any](v T) *T { return &v }
