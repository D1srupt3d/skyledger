// Package demo is a fake tar1090 serving synthetic, deterministic data: for tests, screenshots and trying skyledger.
//
// Fictional aircraft fly straight tracks across a made-up receiver in the middle of the
// North Atlantic, then stay out of range for a while before the next pass, so history splits
// into separate flights. Everything is computed from the clock, so any two requests for the
// same moment agree, and the heatmap history covers the last Days days.
package demo

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/D1srupt3d/skyledger/internal/heatmap"
)

var Receiver = heatmap.Receiver{Lat: 45.0, Lon: -35.0}

const (
	DBVersion = "db-demo-1"
	halfHour  = 1800
)

// Days of heatmap history served, from DEMO_DAYS (default 7).
var Days = func() int {
	if n, err := strconv.Atoi(os.Getenv("DEMO_DAYS")); err == nil {
		return n
	}
	return 7
}()

// Plane flies from Bearing at Radius nm, straight through near the receiver, is gone for
// gap minutes, and repeats.
type Plane struct {
	Hex                   int32
	Callsign, TypeCode    string
	Description           string
	Alt, Speed, Squawk    int
	Mil                   bool
	startLat, startLon    float64
	endLat, endLon        float64
	period, cycle, offset float64
}

func newPlane(index int, callsign string, bearing, radius float64, alt, speed int, offsetMin, gapMin float64,
	mil bool, squawk int, missNM float64) *Plane {
	p := &Plane{Hex: 0xDE0000 + int32(index), Callsign: callsign, Alt: alt, Speed: speed, Mil: mil, Squawk: squawk}
	switch {
	case mil:
		p.TypeCode, p.Description = "C17", "DEMO MILITARY TRANSPORT"
	case alt > 20000:
		p.TypeCode, p.Description = "B38M", "DEMO JETLINER"
	default:
		p.TypeCode, p.Description = "C172", "DEMO LIGHT AIRCRAFT"
	}
	p.startLat, p.startLon = destination(Receiver.Lat, Receiver.Lon, bearing, radius)
	// Aim missNM to the side of the receiver, so closest approaches differ.
	aimLat, aimLon := destination(Receiver.Lat, Receiver.Lon, math.Mod(bearing+90, 360), missNM)
	p.endLat, p.endLon = 2*aimLat-p.startLat, 2*aimLon-p.startLon
	// Whole minutes, so a plane is in or out of range for a whole minute at a time.
	p.period = math.RoundToEven(2*radius/float64(speed)*60) * 60
	p.cycle = p.period + gapMin*60
	p.offset = offsetMin * 60
	return p
}

// At is the plane's position at Unix time t, or ok false while out of range.
func (p *Plane) At(t float64) (lat, lon float64, ok bool) {
	phase := math.Mod(t-p.offset, p.cycle)
	if phase < 0 {
		phase += p.cycle
	}
	if phase >= p.period {
		return 0, 0, false
	}
	f := phase / p.period
	return p.startLat + (p.endLat-p.startLat)*f, p.startLon + (p.endLon-p.startLon)*f, true
}

func destination(lat, lon, bearing, nm float64) (float64, float64) {
	rad := math.Pi / 180
	d := nm / 3440.065
	p1, l1, b := lat*rad, lon*rad, bearing*rad
	p2 := math.Asin(math.Sin(p1)*math.Cos(d) + math.Cos(p1)*math.Sin(d)*math.Cos(b))
	l2 := l1 + math.Atan2(math.Sin(b)*math.Sin(d)*math.Cos(p1), math.Cos(d)-math.Sin(p1)*math.Sin(p2))
	return p2 / rad, l2 / rad
}

// Planes: callsign, bearing, radius, alt, speed, offset min, gap min, military, squawk, miss nm.
var Planes = []*Plane{
	// Airliners: regulars, back every 40 minutes to 3 hours. 101/505 and 202/707 are
	// pairs on the same cycle, each arriving as the other leaves (a pass must outlast its
	// partner's gap): never an empty sky.
	newPlane(1, "DMO101", 20, 240, 37000, 480, 0, 40, false, 2101, 30), // 60 min pass, 100 min cycle
	newPlane(2, "DMO202", 70, 220, 35000, 460, 9, 53, false, 2202, 5),  // 57 min pass, 110 min cycle
	newPlane(3, "DMO303", 110, 200, 39000, 500, 17, 90, false, 2303, 60),
	newPlane(4, "DMO404", 160, 250, 33000, 450, 26, 120, false, 2404, 15),
	newPlane(5, "DMO505", 200, 230, 36000, 470, 60, 41, false, 2505, 45), // 59 min pass, 100 min cycle
	newPlane(6, "DMO606", 250, 260, 38000, 490, 44, 180, false, 2606, 8),
	newPlane(7, "DMO707", 290, 210, 34000, 455, 66, 55, false, 2707, 70), // 55 min pass, 110 min cycle
	newPlane(8, "DMO808", 330, 245, 40000, 505, 61, 150, false, 2808, 20),
	newPlane(9, "DEMOX1", 45, 120, 3000, 160, 4, 270, false, 7000, 1),      // low and slow: "lowest aircraft"
	newPlane(10, "RCH99", 135, 180, 28000, 420, 21, 3550, true, 4521, 25),  // military, every ~2.5 days: rare
	newPlane(11, "DMO911", 300, 200, 31000, 440, 38, 240, false, 7700, 35), // squawking 7700
}

// Seen is a plane in range and where it is.
type Seen struct {
	Plane    *Plane
	Lat, Lon float64
}

// Visible lists the planes in range at Unix time t.
func Visible(t float64) []Seen {
	var out []Seen
	for _, p := range Planes {
		if lat, lon, ok := p.At(t); ok {
			out = append(out, Seen{p, lat, lon})
		}
	}
	return out
}

func round6(v float64) float64 { return math.RoundToEven(v*1e6) / 1e6 }

func AircraftJSON(now float64) map[string]any {
	aircraft := []map[string]any{}
	for _, s := range Visible(now) {
		p := s.Plane
		a := map[string]any{
			"hex":       fmt.Sprintf("%06x", p.Hex),
			"type":      "adsb_icao",
			"flight":    fmt.Sprintf("%-8s", p.Callsign),
			"lat":       round6(s.Lat),
			"lon":       round6(s.Lon),
			"alt_baro":  p.Alt,
			"alt_geom":  p.Alt + 400,
			"gs":        p.Speed,
			"track":     90.0,
			"seen_pos":  0.4,
			"seen":      0.2,
			"rssi":      -20.0,
			"squawk":    fmt.Sprintf("%04d", p.Squawk),
			"emergency": "none",
			"nic":       8,
			"nac_p":     10,
			"r":         fmt.Sprintf("DEMO-%02d", p.Hex&0xFF),
			"t":         p.TypeCode,
			"desc":      p.Description,
			"ownOp":     "DEMO AIR",
			"category":  "A1",
		}
		if p.Squawk == 7700 {
			a["emergency"] = "general"
		}
		if p.Alt > 20000 {
			a["category"] = "A3"
		}
		if p.Mil {
			a["dbFlags"] = 1
		}
		if p.Alt >= 20000 {
			// Weather aloft: a westerly jet that strengthens with altitude, cold air.
			for k, v := range map[string]any{
				"wd": 270, "ws": 40 + p.Alt/1000, "oat": -56, "tat": -30, "ias": 280, "tas": p.Speed,
				"mach": 0.78, "roll": 0.0, "nav_altitude_mcp": p.Alt, "nav_modes": []string{"autopilot", "lnav"},
				"nav_qnh": 1013.2,
			} {
				a[k] = v
			}
		}
		aircraft = append(aircraft, a)
	}
	return map[string]any{"now": now, "messages": int64(now) % 10_000_000, "aircraft": aircraft}
}

func StatsJSON(now float64) map[string]any {
	end := now - math.Mod(now, 60)
	return map[string]any{
		"now":                  now,
		"gain_db":              42.1,
		"estimated_ppm":        -1.5,
		"aircraft_with_pos":    len(Visible(now)),
		"aircraft_without_pos": 2,
		"last1min": map[string]any{
			"start":                end - 60,
			"end":                  end,
			"messages":             600000,
			"messages_valid":       18000,
			"position_count_total": 2100,
			"max_distance":         240 * 1852,
			"local": map[string]any{
				"signal":          -12.0,
				"noise":           -32.0,
				"peak_signal":     -1.5,
				"strong_signals":  700,
				"samples_dropped": 0,
				"samples_lost":    0,
			},
		},
	}
}

// HeatmapFile is the replay file for one UTC half hour of day.
func HeatmapFile(day time.Time, half int) []byte {
	start := float64(day.Unix() + int64(half)*halfHour)
	var slices []heatmap.Slice
	for k := range 120 { // 15 s slices
		t := start + float64(k*15)
		var records [][]byte
		for _, s := range Visible(t) {
			p := s.Plane
			if k%4 == 0 { // callsign records about once a minute, like readsb
				records = append(records, heatmap.Callsign(p.Hex, p.Callsign, int32(p.Squawk)))
			}
			alt, gs := float64(p.Alt), float64(p.Speed)
			records = append(records, heatmap.Position(p.Hex, s.Lat, s.Lon, &alt, &gs, false, 0))
		}
		slices = append(slices, heatmap.Slice{StartMS: int64(t * 1000), Records: records})
	}
	return heatmap.Build(slices, 15000)
}

// DBFiles is tar1090's aircraft database folder.
func DBFiles() map[string]any {
	shard := map[string][]string{}
	for _, p := range Planes {
		flags := "00"
		if p.Mil {
			flags = "10"
		}
		shard[fmt.Sprintf("%06X", p.Hex)[2:]] = []string{fmt.Sprintf("DEMO-%02d", p.Hex&0xFF), p.TypeCode, flags, p.Description}
	}
	return map[string]any{
		"files.js": []string{"DE"},
		"DE.js":    shard,
		"operators.js": map[string]map[string]string{
			"DMO": {"n": "Demo Air", "c": "Nowhere", "r": "DEMO"},
			"RCH": {"n": "Demo Air Force", "c": "Nowhere", "r": "REACH"},
		},
	}
}

var heatmapPath = regexp.MustCompile(`^/globe_history/(\d{4})/(\d{2})/(\d{2})/heatmap/(\d{2})\.bin\.ttf$`)

func jsonBody(v any) []byte {
	b, _ := json.Marshal(v) // maps of plain values always marshal
	return b
}

// Respond is (status, content type, body) for a request path at Unix time now.
func Respond(path string, now float64) (int, string, []byte) {
	switch path {
	case "/":
		return 200, "text/html", []byte(`<script>databaseFolder = "` + DBVersion + `";</script>`)
	case "/data/aircraft.json":
		return 200, "application/json", jsonBody(AircraftJSON(now))
	case "/data/stats.json":
		return 200, "application/json", jsonBody(StatsJSON(now))
	case "/data/receiver.json":
		return 200, "application/json", jsonBody(map[string]any{"lat": Receiver.Lat, "lon": Receiver.Lon,
			"version": "skyledger demo"})
	}
	if strings.HasPrefix(path, "/"+DBVersion+"/") {
		name := path[strings.LastIndex(path, "/")+1:]
		if v, ok := DBFiles()[name]; ok {
			return 200, "application/javascript", jsonBody(v)
		}
	}
	if m := heatmapPath.FindStringSubmatch(path); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		half, _ := strconv.Atoi(m[4])
		day := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
		start := float64(day.Unix() + int64(half)*halfHour)
		nowT := time.Unix(int64(now), 0).UTC()
		today := time.Date(nowT.Year(), nowT.Month(), nowT.Day(), 0, 0, 0, 0, time.UTC)
		// Only finished half hours within the last Days days exist, like a real receiver.
		if half < 48 && start+halfHour <= now && int(today.Sub(day).Hours()/24) <= Days {
			return 200, "application/octet-stream", HeatmapFile(day, half)
		}
	}
	return 404, "text/plain", []byte("not found")
}

// Handler serves Respond at the current time.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := float64(time.Now().UnixMicro()) / 1e6
		status, ctype, body := Respond(r.URL.Path, now)
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

// Serve is `skyledger demo-feed`.
func Serve(port int) error {
	slog.Info("demo tar1090", "port", port, "receiver", Receiver, "days", Days)
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: Handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.ListenAndServe()
}
