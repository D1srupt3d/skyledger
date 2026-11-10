package demo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/D1srupt3d/skyledger/internal/aircraftdb"
	"github.com/D1srupt3d/skyledger/internal/heatmap"
	"github.com/D1srupt3d/skyledger/internal/ingest"
)

// The demo feed must be readable by skyledger's real parsers, end to end.

var now = float64(time.Date(2026, 5, 20, 12, 34, 56, 0, time.UTC).Unix())

func get(t *testing.T, path string, at float64) (int, []byte) {
	t.Helper()
	status, _, body := Respond(path, at)
	return status, body
}

func minutes(start, hours float64) []float64 {
	var out []float64
	for t := int64(start); t < int64(start+hours*3600); t += 60 {
		out = append(out, float64(t))
	}
	return out
}

func parse(t *testing.T, at float64) (positions, aircraft [][]any) {
	t.Helper()
	_, body := get(t, "/data/aircraft.json", at)
	var snap ingest.Snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatal(err)
	}
	positions, aircraft, err := ingest.ParseAircraft(snap, 10)
	if err != nil {
		t.Fatal(err)
	}
	return positions, aircraft
}

func TestLiveFeedParses(t *testing.T) {
	pos, ac := parse(t, now)
	if len(pos) != len(Visible(now)) || len(ac) != len(pos) || len(pos) == 0 {
		t.Fatalf("%d positions, %d visible", len(pos), len(Visible(now)))
	}
	nic := slices.IndexFunc(ingest.PositionColumns, func(c ingest.Column) bool { return c.Name == "nic" })
	for _, r := range pos {
		if r[nic] != 8.0 {
			t.Errorf("nic %v", r[nic])
		}
	}
	// The military one is a rare visitor: find a moment it's in range.
	rch := Planes[slices.IndexFunc(Planes, func(p *Plane) bool { return p.Mil })]
	i := slices.IndexFunc(minutes(now, 72), func(t float64) bool { _, _, ok := rch.At(t); return ok })
	_, ac = parse(t, minutes(now, 72)[i])
	mil := 0
	for _, a := range ac {
		if a[7] == int16(1) {
			mil++
		}
	}
	if mil != 1 {
		t.Errorf("%d military aircraft", mil)
	}
}

func TestPositionsStayInRangeAndMove(t *testing.T) {
	for _, at := range minutes(now, 24) {
		for _, s := range Visible(at) {
			d, _ := heatmap.RangeAndBearing(Receiver.Lat, Receiver.Lon, s.Lat, s.Lon)
			if d >= 270 {
				t.Errorf("%s at %v nm", s.Plane.Callsign, d)
			}
			if lat, lon, ok := s.Plane.At(at + 30); ok && lat == s.Lat && lon == s.Lon {
				t.Errorf("%s didn't move", s.Plane.Callsign)
			}
		}
	}
}

// Each plane leaves range for over 30 minutes between passes, so history splits into many flights.
func TestPlanesComeAndGo(t *testing.T) {
	for _, p := range Planes {
		var seen strings.Builder // x: in range
		for _, at := range minutes(now, float64(24*Days)) {
			if _, _, ok := p.At(at); ok {
				seen.WriteByte('x')
			} else {
				seen.WriteByte('.')
			}
		}
		passes := strings.FieldsFunc(seen.String(), func(r rune) bool { return r == '.' })
		gaps := strings.FieldsFunc(strings.Trim(seen.String(), "."), func(r rune) bool { return r == 'x' })
		if len(passes) < 2 {
			t.Errorf("%s: %d passes", p.Callsign, len(passes))
		}
		for _, g := range gaps {
			if len(g) <= 30 {
				t.Errorf("%s: a %d minute gap", p.Callsign, len(g))
			}
		}
	}
}

func TestNeverAnEmptySky(t *testing.T) {
	for _, at := range minutes(now, float64(24*Days)) {
		if n := len(Visible(at)); n < 2 {
			t.Fatalf("%d aircraft at %v", n, at)
		}
	}
}

func TestOneRareVisitor(t *testing.T) {
	days := map[string]map[int64]bool{}
	for _, at := range minutes(now-float64(86400*Days), float64(24*Days)) {
		for _, s := range Visible(at) {
			if days[s.Plane.TypeCode] == nil {
				days[s.Plane.TypeCode] = map[int64]bool{}
			}
			days[s.Plane.TypeCode][int64(at)/86400] = true
		}
	}
	var rare []string
	for typ, d := range days {
		if len(d) <= 3 {
			rare = append(rare, typ)
		}
	}
	if !slices.Equal(rare, []string{"C17"}) {
		t.Errorf("rare %v", rare)
	}
}

func TestStatsParse(t *testing.T) {
	status, body := get(t, "/data/stats.json", now)
	var stats map[string]any
	if err := json.Unmarshal(body, &stats); err != nil {
		t.Fatal(err)
	}
	row := ingest.ParseStats(stats)
	if status != 200 || row[2] != -12.0 || row[0].(time.Time).Unix()%60 != 0 {
		t.Errorf("%d %v", status, row)
	}
}

func TestDatabase(t *testing.T) {
	status, body := get(t, "/", now)
	version, err := aircraftdb.DBVersion(string(body))
	if status != 200 || err != nil || version != DBVersion {
		t.Fatalf("%d %q %v", status, version, err)
	}
	var shards []string
	_, body = get(t, "/"+version+"/files.js", now)
	_ = json.Unmarshal(body, &shards)
	var rows [][]any
	for _, s := range shards {
		var data map[string]json.RawMessage
		_, body = get(t, "/"+version+"/"+s+".js", now)
		_ = json.Unmarshal(body, &data)
		r, err := aircraftdb.DBRows(s, data)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r...)
	}
	var hexes, want []string
	mil := 0
	for _, r := range rows {
		hexes = append(hexes, r[0].(string))
		if r[4].(int16)&1 != 0 {
			mil++
		}
	}
	for _, p := range Planes {
		want = append(want, fmt.Sprintf("%06x", p.Hex))
	}
	slices.Sort(want)
	if !slices.Equal(hexes, want) || mil != 1 {
		t.Errorf("%v %d", hexes, mil)
	}
	var ops map[string]aircraftdb.Operator
	_, body = get(t, "/"+version+"/operators.js", now)
	_ = json.Unmarshal(body, &ops)
	if a := aircraftdb.AirlineRows(ops); a[0][1] != "Demo Air" {
		t.Errorf("%v", a)
	}
	if status, _ := get(t, "/"+version+"/nope.js", now); status != 404 {
		t.Error("unknown database file served")
	}
}

func TestHeatmapHistory(t *testing.T) {
	day := time.Unix(int64(now), 0).UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	status, body := get(t, "/globe_history/"+day.Format("2006/01/02")+"/heatmap/10.bin.ttf", now)
	rx := Receiver
	rows, err := heatmap.Decode(body, &rx)
	if err != nil {
		t.Fatal(err)
	}
	start := float64(day.Add(5 * time.Hour).Unix())
	expected := 0
	for _, at := range minutes(start, 0.5) { // one per aircraft per minute
		expected += len(Visible(at))
	}
	if status != 200 || len(rows) != expected || expected == 0 {
		t.Fatalf("%d: %d rows, want %d", status, len(rows), expected)
	}
	for _, r := range rows {
		if r.Flight == nil || *r.DistanceNM >= 270 {
			t.Errorf("%+v", r)
		}
	}
}

func TestHeatmapOnlyFinishedRecentHalfHours(t *testing.T) {
	today := time.Unix(int64(now), 0).UTC().Truncate(24 * time.Hour)
	old := today.AddDate(0, 0, -(Days + 1))
	for path, want := range map[string]int{
		"/globe_history/" + today.Format("2006/01/02") + "/heatmap/47.bin.ttf": 404, // later today
		"/globe_history/" + today.Format("2006/01/02") + "/heatmap/00.bin.ttf": 200, // earlier today
		"/globe_history/" + old.Format("2006/01/02") + "/heatmap/00.bin.ttf":   404,
		"/nope": 404,
	} {
		if status, _ := get(t, path, now); status != want {
			t.Errorf("%s: %d, want %d", path, status, want)
		}
	}
}

func TestHandlerServesReceiver(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/data/receiver.json?x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var rx map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&rx)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/json" || rx["lat"] != 45.0 {
		t.Errorf("%d %v", resp.StatusCode, rx)
	}
}

func TestServeBadPort(t *testing.T) {
	if err := Serve(-1); err == nil {
		t.Error("want an error")
	}
}
