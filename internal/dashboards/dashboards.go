// Package dashboards generates skyledger's Grafana dashboards (classic JSON
// schema) for charts/skyledger/grafana/dashboards/ (Compose mounts it, the
// Helm chart packages it). tools/gendashboards writes or checks the files.
package dashboards

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
)

var ds = o("type", "grafana-postgresql-datasource", "uid", "skyledger")

var lastID atomic.Int64

func nid() int { return int(lastID.Add(1)) }

func target(sql, format string) *obj {
	return o(
		"refId", "A",
		"datasource", ds,
		"editorMode", "code",
		"format", format,
		"rawQuery", true,
		"rawSql", sql,
	)
}

// withRefID is Python's dict(target(...), refId=ref).
func withRefID(t *obj, ref string) *obj {
	t.set("refId", ref)
	return t
}

func gridPos(x, y, w, h int) *obj { return o("x", x, "y", y, "w", w, "h", h) }

func row(title string, y int) *obj {
	return o(
		"id", nid(),
		"type", "row",
		"title", title,
		"collapsed", false,
		"panels", []any{},
		"gridPos", gridPos(0, y, 24, 1),
	)
}

// statOpts are stat()'s keyword arguments. Zero values mean the Python default.
type statOpts struct {
	unit     string // default "none"
	steps    []any
	decimals any // nil: not set
	noValue  string
}

func stat(title, desc, sql string, pos *obj, opt statOpts) *obj {
	steps := opt.steps
	if len(steps) == 0 {
		steps = []any{o("value", nil, "color", "text")}
	}
	defaults := o(
		"unit", cmp.Or(opt.unit, "none"),
		"color", o("mode", "thresholds"),
		"thresholds", o("mode", "absolute", "steps", steps),
	)
	if opt.decimals != nil {
		defaults.set("decimals", opt.decimals)
	}
	if opt.noValue != "" {
		defaults.set("noValue", opt.noValue)
	}
	colorMode := "background"
	if len(opt.steps) == 0 {
		colorMode = "value"
	}
	return o(
		"id", nid(),
		"type", "stat",
		"title", title,
		"description", desc,
		"datasource", ds,
		"targets", []any{target(sql, "table")},
		"fieldConfig", o("defaults", defaults, "overrides", []any{}),
		"options", o(
			"reduceOptions", o("calcs", []any{"lastNotNull"}, "fields", "", "values", false),
			"colorMode", colorMode,
			"graphMode", "none",
			"textMode", "value",
		),
		"gridPos", pos,
	)
}

// tsOpts are timeseries()'s keyword arguments. Zero values mean the Python default.
type tsOpts struct {
	unit        string // default "none"
	stack       bool
	interval    string // default "1m"
	draw        string // default "line"
	legend      any    // nil: same as stack
	noMinimum   bool   // Python's minimum=None; otherwise min is 0
	maximum     any    // nil: not set
	points      bool
	thresholds  []any
	timeFrom    string
	insertNulls int
}

func timeseries(title, desc, sql string, pos *obj, opt tsOpts) *obj {
	draw := cmp.Or(opt.draw, "line")
	fillOpacity := 10
	if opt.stack {
		fillOpacity = 20
	} else if draw == "points" {
		fillOpacity = 0
	}
	showPoints := "never"
	if draw == "points" || opt.points {
		showPoints = "always"
	}
	stacking := "none"
	if opt.stack {
		stacking = "normal"
	}
	custom := o(
		"drawStyle", draw,
		"lineWidth", 1,
		"fillOpacity", fillOpacity,
		"pointSize", 4,
		"showPoints", showPoints,
		"spanNulls", false,
		"stacking", o("mode", stacking, "group", "A"),
	)
	defaults := o("unit", cmp.Or(opt.unit, "none"), "custom", custom)
	if !opt.noMinimum {
		defaults.set("min", 0)
	}
	if len(opt.thresholds) > 0 {
		defaults.set("thresholds", o("mode", "absolute", "steps", opt.thresholds))
		custom.set("thresholdsStyle", o("mode", "dashed"))
	}
	if opt.maximum != nil {
		defaults.set("max", opt.maximum)
	}
	if opt.insertNulls != 0 {
		custom.set("insertNulls", opt.insertNulls)
	}
	showLegend := opt.stack
	if l, ok := opt.legend.(bool); ok {
		showLegend = l
	}
	tooltip := "single"
	if showLegend {
		tooltip = "multi"
	}
	var extra []any
	if opt.timeFrom != "" {
		extra = []any{"timeFrom", opt.timeFrom}
	}
	return o(append(extra,
		"id", nid(),
		"type", "timeseries",
		"title", title,
		"description", desc,
		"datasource", ds,
		"interval", cmp.Or(opt.interval, "1m"),
		"targets", []any{target(sql, "time_series")},
		"fieldConfig", o("defaults", defaults, "overrides", []any{}),
		"options", o(
			"legend", o("displayMode", "list", "placement", "bottom", "showLegend", showLegend),
			"tooltip", o("mode", tooltip, "sort", "desc"),
		),
		"gridPos", pos,
	)...)
}

func table(title, desc, sql string, pos *obj, overrides ...any) *obj {
	if overrides == nil {
		overrides = []any{}
	}
	return o(
		"id", nid(),
		"type", "table",
		"title", title,
		"description", desc,
		"datasource", ds,
		"targets", []any{target(sql, "table")},
		"fieldConfig", o("defaults", o("custom", o("align", "auto")), "overrides", overrides),
		"options", o("showHeader", true, "cellHeight", "sm"),
		"gridPos", pos,
	)
}

// unitOverride sets a column's unit, and its decimals unless decimals is nil.
func unitOverride(field, unit string, decimals any) *obj {
	props := []any{o("id", "unit", "value", unit)}
	if decimals != nil {
		props = append(props, o("id", "decimals", "value", decimals))
	}
	return o("matcher", o("id", "byName", "options", field), "properties", props)
}

// --- SQL -------------------------------------------------------------------
// Grafana macros: $__timeFilter(col), $__timeGroupAlias(col, $__interval, NULL).
// The NULL fills empty buckets so an outage shows as a gap, not a straight line.
// With timescaledb: true on the datasource, $__timeGroup uses time_bucket().
// Receiver panels plot raw per-minute rows instead: Grafana breaks the line where
// rows are further apart than rxGap (ms).
const rxGap = 180000

// Altitude band for the weather panels; callers filter alt_baro IS NOT NULL.
const bandSQL = "CASE WHEN alt_baro < 10000 THEN 'below 10k ft' WHEN alt_baro < 20000 THEN '10k-20k ft' " +
	"WHEN alt_baro < 30000 THEN '20k-30k ft' ELSE '30k ft and up' END"

// Local days and hours use the TZ the stack runs with: migrate writes it into db_meta.
const (
	localTZ    = "(SELECT value FROM db_meta WHERE key = 'tz')"
	airlineSQL = "substring(f.callsign FROM '^([A-Z]{3})[0-9]')"
)

// kt is the implied speed (kt) between consecutive positions a and b (column prefixes).
func kt(a, b string) string {
	return strings.NewReplacer("{a}", a, "{b}", b).Replace(
		"2 * 3440.065 * asin(sqrt(sin(radians({b}lat - {a}lat) / 2) ^ 2 + cos(radians({a}lat)) " +
			"* cos(radians({b}lat)) * sin(radians({b}lon - {a}lon) / 2) ^ 2)) " +
			"/ greatest(extract(epoch FROM {b}time - {a}time) / 3600, 1.0 / 3600)")
}

// Outline point columns, and a query tail that closes the loop by repeating
// the first bearing at 360 (the Route layer draws points in order).
const (
	outlineCols = `deg, lat, lon, distance_nm AS distance, alt_baro AS altitude, hex, time AS "heard at"`
	closeSQL    = "UNION ALL SELECT * FROM (SELECT deg + 360, %s FROM %s ORDER BY deg LIMIT 1) closing ORDER BY 1"
)

// hourColumns is one count column per local hour, "00" to "23".
func hourColumns() string {
	cols := make([]string, 24)
	for h := range cols {
		cols[h] = fmt.Sprintf(`  count(*) FILTER (WHERE extract(hour FROM t) = %d) AS "%02d"`, h, h)
	}
	return strings.Join(cols, ",\n")
}

var panelSQL = map[string]string{
	// Receiver health: receiver_stats, one row per minute.
	"rx_signal": `SELECT time, signal AS average, peak_signal AS peak, noise FROM receiver_stats
WHERE $__timeFilter(time) ORDER BY 1`,
	"rx_messages": `SELECT time, messages_valid / nullif(window_seconds, 0) AS valid FROM receiver_stats
WHERE $__timeFilter(time) ORDER BY 1`,
	"rx_strong": "SELECT time, strong_signals AS strong FROM receiver_stats WHERE $__timeFilter(time) ORDER BY 1",
	"rx_aircraft": `SELECT time, aircraft_with_pos AS "with position", aircraft_without_pos AS "without position"
FROM receiver_stats WHERE $__timeFilter(time) ORDER BY 1`,
	"rx_range": `SELECT time, max_distance_m / 1852 AS "max range" FROM receiver_stats
WHERE $__timeFilter(time) ORDER BY 1`,
	"rx_gain":         "SELECT gain_db FROM receiver_stats ORDER BY time DESC LIMIT 1",
	"rx_ppm":          "SELECT ppm FROM receiver_stats ORDER BY time DESC LIMIT 1",
	"rx_age":          "SELECT extract(epoch FROM now() - max(time))::float8 AS age FROM receiver_stats",
	"last_write":      "SELECT extract(epoch FROM now() - max(time))::float8 AS age FROM positions WHERE time > now() - interval '1 hour'",
	"now":             "SELECT count(DISTINCT hex) FROM positions WHERE time > now() - interval '1 minute'",
	"aircraft_range":  "SELECT count(DISTINCT hex) FROM positions WHERE $__timeFilter(time)",
	"positions_range": "SELECT count(*) FROM positions WHERE $__timeFilter(time)",
	"furthest":        "SELECT max(distance_nm) FROM positions WHERE $__timeFilter(time)",
	"tracked": `SELECT $__timeGroupAlias(time, $__interval, NULL), count(DISTINCT hex) AS aircraft
FROM positions WHERE $__timeFilter(time)
GROUP BY 1 ORDER BY 1`,
	"altitude": `SELECT $__timeGroupAlias(time, $__interval, NULL),
  count(DISTINCT hex) FILTER (WHERE on_ground) AS "ground",
  count(DISTINCT hex) FILTER (WHERE alt_baro < 10000) AS "below 10k ft",
  count(DISTINCT hex) FILTER (WHERE alt_baro >= 10000 AND alt_baro < 30000) AS "10k-30k ft",
  count(DISTINCT hex) FILTER (WHERE alt_baro >= 30000) AS "30k ft and up"
FROM positions WHERE $__timeFilter(time)
GROUP BY 1 ORDER BY 1`,
	// 0.05 degree cells (~3 nm): bounded row count at any time range. Plain
	// double precision throughout, so no NUMERIC columns reach Grafana.
	"coverage": `SELECT round(lat * 20) / 20 AS lat, round(lon * 20) / 20 AS lon,
  count(*) AS positions
FROM positions WHERE $__timeFilter(time)
GROUP BY 1, 2`,
	// % 36: a bearing of exactly 360.0 would otherwise get its own "360" bar.
	"bearing": `SELECT lpad((floor(bearing / 10)::int % 36 * 10)::text, 3, '0') AS bearing,
  max(distance_nm) AS "max range"
FROM positions WHERE $__timeFilter(time) AND bearing IS NOT NULL
GROUP BY 1 ORDER BY 1`,
	"lowest": `SELECT * FROM (
  SELECT DISTINCT ON (p.hex) p.time, p.flight, a.registration, a.type_code AS type,
    a.operator, p.alt_baro AS altitude, p.distance_nm AS distance
  FROM positions p LEFT JOIN aircraft a USING (hex)
  WHERE $__timeFilter(p.time) AND NOT p.on_ground AND p.alt_baro IS NOT NULL
  ORDER BY p.hex, p.alt_baro
) lowest ORDER BY altitude LIMIT 15`,
	"operators": `SELECT a.operator, count(DISTINCT p.hex) AS aircraft
FROM positions p JOIN aircraft a USING (hex)
WHERE $__timeFilter(p.time) AND a.operator IS NOT NULL
GROUP BY 1 ORDER BY 2 DESC LIMIT 15`,
	"types": `SELECT a.type_code AS type, max(a.description) AS description, count(DISTINCT p.hex) AS aircraft
FROM positions p JOIN aircraft a USING (hex)
WHERE $__timeFilter(p.time) AND a.type_code IS NOT NULL
GROUP BY 1 ORDER BY 3 DESC LIMIT 15`,
	// Weather aloft: long format (time, metric, value); Grafana makes one series
	// per `metric`. Airborne only, banded on barometric altitude.
	"wind_speed": "SELECT $__timeGroupAlias(time, $__interval, NULL), " + bandSQL + ` AS metric,
  percentile_cont(0.5) WITHIN GROUP (ORDER BY wind_speed) AS value
FROM positions WHERE $__timeFilter(time) AND wind_speed IS NOT NULL AND alt_baro IS NOT NULL
GROUP BY 1, 2 ORDER BY 1`,
	"oat": "SELECT $__timeGroupAlias(time, $__interval, NULL), " + bandSQL + ` AS metric,
  percentile_cont(0.5) WITHIN GROUP (ORDER BY oat) AS value
FROM positions WHERE $__timeFilter(time) AND oat IS NOT NULL AND alt_baro IS NOT NULL
GROUP BY 1, 2 ORDER BY 1`,
	// Circular mean: a median or plain average breaks across north (350 and
	// 10 degrees would give ~180). atan2 returns -180..180, shifted to 0..360.
	"wind_dir": `SELECT time, metric, CASE WHEN d < 0 THEN d + 360 ELSE d END AS value FROM (
  SELECT $__timeGroupAlias(time, $__interval, NULL), ` + bandSQL + ` AS metric,
    degrees(atan2(avg(sin(radians(wind_dir))), avg(cos(radians(wind_dir))))) AS d
  FROM positions WHERE $__timeFilter(time) AND wind_dir IS NOT NULL AND alt_baro IS NOT NULL
  GROUP BY 1, 2
) w ORDER BY 1`,
	// db_flags bits: 1 military, 2 interesting.
	"flagged": `SELECT min(p.time) AS first_seen, max(p.time) AS last_seen,
  CASE WHEN a.db_flags & 1 <> 0 THEN 'military' ELSE 'interesting' END AS flag,
  a.registration, a.type_code AS type, a.description, a.operator,
  min(p.alt_baro) AS lowest, min(p.distance_nm) AS closest
FROM positions p JOIN aircraft a USING (hex)
WHERE $__timeFilter(p.time) AND a.db_flags & 3 <> 0
GROUP BY p.hex, a.db_flags, a.registration, a.type_code, a.description, a.operator
ORDER BY 2 DESC`,
	// GPS integrity. Degraded = NIC < 7 (integrity radius 0.2 nm or worse) or
	// NACp < 8 (accuracy 93 m or worse). GPSJam's formula: (bad - 1) / total,
	// so one aircraft with broken avionics doesn't read as interference.
	"gps_degraded": `SELECT $__timeGroupAlias(time, $__interval, NULL),
  greatest(0, count(DISTINCT hex) FILTER (WHERE nic < 7 OR nac_p < 8) - 1)::float8
    / count(DISTINCT hex) AS "degraded"
FROM positions WHERE $__timeFilter(time) AND nic IS NOT NULL AND nac_p IS NOT NULL
GROUP BY 1 ORDER BY 1`,
	"gps_aircraft": `SELECT min(p.time) AS first_seen, max(p.time) AS last_seen, a.registration, a.type_code AS type,
  a.operator, min(p.nic) AS worst_nic, min(p.nac_p) AS worst_nac_p, count(*) AS degraded_positions
FROM positions p LEFT JOIN aircraft a USING (hex)
WHERE $__timeFilter(p.time) AND (p.nic < 7 OR p.nac_p < 8)
GROUP BY p.hex, a.registration, a.type_code, a.operator
ORDER BY 2 DESC LIMIT 50`,
	// Long-term: daily_stats, one row per local day, filled by the nightly
	// heatmap import (backfill.py). Tiny, so any range is fast.
	"daily_aircraft": "SELECT day::timestamptz AS time, aircraft FROM daily_stats WHERE $__timeFilter(day) ORDER BY 1",
	"daily_range":    `SELECT day::timestamptz AS time, max_range_nm AS "max range" FROM daily_stats WHERE $__timeFilter(day) ORDER BY 1`,
	"daily_busiest": `SELECT day::timestamptz AS time, busiest_hour AS "busiest hour" FROM daily_stats
WHERE $__timeFilter(day) ORDER BY 1`,
	// Flights: the `flights` materialized view, rebuilt nightly from history.
	"flight_log": `SELECT f.first_seen AS "first seen", f.callsign, al.name AS airline, d.registration,
  d.type_code AS type, d.description,
  extract(epoch FROM f.last_seen - f.first_seen)::float8 AS duration,
  f.min_alt AS lowest, f.closest_nm AS closest,
  round(f.entry_bearing)::int AS "entered at", round(f.exit_bearing)::int AS "left at"
FROM flights f
LEFT JOIN aircraft_db d USING (hex)
LEFT JOIN airlines al ON al.code = ` + airlineSQL + `
WHERE $__timeFilter(f.first_seen)
ORDER BY f.first_seen DESC LIMIT 500`,
	"flights_per_day": "SELECT time_bucket('1 day', first_seen, " + localTZ + `) AS time, count(*) AS flights
FROM flights WHERE $__timeFilter(first_seen) GROUP BY 1 ORDER BY 1`,
	// One row per local date, one column per local hour.
	"day_hour_grid": "WITH f AS (SELECT first_seen AT TIME ZONE " + localTZ + ` AS t FROM flights WHERE $__timeFilter(first_seen))
SELECT to_char(t::date, 'Dy DD Mon') AS day,
` + hourColumns() + `
FROM f GROUP BY t::date ORDER BY t::date`,
	"regulars": `SELECT d.registration, d.type_code AS type, d.description, count(*) AS flights,
  mode() WITHIN GROUP (ORDER BY f.callsign) AS "usual callsign", max(f.last_seen) AS "last seen"
FROM flights f LEFT JOIN aircraft_db d USING (hex)
WHERE $__timeFilter(f.first_seen)
GROUP BY f.hex, d.registration, d.type_code, d.description ORDER BY 4 DESC LIMIT 25`,
	// First flight ever per aircraft, counted on the day it happened. The first
	// weeks of history  count every aircraft as new.
	"new_airframes": "SELECT time_bucket('1 day', first_ever, " + localTZ + `) AS time, count(*) AS "new aircraft"
FROM (SELECT hex, min(first_seen) AS first_ever FROM flights GROUP BY hex) a
WHERE $__timeFilter(first_ever) GROUP BY 1 ORDER BY 1`,
	// Types seen on at most 3 days in all history, when one was in the range.
	"rare": `WITH type_days AS (
  SELECT d.type_code, count(DISTINCT f.first_seen::date) AS days
  FROM flights f JOIN aircraft_db d USING (hex) WHERE d.type_code IS NOT NULL GROUP BY 1
)
SELECT f.first_seen AS "first seen", f.callsign, d.registration, d.type_code AS type, d.description,
  t.days AS "days ever seen", f.closest_nm AS closest
FROM flights f JOIN aircraft_db d USING (hex) JOIN type_days t USING (type_code)
WHERE $__timeFilter(f.first_seen) AND t.days <= 3
ORDER BY f.first_seen DESC LIMIT 50`,
	// Reception range. All-time: history_outline, kept by the nightly job.
	"outline_all": "SELECT " + outlineCols + " FROM history_outline\n" +
		fmt.Sprintf(closeSQL, "lat, lon, distance_nm, alt_baro, hex, time", "history_outline"),
	// Selected range, from the 10 s positions, with the same corroboration as
	// history_outline: a neighbouring position within 10 min at under 800 kt.
	"outline_range": `WITH p AS (
  SELECT hex, time, lat, lon, alt_baro, distance_nm, bearing,
    lag(time) OVER w AS ptime, lag(lat) OVER w AS plat, lag(lon) OVER w AS plon,
    lead(time) OVER w AS ntime, lead(lat) OVER w AS nlat, lead(lon) OVER w AS nlon
  FROM positions WHERE $__timeFilter(time) AND distance_nm IS NOT NULL AND bearing IS NOT NULL
  WINDOW w AS (PARTITION BY hex ORDER BY time)
), best AS (
  SELECT DISTINCT ON (deg) deg, lat, lon, distance_nm, alt_baro, hex, time
  FROM (SELECT floor(bearing)::int % 360 AS deg, * FROM p
        WHERE (time - ptime <= interval '10 minutes' AND ` + kt("p", "") + ` < 800)
           OR (ntime - time <= interval '10 minutes' AND ` + kt("", "n") + ` < 800)) ok
  ORDER BY deg, distance_nm DESC
)
SELECT ` + outlineCols + " FROM best\n" +
		fmt.Sprintf(closeSQL, "lat, lon, distance_nm, alt_baro, hex, time", "best"),
	"furthest_ever": `SELECT o.time AS "heard at", o.distance_nm AS distance, o.deg AS bearing, o.alt_baro AS altitude,
  d.registration, d.type_code AS type, d.description
FROM history_outline o LEFT JOIN aircraft_db d USING (hex)
ORDER BY o.distance_nm DESC LIMIT 10`,
	// 7500 hijack, 7600 radio failure, 7700 general emergency.
	"emergencies": `SELECT min(p.time) AS first_seen, max(p.time) AS last_seen, p.squawk, p.flight,
  a.registration, a.type_code AS type, a.operator
FROM positions p LEFT JOIN aircraft a USING (hex)
WHERE $__timeFilter(p.time) AND p.squawk IN ('7500', '7600', '7700')
GROUP BY p.hex, p.squawk, p.flight, a.registration, a.type_code, a.operator
ORDER BY 1 DESC`,
}

func build() *obj {
	var panels []any
	y := 0
	panels = append(panels, row("Now", y))
	y++
	panels = append(panels,
		stat(
			"Last write",
			"Age of the newest stored position. Over a minute means the ingester, the feed "+
				"or the receiver is down (docker compose logs ingest).",
			panelSQL["last_write"],
			gridPos(0, y, 4, 4),
			statOpts{
				unit:     "s",
				decimals: 0,
				steps:    []any{o("value", nil, "color", "green"), o("value", 60, "color", "red")},
				noValue:  "Nothing in 1h",
			},
		),
		stat(
			"Aircraft now",
			"Distinct aircraft with a position in the last minute.",
			panelSQL["now"],
			gridPos(4, y, 5, 4),
			statOpts{},
		),
		stat(
			"Aircraft in range",
			"Distinct aircraft seen in the selected time range.",
			panelSQL["aircraft_range"],
			gridPos(9, y, 5, 4),
			statOpts{},
		),
		stat(
			"Positions in range",
			"Stored position fixes in the selected time range.",
			panelSQL["positions_range"],
			gridPos(14, y, 5, 4),
			statOpts{unit: "short"},
		),
		stat(
			"Furthest reception",
			"Longest distance from the receiver in the selected range, nautical miles.",
			panelSQL["furthest"],
			gridPos(19, y, 5, 4),
			statOpts{unit: "suffix: nm", decimals: 1},
		),
	)
	y += 4
	// One receiver_stats row per minute from readsb's stats.json.
	panels = append(panels, row("Receiver", y))
	y++
	panels = append(panels,
		timeseries(
			"Signal and noise",
			"Average and peak signal of decoded messages, and the noise floor, in dBFS "+
				"(0 is the dongle's maximum). The gap between average signal and noise is the margin; a rising "+
				"noise floor means interference nearby. Empty on a readsb without its own SDR.",
			panelSQL["rx_signal"],
			gridPos(0, y, 8, 8),
			tsOpts{insertNulls: rxGap, unit: "suffix: dBFS", legend: true, noMinimum: true},
		),
		timeseries(
			"Messages per second",
			"Valid Mode S / ADS-B messages decoded per second.",
			panelSQL["rx_messages"],
			gridPos(8, y, 8, 8),
			tsOpts{insertNulls: rxGap, unit: "short"},
		),
		timeseries(
			"Strong signals",
			"Messages per minute above -3 dBFS. A lot of them means the gain is too high: "+
				"nearby aircraft overload the dongle and weak, distant ones get lost.",
			panelSQL["rx_strong"],
			gridPos(16, y, 8, 8),
			tsOpts{insertNulls: rxGap, unit: "short"},
		),
	)
	y += 8
	panels = append(panels,
		timeseries(
			"Aircraft heard",
			"Aircraft seen in the last minute, with and without a decoded position.",
			panelSQL["rx_aircraft"],
			gridPos(0, y, 8, 8),
			tsOpts{insertNulls: rxGap, unit: "short", stack: true},
		),
		timeseries(
			"Max range",
			"Furthest position decoded in each minute.",
			panelSQL["rx_range"],
			gridPos(8, y, 8, 8),
			tsOpts{insertNulls: rxGap, unit: "suffix: nm"},
		),
	)
	for i, s := range []struct {
		title, desc, key, unit string
		dec                    int
		steps                  []any
	}{
		{"SDR gain", "Tuner gain readsb is using.", "rx_gain", "suffix: dB", 1, nil},
		{
			"Frequency error",
			"Dongle crystal error estimated from decoded messages. Steady is fine; drifting " +
				"with temperature is normal for cheap dongles.",
			"rx_ppm",
			"suffix: ppm",
			1,
			nil,
		},
		{
			"Receiver stats age",
			"Time since the newest receiver stats row. Over a few minutes means readsb " +
				"or its web server is down (docker compose logs ingest).",
			"rx_age",
			"s",
			0,
			[]any{o("value", nil, "color", "green"), o("value", 180, "color", "red")},
		},
	} {
		w := 8
		if i < 2 {
			w = 4
		}
		panels = append(panels,
			stat(
				s.title,
				s.desc,
				panelSQL[s.key],
				gridPos(16+(i%2)*4, y+(i/2)*4, w, 4),
				statOpts{unit: s.unit, decimals: s.dec, steps: s.steps, noValue: "No data"},
			),
		)
	}
	y += 8
	panels = append(panels, row("Traffic", y))
	y++
	panels = append(panels,
		timeseries(
			"Aircraft tracked",
			"Distinct aircraft per interval.",
			panelSQL["tracked"],
			gridPos(0, y, 12, 8),
			tsOpts{},
		),
		timeseries(
			"Aircraft by altitude",
			"Distinct aircraft per interval by barometric "+
				"altitude band. One aircraft can appear in two bands while climbing.",
			panelSQL["altitude"],
			gridPos(12, y, 12, 8),
			tsOpts{stack: true},
		),
	)
	y += 8
	panels = append(panels, row("Coverage", y))
	y++
	panels = append(panels,
		o(
			"id", nid(),
			"type", "geomap",
			"title", "Where positions were received",
			"description", "Position count per 0.05 degree cell (about 3 nm), so it doubles as a reception "+
				"footprint. Zoom out if planes ever show up past the frame.",
			"datasource", ds,
			"targets", []any{target(panelSQL["coverage"], "table"), withRefID(target(panelSQL["outline_all"], "table"), "B")},
			"fieldConfig", o("defaults", o(), "overrides", []any{}),
			"options", o(
				// A heatmap layer gives "fit" no extent, so the outline points ride along as a small
				// markers layer. All layers, not one named layer: Grafana 13 never fits to a single
				// layer, and even all layers only fits once re-selected after load (the map opens
				// on the whole world); pin coordinates in your own copy if that bothers you.
				// maxZoom stops a sparse outline zooming in to street level.
				"view", o("id", "fit", "allLayers", true, "padding", 5, "maxZoom", 9),
				"controls", o("showZoom", true, "mouseWheelZoom", true, "showAttribution", true),
				"basemap", o("type", "default", "name", "Basemap"),
				"layers", []any{
					o(
						"type", "heatmap",
						"name", "Positions",
						"tooltip", true,
						"location", o("mode", "coords", "latitude", "lat", "longitude", "lon"),
						"filterData", o("id", "byRefId", "options", "A"),
						"config", o(
							"blur", 12,
							"radius", 6,
							"weight", o("field", "positions", "fixed", 1, "min", 0, "max", 1),
						),
					),
					o(
						"type", "markers",
						"name", "Range outline",
						"tooltip", false,
						"location", o("mode", "coords", "latitude", "lat", "longitude", "lon"),
						"filterData", o("id", "byRefId", "options", "B"),
						"config", o(
							"showLegend", false,
							"style", o("size", o("fixed", 2), "color", o("fixed", "#5794F2"), "opacity", 0.5),
						),
					),
				},
				"tooltip", o("mode", "details"),
			),
			"gridPos", gridPos(0, y, 14, 14),
		),
		o(
			"id", nid(),
			"type", "barchart",
			"title", "Range by bearing",
			"description", "Furthest reception per 10 degree sector (true bearing from the receiver). "+
				"Short bars point at terrain, buildings or antenna nulls.",
			"datasource", ds,
			"targets", []any{target(panelSQL["bearing"], "table")},
			"fieldConfig", o(
				"defaults", o(
					"unit", "suffix: nm",
					"min", 0,
					"color", o("mode", "fixed", "fixedColor", "blue"),
					"custom", o("fillOpacity", 70, "lineWidth", 0),
				),
				"overrides", []any{},
			),
			"options", o(
				"xField", "bearing",
				"orientation", "vertical",
				"showValue", "never",
				"barWidth", 0.9,
				"xTickLabelRotation", -45,
				"xTickLabelSpacing", 0,
				"legend", o("showLegend", false, "displayMode", "list", "placement", "bottom"),
				"tooltip", o("mode", "single", "sort", "none"),
			),
			"gridPos", gridPos(14, y, 10, 14),
		),
	)
	y += 14
	ra, rb := withRefID(target(panelSQL["outline_all"], "table"), "A"), withRefID(target(panelSQL["outline_range"], "table"), "B")
	loc := o("mode", "coords", "latitude", "lat", "longitude", "lon")

	dot := func(color string) *obj {
		return o(
			"size", o("fixed", 3),
			"color", o("fixed", color),
			"opacity", 0.8,
			"symbol", o("fixed", "img/icons/marker/circle.svg"),
		)
	}

	panels = append(panels,
		o(
			"id", nid(),
			"type", "geomap",
			"title", "Reception range",
			"description", "Furthest position heard in each degree of bearing. Blue: all time ("+
				"updated nightly). Green: the selected range (last 90 days at most). Positions only "+
				"count if the same aircraft's neighbouring position is within 10 minutes at under "+
				"800 kt, so bad decodes don't draw spikes.",
			"datasource", ds,
			"targets", []any{ra, rb},
			"fieldConfig", o("defaults", o(), "overrides", []any{}),
			"options", o(
				// All layers, not one named layer: Grafana 13 never fits to a single layer, and even
				// all layers only fits once re-selected after load (the map opens on the whole
				// world); pin coordinates in your own copy if that bothers you. The markers layers
				// give the extent; maxZoom stops a sparse outline zooming in to street level.
				"view", o("id", "fit", "allLayers", true, "padding", 5, "maxZoom", 9),
				"controls", o("showZoom", true, "mouseWheelZoom", true, "showAttribution", true),
				"basemap", o("type", "default", "name", "Basemap"),
				"layers", []any{
					o(
						"type", "route",
						"name", "All time",
						"tooltip", true,
						"location", loc,
						"filterData", o("id", "byRefId", "options", "A"),
						"config", o(
							"arrow", 0,
							"style", o("color", o("fixed", "#5794F2"), "lineWidth", 2, "opacity", 0.9),
						),
					),
					o(
						"type", "route",
						"name", "Selected range",
						"tooltip", true,
						"location", loc,
						"filterData", o("id", "byRefId", "options", "B"),
						"config", o(
							"arrow", 0,
							"style", o("color", o("fixed", "#73BF69"), "lineWidth", 2, "opacity", 0.9),
						),
					),
					// Dots under both lines: tooltips, and still visible if the beta Route layer misbehaves.
					o(
						"type", "markers",
						"name", "All-time points",
						"tooltip", true,
						"location", loc,
						"filterData", o("id", "byRefId", "options", "A"),
						"config", o("showLegend", false, "style", dot("#5794F2")),
					),
					o(
						"type", "markers",
						"name", "Selected-range points",
						"tooltip", true,
						"location", loc,
						"filterData", o("id", "byRefId", "options", "B"),
						"config", o("showLegend", false, "style", dot("#73BF69")),
					),
				},
				"tooltip", o("mode", "details"),
			),
			"gridPos", gridPos(0, y, 14, 14),
		),
		table(
			"Furthest ever",
			"The ten furthest bearings in the all-time outline, with what was heard there.",
			panelSQL["furthest_ever"],
			gridPos(14, y, 10, 14),
			unitOverride("distance", "suffix: nm", 1),
			unitOverride("bearing", "degree", nil),
			unitOverride("altitude", "suffix: ft", nil),
		),
	)
	y += 14
	// Weather lines always draw points: reports are sparse, and a lone value
	// between empty slots has no neighbour to draw a line to.
	panels = append(panels, row("Weather aloft", y))
	y++
	panels = append(panels,
		timeseries(
			"Wind speed",
			"Median wind speed reported by aircraft, per altitude band. Aircraft derive it "+
				"from airspeed, heading and ground track, so it is only as good as their instruments.",
			panelSQL["wind_speed"],
			gridPos(0, y, 8, 9),
			tsOpts{unit: "suffix: kt", interval: "5m", legend: true, points: true},
		),
		timeseries(
			"Outside air temperature",
			"Median static air temperature reported by aircraft, per altitude band.",
			panelSQL["oat"],
			gridPos(8, y, 8, 9),
			tsOpts{unit: "celsius", interval: "5m", legend: true, noMinimum: true, points: true},
		),
		timeseries(
			"Wind direction",
			"Direction the wind blows FROM, circular mean per altitude band (0/360 north, "+
				"270 west). A steady westerly at 30k ft and up is the jet stream.",
			panelSQL["wind_dir"],
			gridPos(16, y, 8, 9),
			tsOpts{unit: "degree", interval: "5m", draw: "points", legend: true, maximum: 360},
		),
	)
	y += 9
	panels = append(panels, row("GPS integrity", y))
	y++
	panels = append(panels,
		timeseries(
			"Aircraft with degraded GPS",
			"Share of aircraft reporting NIC < 7 or NACp < 8, per interval, with "+
				"GPSJam's (bad - 1) / total so one faulty aircraft doesn't count. GPSJam colours: under 2% normal, "+
				"2-10% some interference, over 10% likely jamming.",
			panelSQL["gps_degraded"],
			gridPos(0, y, 10, 8),
			tsOpts{
				unit:     "percentunit",
				interval: "30m",
				points:   true,
				thresholds: []any{
					o("value", nil, "color", "green"),
					o("value", 0.02, "color", "yellow"),
					o("value", 0.10, "color", "red"),
				},
			},
		),
		table(
			"Aircraft that reported degraded GPS",
			"Many aircraft at once points at interference; the same "+
				"aircraft every time points at its own avionics. Lower is worse for both codes.",
			panelSQL["gps_aircraft"],
			gridPos(10, y, 14, 8),
		),
	)
	y += 8
	panels = append(panels, row("Notable", y))
	y++
	panels = append(panels,
		table(
			"Lowest airborne aircraft",
			"Each aircraft's lowest barometric altitude in the range, "+
				"lowest first. Distance is from the receiver.",
			panelSQL["lowest"],
			gridPos(0, y, 24, 9),
			unitOverride("altitude", "suffix: ft", nil), unitOverride("distance", "suffix: nm", 1),
		),
	)
	y += 9
	panels = append(panels,
		table(
			"Top operators",
			"Distinct aircraft per registered operator (tar1090 database).",
			panelSQL["operators"],
			gridPos(0, y, 12, 10),
		),
		table(
			"Top aircraft types",
			"Distinct aircraft per ICAO type code.",
			panelSQL["types"],
			gridPos(12, y, 12, 10),
		),
	)
	y += 10
	panels = append(panels,
		table(
			"Emergency squawks",
			"7500 hijack, 7600 radio failure, 7700 emergency. Empty is normal.",
			panelSQL["emergencies"],
			gridPos(0, y, 24, 6),
		),
	)
	y += 6
	panels = append(panels,
		table(
			"Military and interesting aircraft",
			"Aircraft tar1090's database flags as military "+
				"or interesting, with their lowest altitude and closest approach in the range.",
			panelSQL["flagged"],
			gridPos(0, y, 24, 8),
			unitOverride("lowest", "suffix: ft", nil), unitOverride("closest", "suffix: nm", 1),
		),
	)

	y += 8
	panels = append(panels, row("Long-term", y))
	y++
	note := " From the receiver's own replay history, 1 sample per aircraft per minute, local days."
	panels = append(panels,
		timeseries(
			"Aircraft per day",
			"Distinct aircraft each day."+note,
			panelSQL["daily_aircraft"],
			gridPos(0, y, 8, 8),
			tsOpts{interval: "1d", points: true, timeFrom: "2y"},
		),
		timeseries(
			"Furthest reception per day",
			"Longest range each day; a step change usually means the antenna, cable or gain changed."+note,
			panelSQL["daily_range"],
			gridPos(8, y, 8, 8),
			tsOpts{unit: "suffix: nm", interval: "1d", points: true, timeFrom: "2y"},
		),
		timeseries(
			"Busiest hour",
			"Local hour with the most distinct aircraft each day."+note,
			panelSQL["daily_busiest"],
			gridPos(16, y, 8, 8),
			tsOpts{interval: "1d", draw: "points", maximum: 23, timeFrom: "2y"},
		),
	)
	y += 8
	panels = append(panels, row("Flights (from history, updated nightly)", y))
	y++
	fnote := " Flights split where an aircraft went unheard for over 30 minutes; rebuilt nightly, so today shows tomorrow."
	panels = append(panels,
		table(
			"Flight log",
			"Every flight that started in the range, newest first (500 max). "+
				"Entered/left at: bearing from the receiver where it was first and last heard."+fnote,
			panelSQL["flight_log"],
			gridPos(0, y, 24, 12),
			unitOverride("duration", "s", nil),
			unitOverride("lowest", "suffix: ft", nil),
			unitOverride("closest", "suffix: nm", 1),
			unitOverride("entered at", "degree", nil),
			unitOverride("left at", "degree", nil),
		),
	)
	y += 12
	panels = append(panels,
		timeseries(
			"Flights per day",
			"Flights starting each local day."+fnote,
			panelSQL["flights_per_day"],
			gridPos(0, y, 12, 8),
			tsOpts{interval: "1d", points: true, timeFrom: "2y"},
		),
		timeseries(
			"New aircraft per day",
			"Aircraft seen for the first time ever that day. The first "+
				"weeks of history count everything as new."+fnote,
			panelSQL["new_airframes"],
			gridPos(12, y, 12, 8),
			tsOpts{interval: "1d", points: true, timeFrom: "2y"},
		),
	)
	y += 8
	grid := table(
		"Flights per hour, last 7 days",
		"Flights starting in each local hour, one row per day, oldest on top; darker is busier. Move the "+
			"dashboard time range back to page through earlier weeks."+fnote,
		panelSQL["day_hour_grid"],
		gridPos(0, y, 24, 10),
	)
	grid.set("timeFrom", "7d")
	// Grafana's table defaults to about 150 px per column, so only a few hours fit and the rest
	// scroll sideways; narrow fixed columns fit all 24. 7 days usually spans 8 dates: h 10 fits
	// them without an inner scroll bar.
	gridFC := grid.sub("fieldConfig")
	gridFC.sub("defaults").set("custom", o(
		"align", "center",
		"width", 44,
		"minWidth", 36,
		"cellOptions", o("type", "color-background", "mode", "gradient"),
	))
	gridFC.sub("defaults").set("color", o("mode", "continuous-BlPu"))
	gridFC.set("overrides", []any{
		o(
			"matcher", o("id", "byName", "options", "day"),
			"properties", []any{
				o("id", "custom.cellOptions", "value", o("type", "auto")),
				o("id", "custom.width", "value", 96),
			},
		),
	})
	panels = append(panels, grid)
	y += 10
	panels = append(panels,
		table(
			"Regulars",
			"Aircraft with the most flights in the range."+fnote,
			panelSQL["regulars"],
			gridPos(0, y, 12, 10),
		),
		table(
			"Rare visitors",
			"Flights in the range by aircraft types seen on 3 days or fewer in all history."+fnote,
			panelSQL["rare"],
			gridPos(12, y, 12, 10),
			unitOverride("closest", "suffix: nm", 1),
		),
	)
	return o(
		"uid", "skyledger-all",
		"title", "Skyledger",
		"description", "",
		"tags", []any{"skyledger"},
		"schemaVersion", 41,
		"editable", true,
		"graphTooltip", 0,
		"time", o("from", "now-24h", "to", "now"),
		"refresh", "1m",
		"timezone", "",
		"templating", o("list", []any{}),
		"annotations", o("list", []any{}),
		"links", []any{},
		"panels", panels,
	)
}

// --- Split into three linked dashboards ------------------------------------
// Never change a uid once released: Grafana keeps the old dashboard as a
// provisioned duplicate that can't be deleted from the UI.

type section struct {
	title  string
	titles []string
}

var layout = []struct {
	uid, title, desc string
	sections         []section
}{
	{
		"skyledger-overview",
		"Skyledger Overview",
		"What's overhead now, how far the receiver reaches, notable aircraft.",
		[]section{
			{
				"Now",
				[]string{
					"Last write",
					"Aircraft now",
					"Aircraft in range",
					"Positions in range",
					"Furthest reception",
				},
			},
			{"Reception range", []string{"Reception range", "Furthest ever"}},
			{"Traffic", []string{"Aircraft tracked", "Aircraft by altitude"}},
			{
				"Notable",
				[]string{"Lowest airborne aircraft", "Military and interesting aircraft", "Emergency squawks"},
			},
		},
	},
	{
		"skyledger-receiver",
		"Skyledger Receiver",
		"How well the receiver is working: signal, decode rate, GPS integrity, coverage.",
		[]section{
			{
				"Receiver",
				[]string{
					"Signal and noise",
					"Messages per second",
					"Strong signals",
					"Aircraft heard",
					"Max range",
					"SDR gain",
					"Frequency error",
					"Receiver stats age",
				},
			},
			{"GPS integrity", []string{"Aircraft with degraded GPS", "Aircraft that reported degraded GPS"}},
			{"Coverage", []string{"Where positions were received", "Range by bearing"}},
		},
	},
	{
		"skyledger-history",
		"Skyledger History",
		"Flights, long-term trends and weather aloft, from the history import.",
		[]section{
			{
				"Flights (from history, updated nightly)",
				[]string{
					"Flight log",
					"Flights per day",
					"New aircraft per day",
					"Flights per hour, last 7 days",
					"Regulars",
					"Rare visitors",
				},
			},
			{"Long-term", []string{"Aircraft per day", "Furthest reception per day", "Busiest hour"}},
			{"Weather aloft", []string{"Wind speed", "Outside air temperature", "Wind direction"}},
			{"Operators and types", []string{"Top operators", "Top aircraft types"}},
		},
	},
}

func split(full *obj) (map[string]*obj, error) {
	byTitle := map[string]*obj{}
	for _, p := range full.get("panels").([]any) {
		if p := p.(*obj); p.get("type") != "row" {
			byTitle[p.get("title").(string)] = p
		}
	}
	var placed []string
	for _, d := range layout {
		for _, s := range d.sections {
			placed = append(placed, s.titles...)
		}
	}
	built := slices.Sorted(maps.Keys(byTitle))
	slices.Sort(placed)
	if !slices.Equal(placed, built) {
		return nil, fmt.Errorf("dashboards: layout places %q, but the built panels are %q", placed, built)
	}
	gp := func(p *obj, k string) int { return p.sub("gridPos").num(k) }
	out := map[string]*obj{}
	for _, d := range layout {
		panels, y, pid := []any{}, 0, 1
		for _, s := range d.sections {
			r := row(s.title, y)
			r.set("id", pid)
			panels = append(panels, r)
			pid++
			y++
			members := make([]*obj, 0, len(s.titles))
			for _, t := range s.titles {
				members = append(members, byTitle[t])
			}
			slices.SortStableFunc(members, func(a, b *obj) int {
				return cmp.Or(cmp.Compare(gp(a, "y"), gp(b, "y")), cmp.Compare(gp(a, "x"), gp(b, "x")))
			})
			// Close gaps left by panels that moved to another dashboard: each
			// original y band keeps its internal layout, bands stack tightly.
			bands := map[int][]*obj{}
			for _, p := range members {
				bands[gp(p, "y")] = append(bands[gp(p, "y")], p)
			}
			for _, bandY := range slices.Sorted(maps.Keys(bands)) {
				band := bands[bandY]
				top, bottom := gp(band[0], "y"), 0
				for _, p := range band {
					top = min(top, gp(p, "y"))
					bottom = max(bottom, gp(p, "y")+gp(p, "h"))
				}
				for _, p := range band {
					g := p.sub("gridPos").clone()
					g.set("y", y+g.num("y")-top)
					moved := p.clone()
					moved.set("id", pid)
					moved.set("gridPos", g)
					panels = append(panels, moved)
					pid++
				}
				y += bottom - top
			}
		}
		dash := full.clone()
		dash.set("uid", d.uid)
		dash.set("title", d.title)
		dash.set("description", d.desc)
		dash.set("tags", []any{"skyledger"})
		dash.set("links", []any{
			o(
				"title", "Skyledger",
				"type", "dashboards",
				"tags", []any{"skyledger"},
				"asDropdown", false,
				"includeVars", false,
				"keepTime", true,
				"targetBlank", false,
			),
		})
		dash.set("panels", panels)
		out[d.uid] = dash
	}
	return out, nil
}

// Render returns {file name: JSON text} for every dashboard, byte for byte
// what the Python generator wrote (json.dumps(dash, indent=2) + "\n").
func Render() (map[string][]byte, error) {
	dashes, err := split(build())
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for uid, dash := range dashes {
		text, err := dumps(dash)
		if err != nil {
			return nil, err
		}
		files[uid+".json"] = append(text, '\n')
	}
	return files, nil
}

// AllQueries returns {"<dashboard>/<panel>/<refId>": sql} for every Postgres
// query in every dashboard. It panics if the layout is broken (a programming
// error the package tests catch).
func AllQueries() map[string]string {
	dashes, err := split(build())
	if err != nil {
		panic(err)
	}
	out := map[string]string{}
	for uid, dash := range dashes {
		for _, p := range dash.get("panels").([]any) {
			p := p.(*obj)
			targets, _ := p.get("targets").([]any)
			for _, t := range targets {
				t := t.(*obj)
				if sql, ok := t.get("rawSql").(string); ok {
					out[fmt.Sprintf("%s/%s/%s", uid, p.get("title"), t.get("refId"))] = sql
				}
			}
		}
	}
	return out
}
