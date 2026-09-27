#!/usr/bin/env python3
"""Generate skyledger's Grafana dashboards (classic JSON schema) into charts/skyledger/grafana/dashboards/
(Compose mounts it, the Helm chart packages it).

Usage: uv run tools/gen_dashboards.py [--check | --sql]
  (no args)  write the dashboard files
  --check    exit 1 if the committed files differ from what this script generates (CI)
  --sql      print every panel query as JSON (tests)
"""

import json
import pathlib
import sys

DS = {"type": "grafana-postgresql-datasource", "uid": "skyledger"}
_id = 0


def nid():
    global _id
    _id += 1
    return _id


def target(sql, fmt="table"):
    return {
        "refId": "A",
        "datasource": DS,
        "editorMode": "code",
        "format": fmt,
        "rawQuery": True,
        "rawSql": sql,
    }


def row(title, y):
    return {
        "id": nid(),
        "type": "row",
        "title": title,
        "collapsed": False,
        "panels": [],
        "gridPos": {"x": 0, "y": y, "w": 24, "h": 1},
    }


def stat(title, desc, sql, pos, unit="none", steps=None, decimals=None, no_value=None):
    defaults = {
        "unit": unit,
        "color": {"mode": "thresholds"},
        "thresholds": {"mode": "absolute", "steps": steps or [{"value": None, "color": "text"}]},
    }
    if decimals is not None:
        defaults["decimals"] = decimals
    if no_value:
        defaults["noValue"] = no_value
    return {
        "id": nid(),
        "type": "stat",
        "title": title,
        "description": desc,
        "datasource": DS,
        "targets": [target(sql)],
        "fieldConfig": {"defaults": defaults, "overrides": []},
        "options": {
            "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            "colorMode": "value" if not steps else "background",
            "graphMode": "none",
            "textMode": "value",
        },
        "gridPos": pos,
    }


def timeseries(
    title,
    desc,
    sql,
    pos,
    unit="none",
    stack=False,
    interval="1m",
    draw="line",
    legend=None,
    minimum=0,
    maximum=None,
    points=False,
    thresholds=None,
    time_from=None,
):
    defaults = {
        "unit": unit,
        "custom": {
            "drawStyle": draw,
            "lineWidth": 1,
            "fillOpacity": 20 if stack else (0 if draw == "points" else 10),
            "pointSize": 4,
            "showPoints": "always" if draw == "points" or points else "never",
            "spanNulls": False,
            "stacking": {"mode": "normal" if stack else "none", "group": "A"},
        },
    }
    if minimum is not None:
        defaults["min"] = minimum
    if thresholds:
        defaults["thresholds"] = {"mode": "absolute", "steps": thresholds}
        defaults["custom"]["thresholdsStyle"] = {"mode": "dashed"}
    if maximum is not None:
        defaults["max"] = maximum
    show_legend = stack if legend is None else legend
    extra = {"timeFrom": time_from} if time_from else {}
    return {
        **extra,
        "id": nid(),
        "type": "timeseries",
        "title": title,
        "description": desc,
        "datasource": DS,
        "interval": interval,
        "targets": [target(sql, "time_series")],
        "fieldConfig": {"defaults": defaults, "overrides": []},
        "options": {
            "legend": {"displayMode": "list", "placement": "bottom", "showLegend": show_legend},
            "tooltip": {"mode": "multi" if show_legend else "single", "sort": "desc"},
        },
        "gridPos": pos,
    }


def table(title, desc, sql, pos, overrides=None):
    return {
        "id": nid(),
        "type": "table",
        "title": title,
        "description": desc,
        "datasource": DS,
        "targets": [target(sql)],
        "fieldConfig": {"defaults": {"custom": {"align": "auto"}}, "overrides": overrides or []},
        "options": {"showHeader": True, "cellHeight": "sm"},
        "gridPos": pos,
    }


def unit_override(field, unit, decimals=None):
    props = [{"id": "unit", "value": unit}]
    if decimals is not None:
        props.append({"id": "decimals", "value": decimals})
    return {"matcher": {"id": "byName", "options": field}, "properties": props}


# --- SQL -------------------------------------------------------------------
# Grafana macros: $__timeFilter(col), $__timeGroupAlias(col, $__interval).
# With timescaledb: true on the datasource, $__timeGroup uses time_bucket().

# Altitude band for the weather panels; callers filter alt_baro IS NOT NULL.
BAND = (
    "CASE WHEN alt_baro < 10000 THEN 'below 10k ft' WHEN alt_baro < 20000 THEN '10k-20k ft' "
    "WHEN alt_baro < 30000 THEN '20k-30k ft' ELSE '30k ft and up' END"
)

# Local days and hours use the TZ the stack runs with: migrate writes it into db_meta.
LOCAL = "(SELECT value FROM db_meta WHERE key = 'tz')"
AIRLINE = "substring(f.callsign FROM '^([A-Z]{3})[0-9]')"


def kt(a, b):
    """Implied speed (kt) between consecutive positions a and b (column prefixes)."""
    return (
        f"2 * 3440.065 * asin(sqrt(sin(radians({b}lat - {a}lat) / 2) ^ 2 + cos(radians({a}lat)) "
        f"* cos(radians({b}lat)) * sin(radians({b}lon - {a}lon) / 2) ^ 2)) "
        f"/ greatest(extract(epoch FROM {b}time - {a}time) / 3600, 1.0 / 3600)"
    )


# Outline point columns, and a query tail that closes the loop by repeating
# the first bearing at 360 (the Route layer draws points in order).
OUTLINE_COLS = 'deg, lat, lon, distance_nm AS distance, alt_baro AS altitude, hex, time AS "heard at"'
CLOSE = "UNION ALL SELECT * FROM (SELECT deg + 360, {c} FROM {t} ORDER BY deg LIMIT 1) closing ORDER BY 1"

SQL = {
    # Receiver health: receiver_stats, one row per minute.
    "rx_signal": (
        "SELECT time, signal AS average, peak_signal AS peak, noise FROM receiver_stats\n"
        "WHERE $__timeFilter(time) ORDER BY 1"
    ),
    "rx_messages": (
        "SELECT time, messages_valid / nullif(window_seconds, 0) AS valid FROM receiver_stats\n"
        "WHERE $__timeFilter(time) ORDER BY 1"
    ),
    "rx_strong": "SELECT time, strong_signals AS strong FROM receiver_stats WHERE $__timeFilter(time) ORDER BY 1",
    "rx_aircraft": (
        'SELECT time, aircraft_with_pos AS "with position", aircraft_without_pos AS "without position"\n'
        "FROM receiver_stats WHERE $__timeFilter(time) ORDER BY 1"
    ),
    "rx_range": (
        'SELECT time, max_distance_m / 1852 AS "max range" FROM receiver_stats\n'
        "WHERE $__timeFilter(time) ORDER BY 1"
    ),
    "rx_gain": "SELECT gain_db FROM receiver_stats ORDER BY time DESC LIMIT 1",
    "rx_ppm": "SELECT ppm FROM receiver_stats ORDER BY time DESC LIMIT 1",
    "rx_age": "SELECT extract(epoch FROM now() - max(time))::float8 AS age FROM receiver_stats",
    "last_write": "SELECT extract(epoch FROM now() - max(time))::float8 AS age FROM positions WHERE time > now() - interval '1 hour'",
    "now": "SELECT count(DISTINCT hex) FROM positions WHERE time > now() - interval '1 minute'",
    "aircraft_range": "SELECT count(DISTINCT hex) FROM positions WHERE $__timeFilter(time)",
    "positions_range": "SELECT count(*) FROM positions WHERE $__timeFilter(time)",
    "furthest": "SELECT max(distance_nm) FROM positions WHERE $__timeFilter(time)",
    "tracked": (
        "SELECT $__timeGroupAlias(time, $__interval), count(DISTINCT hex) AS aircraft\n"
        "FROM positions WHERE $__timeFilter(time)\nGROUP BY 1 ORDER BY 1"
    ),
    "altitude": (
        "SELECT $__timeGroupAlias(time, $__interval),\n"
        '  count(DISTINCT hex) FILTER (WHERE on_ground) AS "ground",\n'
        '  count(DISTINCT hex) FILTER (WHERE alt_baro < 10000) AS "below 10k ft",\n'
        '  count(DISTINCT hex) FILTER (WHERE alt_baro >= 10000 AND alt_baro < 30000) AS "10k-30k ft",\n'
        '  count(DISTINCT hex) FILTER (WHERE alt_baro >= 30000) AS "30k ft and up"\n'
        "FROM positions WHERE $__timeFilter(time)\nGROUP BY 1 ORDER BY 1"
    ),
    # 0.05 degree cells (~3 nm): bounded row count at any time range. Plain
    # double precision throughout, so no NUMERIC columns reach Grafana.
    "coverage": (
        "SELECT round(lat * 20) / 20 AS lat, round(lon * 20) / 20 AS lon,\n"
        "  count(*) AS positions\n"
        "FROM positions WHERE $__timeFilter(time)\nGROUP BY 1, 2"
    ),
    # % 36: a bearing of exactly 360.0 would otherwise get its own "360" bar.
    "bearing": (
        "SELECT lpad((floor(bearing / 10)::int % 36 * 10)::text, 3, '0') AS bearing,\n"
        '  max(distance_nm) AS "max range"\n'
        "FROM positions WHERE $__timeFilter(time) AND bearing IS NOT NULL\n"
        "GROUP BY 1 ORDER BY 1"
    ),
    "lowest": (
        "SELECT * FROM (\n"
        "  SELECT DISTINCT ON (p.hex) p.time, p.flight, a.registration, a.type_code AS type,\n"
        "    a.operator, p.alt_baro AS altitude, p.distance_nm AS distance\n"
        "  FROM positions p LEFT JOIN aircraft a USING (hex)\n"
        "  WHERE $__timeFilter(p.time) AND NOT p.on_ground AND p.alt_baro IS NOT NULL\n"
        "  ORDER BY p.hex, p.alt_baro\n"
        ") lowest ORDER BY altitude LIMIT 15"
    ),
    "operators": (
        "SELECT a.operator, count(DISTINCT p.hex) AS aircraft\n"
        "FROM positions p JOIN aircraft a USING (hex)\n"
        "WHERE $__timeFilter(p.time) AND a.operator IS NOT NULL\n"
        "GROUP BY 1 ORDER BY 2 DESC LIMIT 15"
    ),
    "types": (
        "SELECT a.type_code AS type, max(a.description) AS description, count(DISTINCT p.hex) AS aircraft\n"
        "FROM positions p JOIN aircraft a USING (hex)\n"
        "WHERE $__timeFilter(p.time) AND a.type_code IS NOT NULL\n"
        "GROUP BY 1 ORDER BY 3 DESC LIMIT 15"
    ),
    # Weather aloft: long format (time, metric, value); Grafana makes one series
    # per `metric`. Airborne only, banded on barometric altitude.
    "wind_speed": (
        "SELECT $__timeGroupAlias(time, $__interval), " + BAND + " AS metric,\n"
        "  percentile_cont(0.5) WITHIN GROUP (ORDER BY wind_speed) AS value\n"
        "FROM positions WHERE $__timeFilter(time) AND wind_speed IS NOT NULL AND alt_baro IS NOT NULL\n"
        "GROUP BY 1, 2 ORDER BY 1"
    ),
    "oat": (
        "SELECT $__timeGroupAlias(time, $__interval), " + BAND + " AS metric,\n"
        "  percentile_cont(0.5) WITHIN GROUP (ORDER BY oat) AS value\n"
        "FROM positions WHERE $__timeFilter(time) AND oat IS NOT NULL AND alt_baro IS NOT NULL\n"
        "GROUP BY 1, 2 ORDER BY 1"
    ),
    # Circular mean: a median or plain average breaks across north (350 and
    # 10 degrees would give ~180). atan2 returns -180..180, shifted to 0..360.
    "wind_dir": (
        "SELECT time, metric, CASE WHEN d < 0 THEN d + 360 ELSE d END AS value FROM (\n"
        "  SELECT $__timeGroupAlias(time, $__interval), " + BAND + " AS metric,\n"
        "    degrees(atan2(avg(sin(radians(wind_dir))), avg(cos(radians(wind_dir))))) AS d\n"
        "  FROM positions WHERE $__timeFilter(time) AND wind_dir IS NOT NULL AND alt_baro IS NOT NULL\n"
        "  GROUP BY 1, 2\n"
        ") w ORDER BY 1"
    ),
    # db_flags bits: 1 military, 2 interesting.
    "flagged": (
        "SELECT min(p.time) AS first_seen, max(p.time) AS last_seen,\n"
        "  CASE WHEN a.db_flags & 1 <> 0 THEN 'military' ELSE 'interesting' END AS flag,\n"
        "  a.registration, a.type_code AS type, a.description, a.operator,\n"
        "  min(p.alt_baro) AS lowest, min(p.distance_nm) AS closest\n"
        "FROM positions p JOIN aircraft a USING (hex)\n"
        "WHERE $__timeFilter(p.time) AND a.db_flags & 3 <> 0\n"
        "GROUP BY p.hex, a.db_flags, a.registration, a.type_code, a.description, a.operator\n"
        "ORDER BY 2 DESC"
    ),
    # GPS integrity. Degraded = NIC < 7 (integrity radius 0.2 nm or worse) or
    # NACp < 8 (accuracy 93 m or worse). GPSJam's formula: (bad - 1) / total,
    # so one aircraft with broken avionics doesn't read as interference.
    "gps_degraded": (
        "SELECT $__timeGroupAlias(time, $__interval),\n"
        "  greatest(0, count(DISTINCT hex) FILTER (WHERE nic < 7 OR nac_p < 8) - 1)::float8\n"
        '    / count(DISTINCT hex) AS "degraded"\n'
        "FROM positions WHERE $__timeFilter(time) AND nic IS NOT NULL AND nac_p IS NOT NULL\n"
        "GROUP BY 1 ORDER BY 1"
    ),
    "gps_aircraft": (
        "SELECT min(p.time) AS first_seen, max(p.time) AS last_seen, a.registration, a.type_code AS type,\n"
        "  a.operator, min(p.nic) AS worst_nic, min(p.nac_p) AS worst_nac_p, count(*) AS degraded_positions\n"
        "FROM positions p LEFT JOIN aircraft a USING (hex)\n"
        "WHERE $__timeFilter(p.time) AND (p.nic < 7 OR p.nac_p < 8)\n"
        "GROUP BY p.hex, a.registration, a.type_code, a.operator\n"
        "ORDER BY 2 DESC LIMIT 50"
    ),
    # Long-term: daily_stats, one row per local day, filled by the nightly
    # heatmap import (backfill.py). Tiny, so any range is fast.
    "daily_aircraft": "SELECT day::timestamptz AS time, aircraft FROM daily_stats WHERE $__timeFilter(day) ORDER BY 1",
    "daily_range": 'SELECT day::timestamptz AS time, max_range_nm AS "max range" FROM daily_stats WHERE $__timeFilter(day) ORDER BY 1',
    "daily_busiest": (
        'SELECT day::timestamptz AS time, busiest_hour AS "busiest hour" FROM daily_stats\n'
        "WHERE $__timeFilter(day) ORDER BY 1"
    ),
    # Flights: the `flights` materialized view, rebuilt nightly from history.
    "flight_log": (
        'SELECT f.first_seen AS "first seen", f.callsign, al.name AS airline, d.registration,\n'
        "  d.type_code AS type, d.description,\n"
        "  extract(epoch FROM f.last_seen - f.first_seen)::float8 AS duration,\n"
        "  f.min_alt AS lowest, f.closest_nm AS closest,\n"
        '  round(f.entry_bearing)::int AS "entered at", round(f.exit_bearing)::int AS "left at"\n'
        "FROM flights f\n"
        "LEFT JOIN aircraft_db d USING (hex)\n"
        f"LEFT JOIN airlines al ON al.code = {AIRLINE}\n"
        "WHERE $__timeFilter(f.first_seen)\n"
        "ORDER BY f.first_seen DESC LIMIT 500"
    ),
    "flights_per_day": (
        f"SELECT time_bucket('1 day', first_seen, {LOCAL}) AS time, count(*) AS flights\n"
        "FROM flights WHERE $__timeFilter(first_seen) GROUP BY 1 ORDER BY 1"
    ),
    "week_grid": (
        f"SELECT to_char(first_seen AT TIME ZONE {LOCAL}, 'ID Dy') AS day,\n"
        + ",\n".join(
            f'  count(*) FILTER (WHERE extract(hour FROM first_seen AT TIME ZONE {LOCAL}) = {h}) AS "{h:02d}"'
            for h in range(24)
        )
        + "\nFROM flights WHERE $__timeFilter(first_seen) GROUP BY 1 ORDER BY 1"
    ),
    "regulars": (
        "SELECT d.registration, d.type_code AS type, d.description, count(*) AS flights,\n"
        '  mode() WITHIN GROUP (ORDER BY f.callsign) AS "usual callsign", max(f.last_seen) AS "last seen"\n'
        "FROM flights f LEFT JOIN aircraft_db d USING (hex)\n"
        "WHERE $__timeFilter(f.first_seen)\n"
        "GROUP BY f.hex, d.registration, d.type_code, d.description ORDER BY 4 DESC LIMIT 25"
    ),
    # First flight ever per aircraft, counted on the day it happened. The first
    # weeks of history  count every aircraft as new.
    "new_airframes": (
        f"SELECT time_bucket('1 day', first_ever, {LOCAL}) AS time, count(*) AS \"new aircraft\"\n"
        "FROM (SELECT hex, min(first_seen) AS first_ever FROM flights GROUP BY hex) a\n"
        "WHERE $__timeFilter(first_ever) GROUP BY 1 ORDER BY 1"
    ),
    # Types seen on at most 3 days in all history, when one was in the range.
    "rare": (
        "WITH type_days AS (\n"
        "  SELECT d.type_code, count(DISTINCT f.first_seen::date) AS days\n"
        "  FROM flights f JOIN aircraft_db d USING (hex) WHERE d.type_code IS NOT NULL GROUP BY 1\n"
        ")\n"
        'SELECT f.first_seen AS "first seen", f.callsign, d.registration, d.type_code AS type, d.description,\n'
        '  t.days AS "days ever seen", f.closest_nm AS closest\n'
        "FROM flights f JOIN aircraft_db d USING (hex) JOIN type_days t USING (type_code)\n"
        "WHERE $__timeFilter(f.first_seen) AND t.days <= 3\n"
        "ORDER BY f.first_seen DESC LIMIT 50"
    ),
    # Reception range. All-time: history_outline, kept by the nightly job.
    "outline_all": (
        f"SELECT {OUTLINE_COLS} FROM history_outline\n"
        + CLOSE.format(c="lat, lon, distance_nm, alt_baro, hex, time", t="history_outline")
    ),
    # Selected range, from the 10 s positions, with the same corroboration as
    # history_outline: a neighbouring position within 10 min at under 800 kt.
    "outline_range": (
        "WITH p AS (\n"
        "  SELECT hex, time, lat, lon, alt_baro, distance_nm, bearing,\n"
        "    lag(time) OVER w AS ptime, lag(lat) OVER w AS plat, lag(lon) OVER w AS plon,\n"
        "    lead(time) OVER w AS ntime, lead(lat) OVER w AS nlat, lead(lon) OVER w AS nlon\n"
        "  FROM positions WHERE $__timeFilter(time) AND distance_nm IS NOT NULL AND bearing IS NOT NULL\n"
        "  WINDOW w AS (PARTITION BY hex ORDER BY time)\n"
        "), best AS (\n"
        "  SELECT DISTINCT ON (deg) deg, lat, lon, distance_nm, alt_baro, hex, time\n"
        "  FROM (SELECT floor(bearing)::int % 360 AS deg, * FROM p\n"
        f"        WHERE (time - ptime <= interval '10 minutes' AND {kt('p', '')} < 800)\n"
        f"           OR (ntime - time <= interval '10 minutes' AND {kt('', 'n')} < 800)) ok\n"
        "  ORDER BY deg, distance_nm DESC\n"
        ")\n"
        f"SELECT {OUTLINE_COLS} FROM best\n"
        + CLOSE.format(c="lat, lon, distance_nm, alt_baro, hex, time", t="best")
    ),
    "furthest_ever": (
        'SELECT o.time AS "heard at", o.distance_nm AS distance, o.deg AS bearing, o.alt_baro AS altitude,\n'
        "  d.registration, d.type_code AS type, d.description\n"
        "FROM history_outline o LEFT JOIN aircraft_db d USING (hex)\n"
        "ORDER BY o.distance_nm DESC LIMIT 10"
    ),
    # 7500 hijack, 7600 radio failure, 7700 general emergency.
    "emergencies": (
        "SELECT min(p.time) AS first_seen, max(p.time) AS last_seen, p.squawk, p.flight,\n"
        "  a.registration, a.type_code AS type, a.operator\n"
        "FROM positions p LEFT JOIN aircraft a USING (hex)\n"
        "WHERE $__timeFilter(p.time) AND p.squawk IN ('7500', '7600', '7700')\n"
        "GROUP BY p.hex, p.squawk, p.flight, a.registration, a.type_code, a.operator\n"
        "ORDER BY 1 DESC"
    ),
}


def build():
    panels = []
    y = 0
    panels.append(row("Now", y))
    y += 1
    panels.append(
        stat(
            "Last write",
            "Age of the newest stored position. Over a minute means the ingester, the feed "
            "or the receiver is down (docker compose logs ingest).",
            SQL["last_write"],
            {"x": 0, "y": y, "w": 4, "h": 4},
            unit="s",
            decimals=0,
            steps=[{"value": None, "color": "green"}, {"value": 60, "color": "red"}],
            no_value="Nothing in 1h",
        )
    )
    panels.append(
        stat(
            "Aircraft now",
            "Distinct aircraft with a position in the last minute.",
            SQL["now"],
            {"x": 4, "y": y, "w": 5, "h": 4},
        )
    )
    panels.append(
        stat(
            "Aircraft in range",
            "Distinct aircraft seen in the selected time range.",
            SQL["aircraft_range"],
            {"x": 9, "y": y, "w": 5, "h": 4},
        )
    )
    panels.append(
        stat(
            "Positions in range",
            "Stored position fixes in the selected time range.",
            SQL["positions_range"],
            {"x": 14, "y": y, "w": 5, "h": 4},
            unit="short",
        )
    )
    panels.append(
        stat(
            "Furthest reception",
            "Longest distance from the receiver in the selected range, nautical miles.",
            SQL["furthest"],
            {"x": 19, "y": y, "w": 5, "h": 4},
            unit="suffix: nm",
            decimals=1,
        )
    )
    y += 4
    # One receiver_stats row per minute from readsb's stats.json.
    panels.append(row("Receiver", y))
    y += 1
    panels.append(
        timeseries(
            "Signal and noise",
            "Average and peak signal of decoded messages, and the noise floor, in dBFS "
            "(0 is the dongle's maximum). The gap between average signal and noise is the margin; a rising "
            "noise floor means interference nearby. Empty on a readsb without its own SDR.",
            SQL["rx_signal"],
            {"x": 0, "y": y, "w": 8, "h": 8},
            unit="suffix: dBFS",
            legend=True,
            minimum=None,
        )
    )
    panels.append(
        timeseries(
            "Messages per second",
            "Valid Mode S / ADS-B messages decoded per second.",
            SQL["rx_messages"],
            {"x": 8, "y": y, "w": 8, "h": 8},
            unit="short",
        )
    )
    panels.append(
        timeseries(
            "Strong signals",
            "Messages per minute above -3 dBFS. A lot of them means the gain is too high: "
            "nearby aircraft overload the dongle and weak, distant ones get lost.",
            SQL["rx_strong"],
            {"x": 16, "y": y, "w": 8, "h": 8},
            unit="short",
        )
    )
    y += 8
    panels.append(
        timeseries(
            "Aircraft heard",
            "Aircraft seen in the last minute, with and without a decoded position.",
            SQL["rx_aircraft"],
            {"x": 0, "y": y, "w": 8, "h": 8},
            unit="short",
            stack=True,
        )
    )
    panels.append(
        timeseries(
            "Max range",
            "Furthest position decoded in each minute.",
            SQL["rx_range"],
            {"x": 8, "y": y, "w": 8, "h": 8},
            unit="suffix: nm",
        )
    )
    for i, (title, desc, key, unit, dec, steps) in enumerate(
        [
            ("SDR gain", "Tuner gain readsb is using.", "rx_gain", "suffix: dB", 1, None),
            (
                "Frequency error",
                "Dongle crystal error estimated from decoded messages. Steady is fine; drifting "
                "with temperature is normal for cheap dongles.",
                "rx_ppm",
                "suffix: ppm",
                1,
                None,
            ),
            (
                "Receiver stats age",
                "Time since the newest receiver stats row. Over a few minutes means readsb "
                "or its web server is down (docker compose logs ingest).",
                "rx_age",
                "s",
                0,
                [{"value": None, "color": "green"}, {"value": 180, "color": "red"}],
            ),
        ]
    ):
        panels.append(
            stat(
                title,
                desc,
                SQL[key],
                {"x": 16 + (i % 2) * 4, "y": y + (i // 2) * 4, "w": 4 if i < 2 else 8, "h": 4},
                unit=unit,
                decimals=dec,
                steps=steps,
                no_value="No data",
            )
        )
    y += 8
    panels.append(row("Traffic", y))
    y += 1
    panels.append(
        timeseries(
            "Aircraft tracked",
            "Distinct aircraft per interval.",
            SQL["tracked"],
            {"x": 0, "y": y, "w": 12, "h": 8},
        )
    )
    panels.append(
        timeseries(
            "Aircraft by altitude",
            "Distinct aircraft per interval by barometric "
            "altitude band. One aircraft can appear in two bands while climbing.",
            SQL["altitude"],
            {"x": 12, "y": y, "w": 12, "h": 8},
            stack=True,
        )
    )
    y += 8
    panels.append(row("Coverage", y))
    y += 1
    panels.append(
        {
            "id": nid(),
            "type": "geomap",
            "title": "Where positions were received",
            "description": "Position count per 0.05 degree cell (about 3 nm), so it doubles as a reception "
            "footprint. Zoom out if planes ever show up past the frame.",
            "datasource": DS,
            "targets": [target(SQL["coverage"]), dict(target(SQL["outline_all"]), refId="B")],
            "fieldConfig": {"defaults": {}, "overrides": []},
            "options": {
                # "fit" can't take an extent from a heatmap layer (the map falls back to the whole
                # world), so the outline points ride along as a small markers layer to fit to;
                # maxZoom stops a sparse outline zooming in to street level.
                "view": {"id": "fit", "allLayers": False, "layer": "Range outline", "padding": 5, "maxZoom": 9},
                "controls": {"showZoom": True, "mouseWheelZoom": True, "showAttribution": True},
                "basemap": {"type": "default", "name": "Basemap"},
                "layers": [
                    {
                        "type": "heatmap",
                        "name": "Positions",
                        "tooltip": True,
                        "location": {"mode": "coords", "latitude": "lat", "longitude": "lon"},
                        "filterData": {"id": "byRefId", "options": "A"},
                        "config": {
                            "blur": 12,
                            "radius": 6,
                            "weight": {"field": "positions", "fixed": 1, "min": 0, "max": 1},
                        },
                    },
                    {
                        "type": "markers",
                        "name": "Range outline",
                        "tooltip": False,
                        "location": {"mode": "coords", "latitude": "lat", "longitude": "lon"},
                        "filterData": {"id": "byRefId", "options": "B"},
                        "config": {
                            "showLegend": False,
                            "style": {"size": {"fixed": 2}, "color": {"fixed": "#5794F2"}, "opacity": 0.5},
                        },
                    },
                ],
                "tooltip": {"mode": "details"},
            },
            "gridPos": {"x": 0, "y": y, "w": 14, "h": 14},
        }
    )
    panels.append(
        {
            "id": nid(),
            "type": "barchart",
            "title": "Range by bearing",
            "description": "Furthest reception per 10 degree sector (true bearing from the receiver). "
            "Short bars point at terrain, buildings or antenna nulls.",
            "datasource": DS,
            "targets": [target(SQL["bearing"])],
            "fieldConfig": {
                "defaults": {
                    "unit": "suffix: nm",
                    "min": 0,
                    "color": {"mode": "fixed", "fixedColor": "blue"},
                    "custom": {"fillOpacity": 70, "lineWidth": 0},
                },
                "overrides": [],
            },
            "options": {
                "xField": "bearing",
                "orientation": "vertical",
                "showValue": "never",
                "barWidth": 0.9,
                "xTickLabelRotation": -45,
                "xTickLabelSpacing": 0,
                "legend": {"showLegend": False, "displayMode": "list", "placement": "bottom"},
                "tooltip": {"mode": "single", "sort": "none"},
            },
            "gridPos": {"x": 14, "y": y, "w": 10, "h": 14},
        }
    )
    y += 14
    ra, rb = dict(target(SQL["outline_all"]), refId="A"), dict(target(SQL["outline_range"]), refId="B")
    loc = {"mode": "coords", "latitude": "lat", "longitude": "lon"}

    def dot(color):
        return {
            "size": {"fixed": 3},
            "color": {"fixed": color},
            "opacity": 0.8,
            "symbol": {"fixed": "img/icons/marker/circle.svg"},
        }

    panels.append(
        {
            "id": nid(),
            "type": "geomap",
            "title": "Reception range",
            "description": "Furthest position heard in each degree of bearing. Blue: all time ("
            "updated nightly). Green: the selected range (last 90 days at most). Positions only "
            "count if the same aircraft's neighbouring position is within 10 minutes at under "
            "800 kt, so bad decodes don't draw spikes.",
            "datasource": DS,
            "targets": [ra, rb],
            "fieldConfig": {"defaults": {}, "overrides": []},
            "options": {
                # "fit" can't take an extent from the route layers reliably, so fit to the all-time
                # markers layer that rides along for that purpose; maxZoom stops a sparse outline
                # zooming in to street level.
                "view": {"id": "fit", "allLayers": False, "layer": "All-time points", "padding": 5, "maxZoom": 9},
                "controls": {"showZoom": True, "mouseWheelZoom": True, "showAttribution": True},
                "basemap": {"type": "default", "name": "Basemap"},
                "layers": [
                    {
                        "type": "route",
                        "name": "All time",
                        "tooltip": True,
                        "location": loc,
                        "filterData": {"id": "byRefId", "options": "A"},
                        "config": {
                            "arrow": 0,
                            "style": {"color": {"fixed": "#5794F2"}, "lineWidth": 2, "opacity": 0.9},
                        },
                    },
                    {
                        "type": "route",
                        "name": "Selected range",
                        "tooltip": True,
                        "location": loc,
                        "filterData": {"id": "byRefId", "options": "B"},
                        "config": {
                            "arrow": 0,
                            "style": {"color": {"fixed": "#73BF69"}, "lineWidth": 2, "opacity": 0.9},
                        },
                    },
                    # Dots under both lines: tooltips, and still visible if the beta Route layer misbehaves.
                    {
                        "type": "markers",
                        "name": "All-time points",
                        "tooltip": True,
                        "location": loc,
                        "filterData": {"id": "byRefId", "options": "A"},
                        "config": {"showLegend": False, "style": dot("#5794F2")},
                    },
                    {
                        "type": "markers",
                        "name": "Selected-range points",
                        "tooltip": True,
                        "location": loc,
                        "filterData": {"id": "byRefId", "options": "B"},
                        "config": {"showLegend": False, "style": dot("#73BF69")},
                    },
                ],
                "tooltip": {"mode": "details"},
            },
            "gridPos": {"x": 0, "y": y, "w": 14, "h": 14},
        }
    )
    panels.append(
        table(
            "Furthest ever",
            "The ten furthest bearings in the all-time outline, with what was heard there.",
            SQL["furthest_ever"],
            {"x": 14, "y": y, "w": 10, "h": 14},
            [
                unit_override("distance", "suffix: nm", 1),
                unit_override("bearing", "degree"),
                unit_override("altitude", "suffix: ft"),
            ],
        )
    )
    y += 14
    # Weather lines always draw points: reports are sparse, and a lone value
    # between empty slots has no neighbour to draw a line to.
    panels.append(row("Weather aloft", y))
    y += 1
    panels.append(
        timeseries(
            "Wind speed",
            "Median wind speed reported by aircraft, per altitude band. Aircraft derive it "
            "from airspeed, heading and ground track, so it is only as good as their instruments.",
            SQL["wind_speed"],
            {"x": 0, "y": y, "w": 8, "h": 9},
            unit="suffix: kt",
            interval="5m",
            legend=True,
            points=True,
        )
    )
    panels.append(
        timeseries(
            "Outside air temperature",
            "Median static air temperature reported by aircraft, per altitude band.",
            SQL["oat"],
            {"x": 8, "y": y, "w": 8, "h": 9},
            unit="celsius",
            interval="5m",
            legend=True,
            minimum=None,
            points=True,
        )
    )
    panels.append(
        timeseries(
            "Wind direction",
            "Direction the wind blows FROM, circular mean per altitude band (0/360 north, "
            "270 west). A steady westerly at 30k ft and up is the jet stream.",
            SQL["wind_dir"],
            {"x": 16, "y": y, "w": 8, "h": 9},
            unit="degree",
            interval="5m",
            draw="points",
            legend=True,
            minimum=0,
            maximum=360,
        )
    )
    y += 9
    panels.append(row("GPS integrity", y))
    y += 1
    panels.append(
        timeseries(
            "Aircraft with degraded GPS",
            "Share of aircraft reporting NIC < 7 or NACp < 8, per interval, with "
            "GPSJam's (bad - 1) / total so one faulty aircraft doesn't count. GPSJam colours: under 2% normal, "
            "2-10% some interference, over 10% likely jamming.",
            SQL["gps_degraded"],
            {"x": 0, "y": y, "w": 10, "h": 8},
            unit="percentunit",
            interval="30m",
            points=True,
            maximum=None,
            thresholds=[
                {"value": None, "color": "green"},
                {"value": 0.02, "color": "yellow"},
                {"value": 0.10, "color": "red"},
            ],
        )
    )
    panels.append(
        table(
            "Aircraft that reported degraded GPS",
            "Many aircraft at once points at interference; the same "
            "aircraft every time points at its own avionics. Lower is worse for both codes.",
            SQL["gps_aircraft"],
            {"x": 10, "y": y, "w": 14, "h": 8},
        )
    )
    y += 8
    panels.append(row("Notable", y))
    y += 1
    panels.append(
        table(
            "Lowest airborne aircraft",
            "Each aircraft's lowest barometric altitude in the range, "
            "lowest first. Distance is from the receiver.",
            SQL["lowest"],
            {"x": 0, "y": y, "w": 24, "h": 9},
            [unit_override("altitude", "suffix: ft"), unit_override("distance", "suffix: nm", 1)],
        )
    )
    y += 9
    panels.append(
        table(
            "Top operators",
            "Distinct aircraft per registered operator (tar1090 database).",
            SQL["operators"],
            {"x": 0, "y": y, "w": 12, "h": 10},
        )
    )
    panels.append(
        table(
            "Top aircraft types",
            "Distinct aircraft per ICAO type code.",
            SQL["types"],
            {"x": 12, "y": y, "w": 12, "h": 10},
        )
    )
    y += 10
    panels.append(
        table(
            "Emergency squawks",
            "7500 hijack, 7600 radio failure, 7700 emergency. Empty is normal.",
            SQL["emergencies"],
            {"x": 0, "y": y, "w": 24, "h": 6},
        )
    )
    y += 6
    panels.append(
        table(
            "Military and interesting aircraft",
            "Aircraft tar1090's database flags as military "
            "or interesting, with their lowest altitude and closest approach in the range.",
            SQL["flagged"],
            {"x": 0, "y": y, "w": 24, "h": 8},
            [unit_override("lowest", "suffix: ft"), unit_override("closest", "suffix: nm", 1)],
        )
    )

    y += 8
    panels.append(row("Long-term", y))
    y += 1
    note = " From the receiver's own replay history, 1 sample per aircraft per minute, local days."
    panels.append(
        timeseries(
            "Aircraft per day",
            "Distinct aircraft each day." + note,
            SQL["daily_aircraft"],
            {"x": 0, "y": y, "w": 8, "h": 8},
            interval="1d",
            points=True,
            time_from="2y",
        )
    )
    panels.append(
        timeseries(
            "Furthest reception per day",
            "Longest range each day; a step change usually means the antenna, cable or gain changed." + note,
            SQL["daily_range"],
            {"x": 8, "y": y, "w": 8, "h": 8},
            unit="suffix: nm",
            interval="1d",
            points=True,
            time_from="2y",
        )
    )
    panels.append(
        timeseries(
            "Busiest hour",
            "Local hour with the most distinct aircraft each day." + note,
            SQL["daily_busiest"],
            {"x": 16, "y": y, "w": 8, "h": 8},
            interval="1d",
            draw="points",
            minimum=0,
            maximum=23,
            time_from="2y",
        )
    )
    y += 8
    panels.append(row("Flights (from history, updated nightly)", y))
    y += 1
    fnote = " Flights split where an aircraft went unheard for over 30 minutes; rebuilt nightly, so today shows tomorrow."
    panels.append(
        table(
            "Flight log",
            "Every flight that started in the range, newest first (500 max). "
            "Entered/left at: bearing from the receiver where it was first and last heard." + fnote,
            SQL["flight_log"],
            {"x": 0, "y": y, "w": 24, "h": 12},
            [
                unit_override("duration", "s"),
                unit_override("lowest", "suffix: ft"),
                unit_override("closest", "suffix: nm", 1),
                unit_override("entered at", "degree"),
                unit_override("left at", "degree"),
            ],
        )
    )
    y += 12
    panels.append(
        timeseries(
            "Flights per day",
            "Flights starting each local day." + fnote,
            SQL["flights_per_day"],
            {"x": 0, "y": y, "w": 12, "h": 8},
            interval="1d",
            points=True,
            time_from="2y",
        )
    )
    panels.append(
        timeseries(
            "New aircraft per day",
            "Aircraft seen for the first time ever that day. The first "
            "weeks of history count everything as new." + fnote,
            SQL["new_airframes"],
            {"x": 12, "y": y, "w": 12, "h": 8},
            interval="1d",
            points=True,
            time_from="2y",
        )
    )
    y += 8
    grid = table(
        "Flights by weekday and hour",
        "Flights starting in each local weekday and hour over the range; darker is busier." + fnote,
        SQL["week_grid"],
        {"x": 0, "y": y, "w": 24, "h": 8},
    )
    grid["fieldConfig"]["defaults"]["custom"]["cellOptions"] = {
        "type": "color-background",
        "mode": "gradient",
    }
    grid["fieldConfig"]["defaults"]["color"] = {"mode": "continuous-BlPu"}
    grid["fieldConfig"]["overrides"] = [
        {
            "matcher": {"id": "byName", "options": "day"},
            "properties": [{"id": "custom.cellOptions", "value": {"type": "auto"}}],
        }
    ]
    panels.append(grid)
    y += 8
    panels.append(
        table(
            "Regulars",
            "Aircraft with the most flights in the range." + fnote,
            SQL["regulars"],
            {"x": 0, "y": y, "w": 12, "h": 10},
        )
    )
    panels.append(
        table(
            "Rare visitors",
            "Flights in the range by aircraft types seen on 3 days or fewer in all history." + fnote,
            SQL["rare"],
            {"x": 12, "y": y, "w": 12, "h": 10},
            [unit_override("closest", "suffix: nm", 1)],
        )
    )
    return {
        "uid": "skyledger-all",
        "title": "skyledger",
        "description": "",
        "tags": ["skyledger"],
        "schemaVersion": 41,
        "editable": True,
        "graphTooltip": 0,
        "time": {"from": "now-24h", "to": "now"},
        "refresh": "1m",
        "timezone": "",
        "templating": {"list": []},
        "annotations": {"list": []},
        "links": [],
        "panels": panels,
    }


# --- Split into three linked dashboards ------------------------------------
# Never change a uid once released: Grafana keeps the old dashboard as a
# provisioned duplicate that can't be deleted from the UI.
LAYOUT = {
    "skyledger-overview": (
        "skyledger Overview",
        "What's overhead now, how far the receiver reaches, notable aircraft.",
        [
            (
                "Now",
                [
                    "Last write",
                    "Aircraft now",
                    "Aircraft in range",
                    "Positions in range",
                    "Furthest reception",
                ],
            ),
            ("Reception range", ["Reception range", "Furthest ever"]),
            ("Traffic", ["Aircraft tracked", "Aircraft by altitude"]),
            (
                "Notable",
                ["Lowest airborne aircraft", "Military and interesting aircraft", "Emergency squawks"],
            ),
        ],
    ),
    "skyledger-receiver": (
        "skyledger Receiver",
        "How well the receiver is working: signal, decode rate, GPS integrity, coverage.",
        [
            (
                "Receiver",
                [
                    "Signal and noise",
                    "Messages per second",
                    "Strong signals",
                    "Aircraft heard",
                    "Max range",
                    "SDR gain",
                    "Frequency error",
                    "Receiver stats age",
                ],
            ),
            ("GPS integrity", ["Aircraft with degraded GPS", "Aircraft that reported degraded GPS"]),
            ("Coverage", ["Where positions were received", "Range by bearing"]),
        ],
    ),
    "skyledger-history": (
        "skyledger History",
        "Flights, long-term trends and weather aloft, from the history import.",
        [
            (
                "Flights (from history, updated nightly)",
                [
                    "Flight log",
                    "Flights per day",
                    "New aircraft per day",
                    "Flights by weekday and hour",
                    "Regulars",
                    "Rare visitors",
                ],
            ),
            ("Long-term", ["Aircraft per day", "Furthest reception per day", "Busiest hour"]),
            ("Weather aloft", ["Wind speed", "Outside air temperature", "Wind direction"]),
            ("Operators and types", ["Top operators", "Top aircraft types"]),
        ],
    ),
}


def split(full):
    by_title = {p["title"]: p for p in full["panels"] if p["type"] != "row"}
    placed = [t for _, _, sections in LAYOUT.values() for _, titles in sections for t in titles]
    assert sorted(placed) == sorted(by_title), set(by_title) ^ set(placed)
    out = {}
    for uid, (title, desc, sections) in LAYOUT.items():
        panels, y, pid = [], 0, 1
        for row_title, titles in sections:
            panels.append({**row(row_title, y), "id": pid})
            pid += 1
            y += 1
            members = sorted(
                (by_title[t] for t in titles), key=lambda p: (p["gridPos"]["y"], p["gridPos"]["x"])
            )
            # Close gaps left by panels that moved to another dashboard: each
            # original y band keeps its internal layout, bands stack tightly.
            bands = {}
            for p in members:
                bands.setdefault(p["gridPos"]["y"], []).append(p)
            for band_y in sorted(bands):
                band = bands[band_y]
                top = min(p["gridPos"]["y"] for p in band)
                height = max(p["gridPos"]["y"] + p["gridPos"]["h"] for p in band) - top
                for p in band:
                    panels.append(
                        {**p, "id": pid, "gridPos": {**p["gridPos"], "y": y + p["gridPos"]["y"] - top}}
                    )
                    pid += 1
                y += height
        out[uid] = {
            **full,
            "uid": uid,
            "title": title,
            "description": desc,
            "tags": ["skyledger"],
            "links": [
                {
                    "title": "skyledger",
                    "type": "dashboards",
                    "tags": ["skyledger"],
                    "asDropdown": False,
                    "includeVars": False,
                    "keepTime": True,
                    "targetBlank": False,
                }
            ],
            "panels": panels,
        }
    return out


OUT = pathlib.Path(__file__).resolve().parents[1] / "charts" / "skyledger" / "grafana" / "dashboards"


def render():
    """{file name: JSON text} for every dashboard."""
    return {f"{uid}.json": json.dumps(dash, indent=2) + "\n" for uid, dash in split(build()).items()}


def all_queries():
    """{"<dashboard>/<panel>/<refId>": sql} for every Postgres query in every dashboard."""
    out = {}
    for uid, dash in split(build()).items():
        for panel in dash["panels"]:
            for t in panel.get("targets", []):
                if "rawSql" in t:
                    out[f"{uid}/{panel['title']}/{t['refId']}"] = t["rawSql"]
    return out


def main(argv):
    files = render()
    if argv == ["--sql"]:
        print(json.dumps(all_queries(), indent=2))
        return 0
    if argv == ["--check"]:
        stale = [n for n, text in files.items() if not (OUT / n).exists() or (OUT / n).read_text() != text]
        extra = sorted(p.name for p in OUT.glob("*.json") if p.name not in files)
        if stale or extra:
            print(f"dashboards out of date: {stale or ''} {extra or ''}. Run: uv run tools/gen_dashboards.py")
            return 1
        return 0
    OUT.mkdir(parents=True, exist_ok=True)
    for name, text in files.items():
        (OUT / name).write_text(text)
        print(f"wrote {OUT.name}/{name}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
