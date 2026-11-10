package dashboards

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Ported from the Python test_dashboards.py. test_committed_files_are_current
// and test_dashboards_live_in_the_chart live in tools/gendashboards.

type jmap = map[string]any

// dashboardsJSON parses Render()'s output, like the Python test's dashboards().
func dashboardsJSON(t *testing.T) map[string]jmap {
	t.Helper()
	files, err := Render()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]jmap{}
	for name, text := range files {
		var d jmap
		if err := json.Unmarshal(text, &d); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = d
	}
	return out
}

func panelsOf(d jmap) []jmap {
	var out []jmap
	for _, p := range d["panels"].([]any) {
		out = append(out, p.(jmap))
	}
	return out
}

func allPanels(t *testing.T) []jmap {
	var out []jmap
	for _, d := range dashboardsJSON(t) {
		out = append(out, panelsOf(d)...)
	}
	return out
}

func targetsOf(p jmap) []jmap {
	var out []jmap
	ts, _ := p["targets"].([]any)
	for _, x := range ts {
		out = append(out, x.(jmap))
	}
	return out
}

func TestUIDsAndNav(t *testing.T) {
	d := dashboardsJSON(t)
	var uids []string
	for _, x := range d {
		uids = append(uids, x["uid"].(string))
		if !reflect.DeepEqual(x["tags"], []any{"skyledger"}) {
			t.Errorf("%s tags = %v", x["uid"], x["tags"])
		}
		link := x["links"].([]any)[0].(jmap)
		if !reflect.DeepEqual(link["tags"], []any{"skyledger"}) {
			t.Errorf("%s link tags = %v", x["uid"], link["tags"])
		}
	}
	slices.Sort(uids)
	if want := []string{"skyledger-history", "skyledger-overview", "skyledger-receiver"}; !slices.Equal(uids, want) {
		t.Errorf("uids = %v, want %v", uids, want)
	}
}

func TestLayoutHasNoOverlaps(t *testing.T) {
	for name, dash := range dashboardsJSON(t) {
		type cell struct{ x, y int }
		cells := map[cell]bool{}
		ids := map[float64]bool{}
		for _, p := range panelsOf(dash) {
			id := p["id"].(float64)
			if ids[id] {
				t.Errorf("%s: duplicate id %v", name, id)
			}
			ids[id] = true
			g := p["gridPos"].(jmap)
			x0, y0, w, h := int(g["x"].(float64)), int(g["y"].(float64)), int(g["w"].(float64)), int(g["h"].(float64))
			if x0+w > 24 {
				t.Errorf("%s: %s is wider than the grid", name, p["title"])
			}
			for x := x0; x < x0+w; x++ {
				for y := y0; y < y0+h; y++ {
					if cells[cell{x, y}] {
						t.Fatalf("%s: %s overlaps another panel at %d,%d", name, p["title"], x, y)
					}
					cells[cell{x, y}] = true
				}
			}
		}
	}
}

func TestEveryQueryUsesTheSkyledgerDatasource(t *testing.T) {
	want := jmap{"type": ds.get("type"), "uid": ds.get("uid")}
	for _, p := range allPanels(t) {
		for _, tg := range targetsOf(p) {
			if !reflect.DeepEqual(tg["datasource"], want) {
				t.Errorf("%s: datasource %v", p["title"], tg["datasource"])
			}
			if _, ok := tg["rawSql"]; !ok {
				t.Errorf("%s: target without rawSql", p["title"])
			}
		}
	}
}

func TestNothingSiteSpecific(t *testing.T) {
	files, err := Render()
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, b := range files {
		text.Write(b)
	}
	// (A pinned map centre is covered by TestMapsFitToData; "coords" also names the lat/lon layer mode.)
	for _, banned := range []string{"America/", "Europe/", "kubectl", "prometheus", "adsb-history"} {
		if strings.Contains(text.String(), banned) {
			t.Errorf("rendered dashboards contain %q", banned)
		}
	}
}

func geomaps(t *testing.T) []jmap {
	var maps []jmap
	for _, p := range allPanels(t) {
		if p["type"] == "geomap" {
			maps = append(maps, p)
		}
	}
	return maps
}

func hasMarkersLayer(m jmap) bool {
	for _, l := range m["options"].(jmap)["layers"].([]any) {
		if l.(jmap)["type"] == "markers" {
			return true
		}
	}
	return false
}

func TestMapsFitToData(t *testing.T) {
	maps := geomaps(t)
	if len(maps) != 2 {
		t.Fatalf("got %d maps, want 2", len(maps))
	}
	for _, m := range maps {
		if id := m["options"].(jmap)["view"].(jmap)["id"]; id != "fit" {
			t.Errorf("%s: view id %v", m["title"], id)
		}
		if !hasMarkersLayer(m) {
			t.Errorf("%s: no markers layer", m["title"])
		}
	}
}

func TestAllQueriesCoversEveryPanel(t *testing.T) {
	queries := AllQueries()
	n := 0
	for _, p := range allPanels(t) {
		if p["type"] != "row" {
			n++
		}
	}
	if len(queries) < n {
		t.Errorf("%d queries for %d panels", len(queries), n)
	}
	for name, q := range queries {
		if strings.Contains(q, "history_daily") {
			t.Errorf("%s uses history_daily", name)
		}
	}
}

func TestMapsFitAllLayers(t *testing.T) {
	// Grafana 13 never fits a map to one named layer, so both maps fit all layers; the outline
	// rides along as a markers layer because heatmap and route layers give "fit" no extent.
	fitAll := jmap{"id": "fit", "allLayers": true, "padding": 5.0, "maxZoom": 9.0}
	var titles []string
	for _, m := range geomaps(t) {
		titles = append(titles, m["title"].(string))
		if view := m["options"].(jmap)["view"]; !reflect.DeepEqual(view, fitAll) {
			t.Errorf("%s: view %v", m["title"], view)
		}
		if !hasMarkersLayer(m) {
			t.Errorf("%s: no markers layer", m["title"])
		}
	}
	slices.Sort(titles)
	if want := []string{"Reception range", "Where positions were received"}; !slices.Equal(titles, want) {
		t.Errorf("maps = %v, want %v", titles, want)
	}
}

func TestDayHourGridShowsOneWeekOfDates(t *testing.T) {
	var grid jmap
	for _, p := range panelsOf(dashboardsJSON(t)["skyledger-history.json"]) {
		if p["title"] == "Flights per hour, last 7 days" {
			grid = p
			break
		}
	}
	if grid == nil {
		t.Fatal("no day/hour grid on the history dashboard")
	}
	if grid["timeFrom"] != "7d" {
		t.Errorf("timeFrom = %v", grid["timeFrom"])
	}
	sql := targetsOf(grid)[0]["rawSql"].(string)
	if !strings.Contains(sql, "'Dy DD Mon'") || !strings.Contains(sql, "GROUP BY t::date ORDER BY t::date") {
		t.Errorf("unexpected grid SQL:\n%s", sql)
	}
	for h := range 24 {
		if col := fmt.Sprintf(`"%02d"`, h); !strings.Contains(sql, col) {
			t.Errorf("grid SQL has no %s column", col)
		}
	}
	if w := grid["fieldConfig"].(jmap)["defaults"].(jmap)["custom"].(jmap)["width"]; w != 44.0 {
		t.Errorf("width = %v", w)
	}
}

func TestGroupedQueriesFillEmptyBucketsWithNull(t *testing.T) {
	// Without a fill, an outage returns no rows and Grafana draws a straight line across it.
	grouped := 0
	for name, sql := range panelSQL {
		if !strings.Contains(sql, "$__timeGroupAlias") {
			continue
		}
		grouped++
		if !strings.Contains(sql, "$__timeGroupAlias(time, $__interval, NULL)") {
			t.Errorf("%s does not fill with NULL", name)
		}
	}
	if grouped != 6 {
		t.Errorf("%d grouped queries, want 6", grouped)
	}
}

func TestRawReceiverPanelsBreakLinesAtOutages(t *testing.T) {
	// Raw receiver_stats rows have no NULL-filled buckets: Grafana must insert the gap itself.
	raw := 0
	for _, p := range allPanels(t) {
		if p["type"] != "timeseries" {
			continue
		}
		isRaw := strings.Contains(targetsOf(p)[0]["rawSql"].(string), "FROM receiver_stats")
		custom := p["fieldConfig"].(jmap)["defaults"].(jmap)["custom"].(jmap)
		got, set := custom["insertNulls"]
		if isRaw {
			raw++
			if got != float64(rxGap) {
				t.Errorf("%s: insertNulls = %v, want %d", p["title"], got, rxGap)
			}
		} else if set {
			t.Errorf("%s: insertNulls = %v, want unset", p["title"], got)
		}
	}
	if raw != 5 {
		t.Errorf("%d raw receiver panels, want 5", raw)
	}
}

// Beyond the Python tests: the port's own pieces.

func TestSplitRejectsUnplacedPanel(t *testing.T) {
	full := build()
	full.set("panels", append(full.get("panels").([]any), table("Orphan", "", "SELECT 1", gridPos(0, 0, 1, 1))))
	if _, err := split(full); err == nil || !strings.Contains(err.Error(), "Orphan") {
		t.Errorf("split error = %v, want one naming the orphan panel", err)
	}
}

func TestSplitKeepsKeyOrderAndRenumbers(t *testing.T) {
	dashes, err := split(build())
	if err != nil {
		t.Fatal(err)
	}
	d := dashes["skyledger-overview"]
	// Overridden keys keep their place, as {**full, "uid": ...} does in Python.
	if d.keys[0] != "uid" || d.keys[len(d.keys)-1] != "panels" {
		t.Errorf("dashboard keys = %v", d.keys)
	}
	for i, p := range d.get("panels").([]any) {
		if id := p.(*obj).get("id"); id != i+1 {
			t.Errorf("panel %d has id %v", i, id)
		}
	}
}

func TestDumpsMatchesPython(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{
		{nil, "null"},
		{true, "true"},
		{-45, "-45"},
		{1.0, "1.0"},
		{0.10, "0.1"},
		{math.Copysign(0, -1), "-0.0"},
		{1e-4, "0.0001"},
		{1e-5, "1e-05"},
		{1e16, "1e+16"},
		{9999999999999998.0, "9999999999999998.0"},
		{`a "b" \ <c> & d`, `"a \"b\" \\ <c> & d"`},
		{"\n\r\t\b\f\x01\x7f", `"\n\r\t\b\f\u0001\u007f"`},
		// \x5c is a backslash: e-acute and a plane emoji, escaped as Python does.
		{"caf\xc3\xa9 \U0001F6E9", "\"caf\x5cu00e9 \x5cud83d\x5cudee9\""},
		{[]any{}, "[]"},
		{o(), "{}"},
		{o("b", 1, "a", []any{2, o("c", nil)}), "{\n  \"b\": 1,\n  \"a\": [\n    2,\n    {\n      \"c\": null\n    }\n  ]\n}"},
	} {
		got, err := dumps(c.in)
		if err != nil || string(got) != c.want {
			t.Errorf("dumps(%#v) = %s, %v; want %s", c.in, got, err, c.want)
		}
	}
}

func TestDumpsRejectsWhatPythonWouldNotWrite(t *testing.T) {
	for _, in := range []any{
		o("a", []string{"typed slices are not used"}),
		[]any{math.NaN()},
		o("x", math.Inf(1)),
	} {
		if _, err := dumps(in); err == nil {
			t.Errorf("dumps(%v) succeeded, want an error", in)
		}
	}
}

func TestObjSetKeepsPosition(t *testing.T) {
	m := o("a", 1, "b", 2)
	c := m.clone()
	c.set("a", 3)
	c.set("z", 4)
	if !slices.Equal(c.keys, []string{"a", "b", "z"}) || c.num("a") != 3 {
		t.Errorf("clone after set = %v %v", c.keys, c.vals)
	}
	if !slices.Equal(m.keys, []string{"a", "b"}) || m.num("a") != 1 {
		t.Errorf("set on a clone changed the original: %v %v", m.keys, m.vals)
	}
}
