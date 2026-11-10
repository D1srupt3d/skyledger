package ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/D1srupt3d/skyledger/internal/config"
	"github.com/D1srupt3d/skyledger/internal/migrate"
	"github.com/D1srupt3d/skyledger/internal/probe"
)

const aircraftJSON = `{"now": 1000.0, "aircraft": [
	{"hex": "a1", "lat": 1.0, "lon": 2.0, "seen_pos": 1.5, "alt_baro": "ground", "flight": "ABC1    ",
	 "emergency": "none", "nav_modes": []},
	{"hex": "a2", "lat": 1.0, "lon": 2.0, "seen_pos": 30.0, "alt_baro": 30000},
	{"hex": "a3", "alt_baro": 30000},
	{"hex": "a4", "lat": 1.0, "lon": 2.0, "seen_pos": 0.2, "alt_baro": 36000, "flight": "   ",
	 "emergency": "general", "wd": 270, "ws": 85, "oat": -48, "tat": -21, "mach": 0.78,
	 "nav_modes": ["autopilot", "lnav"], "dbFlags": 1, "type": "adsb_icao", "nic": 6, "nac_p": 10,
	 "year": 2005}
]}`

func col(name string) int {
	return slices.IndexFunc(PositionColumns, func(c Column) bool { return c.Name == name })
}

func decode[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseAircraft(t *testing.T) {
	pos, ac, err := ParseAircraft(decode[Snapshot](t, aircraftJSON), 10)
	if err != nil || len(pos) != 2 { // a2 is stale, a3 has no position
		t.Fatalf("%d positions, %v", len(pos), err)
	}
	a1, a4 := pos[0], pos[1]
	check := func(row []any, name string, want any) {
		t.Helper()
		if got := row[col(name)]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s %s: got %#v, want %#v", row[1], name, got, want)
		}
	}
	check(a1, "hex", "a1")
	check(a1, "flight", "ABC1")
	check(a1, "alt_baro", nil)
	check(a1, "on_ground", true)
	check(a1, "emergency", nil)
	check(a1, "nav_modes", []string{})
	check(a1, "wind_speed", nil)
	check(a4, "flight", nil)
	check(a4, "on_ground", false)
	check(a4, "alt_baro", 36000.0)
	check(a4, "emergency", "general")
	check(a4, "wind_dir", 270.0)
	check(a4, "wind_speed", 85.0)
	check(a4, "oat", -48.0)
	check(a4, "mach", 0.78)
	check(a4, "nav_modes", []string{"autopilot", "lnav"})
	check(a4, "source", "adsb_icao")
	check(a4, "nic", 6.0)
	check(a4, "nac_p", 10.0)
	if at := a1[col("time")].(time.Time); at.UnixMilli() != 998500 || at.Location() != time.UTC {
		t.Errorf("time %v", at)
	}
	if ac[0][7] != int16(0) || ac[1][7] != int16(1) { // dbFlags defaults to 0
		t.Errorf("flags %v %v", ac[0][7], ac[1][7])
	}
	if ac[1][5] != "2005" { // a numeric year goes to the text column as text
		t.Errorf("year %#v", ac[1][5])
	}
	for _, r := range pos {
		if len(r) != len(PositionColumns) {
			t.Errorf("row has %d values, want %d", len(r), len(PositionColumns))
		}
	}
	if n := strings.Count(InsertPosition, "$"); n != len(PositionColumns) {
		t.Errorf("INSERT has %d parameters", n)
	}
	if n := strings.Count(UpsertAircraft, "$"); n != len(ac[0]) {
		t.Errorf("UPSERT has %d parameters for %d values", n, len(ac[0]))
	}
}

func TestParseAircraftWithoutHex(t *testing.T) {
	_, _, err := ParseAircraft(decode[Snapshot](t, `{"now": 1, "aircraft": [{"lat": 1, "seen_pos": 0}]}`), 10)
	if err == nil {
		t.Error("want an error")
	}
}

func TestTextConversions(t *testing.T) {
	if text(true) != "true" || text(nil) != nil || text("x") != "x" || text(1.5) != "1.5" {
		t.Error("text")
	}
	if textArray("not a list") != "not a list" {
		t.Error("textArray passes non-lists through")
	}
}

func tableColumns(t *testing.T, table string) []string {
	t.Helper()
	sql, err := migrate.Read("0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)CREATE TABLE ` + table + ` \((.*?)\n\);`).FindStringSubmatch(sql)
	var cols []string
	for _, line := range strings.Split(strings.TrimSpace(m[1]), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			cols = append(cols, f[0])
		}
	}
	return cols
}

func names(cols []Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

func TestCodeMatchesSchema(t *testing.T) {
	got, want := names(PositionColumns), tableColumns(t, "positions")
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("positions: code %v, schema %v", got, want)
	}
	if got, want := names(StatsColumns), tableColumns(t, "receiver_stats"); !slices.Equal(got, want) {
		t.Errorf("receiver_stats: code %v, schema %v", got, want)
	}
}

const statsJSON = `{"now": 1790376975.0, "gain_db": 43.4, "estimated_ppm": -2.7, "aircraft_with_pos": 47,
	"aircraft_without_pos": 6,
	"last1min": {"start": 1790376900.0, "end": 1790376960.0, "messages": 900000, "messages_valid": 24980,
		"position_count_total": 2800, "max_distance": 279093,
		"local": {"signal": -11.0, "noise": -31.0, "peak_signal": -0.9, "strong_signals": 1200,
			"samples_dropped": 0, "samples_lost": 0}}}`

func statsRow(t *testing.T, stats map[string]any) map[string]any {
	t.Helper()
	row := ParseStats(stats)
	if len(row) != len(StatsColumns) {
		t.Fatalf("%d values for %d columns", len(row), len(StatsColumns))
	}
	m := map[string]any{}
	for i, c := range StatsColumns {
		m[c.Name] = row[i]
	}
	return m
}

func TestParseStats(t *testing.T) {
	row := statsRow(t, decode[map[string]any](t, statsJSON))
	if row["time"].(time.Time).Unix() != 1790376960 || row["window_seconds"] != 60.0 {
		t.Errorf("%v", row)
	}
	for k, want := range map[string]any{"signal": -11.0, "noise": -31.0, "messages_valid": 24980.0,
		"max_distance_m": 279093.0, "gain_db": 43.4, "ppm": -2.7, "aircraft_with_pos": 47.0, "samples_lost": 0.0} {
		if row[k] != want {
			t.Errorf("%s: got %v, want %v", k, row[k], want)
		}
	}
}

func TestParseStatsNetworkOnlyReadsb(t *testing.T) {
	stats := decode[map[string]any](t, statsJSON)
	delete(stats["last1min"].(map[string]any), "local")
	delete(stats["last1min"].(map[string]any), "start")
	row := statsRow(t, stats)
	if row["signal"] != nil || row["noise"] != nil || row["window_seconds"] != nil || row["messages_valid"] != 24980.0 {
		t.Errorf("%v", row)
	}
}

func TestParseStatsWithoutWindow(t *testing.T) {
	if ParseStats(map[string]any{"now": 1.0}) != nil || ParseStats(map[string]any{"last1min": map[string]any{}}) != nil {
		t.Error("want nil")
	}
}

// Run keeps going when the database is down, and keeps touching the liveness probe.
func TestRunSurvivesFailedPolls(t *testing.T) {
	dir := t.TempDir()
	probe.Alive, probe.Ready = filepath.Join(dir, "alive"), filepath.Join(dir, "ready")
	t.Cleanup(func() { probe.Alive, probe.Ready = "/tmp/alive", "/tmp/ready" })
	s := config.Settings{Tar1090URL: "http://127.0.0.1:1/", DatabaseURL: "postgresql://u:p@127.0.0.1:1/d",
		Poll: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(probe.Alive); err != nil {
		t.Error("alive not touched")
	}
	if _, err := os.Stat(probe.Ready); err == nil {
		t.Error("ready touched without a write")
	}
	if err := Run(ctx, config.Settings{Tar1090URL: "http://r/%zz"}); err == nil {
		t.Error("bad URL: want an error")
	}
}
