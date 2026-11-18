// Package queries holds the SQL behind skyledger's views, shared by the Grafana
// dashboards (internal/dashboards) and the web UI, so the two can't drift apart.
package queries

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Grafana macros: $__timeFilter(col), $__timeGroupAlias(col, $__interval, NULL).
// The NULL fills empty buckets so an outage shows as a gap, not a straight line.
// With timescaledb: true on the datasource, $__timeGroup uses time_bucket().

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

// Panel is every dashboard query by name: Grafana's panels and the web UI run the same SQL.
var Panel = map[string]string{
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

var (
	timeFilter = regexp.MustCompile(`\$__timeFilter\(([\w.]+)\)`)
	timeGroup  = regexp.MustCompile(`\$__timeGroupAlias\(([\w.]+), *\$__interval(?:, *NULL)?\)`)
	anyMacro   = regexp.MustCompile(`\$__\w+`)
)

// Expand turns a Panel query's Grafana macros into bind parameters for callers
// other than Grafana: the time filter becomes from and to, the time group a
// time_bucket of width bucket (e.g. "5 minutes"). Parameters are numbered only
// for the macros present, so args always match what the SQL uses (Postgres
// rejects extra ones). Unlike Grafana, empty buckets are not filled with NULL.
func Expand(sql string, from, to time.Time, bucket string) (string, []any, error) {
	var args []any
	if timeFilter.MatchString(sql) {
		args = append(args, from, to)
		sql = timeFilter.ReplaceAllString(sql, "${1} BETWEEN $$1 AND $$2")
	}
	if timeGroup.MatchString(sql) {
		args = append(args, bucket)
		sql = timeGroup.ReplaceAllString(sql, fmt.Sprintf("time_bucket($$%d::interval, ${1}) AS time", len(args)))
	}
	if m := anyMacro.FindString(sql); m != "" {
		return "", nil, fmt.Errorf("unsupported Grafana macro %s", m)
	}
	return sql, args, nil
}
