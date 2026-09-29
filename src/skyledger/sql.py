"""SQL shared by the history importer (ported unchanged from the tested homelab importer)."""

# Rebuilds daily_stats for local days [%(first)s, %(last)s]; the bounds are
# local midnights, so each day is computed from whole days of positions.
REFRESH_DAILY = """
WITH p AS (
    SELECT (time AT TIME ZONE %(tz)s)::date AS day,
           extract(hour FROM time AT TIME ZONE %(tz)s)::smallint AS hour,
           hex, distance_nm
    FROM history_positions
    WHERE time >= (%(first)s::date)::timestamp AT TIME ZONE %(tz)s
      AND time < (%(last)s::date + 1)::timestamp AT TIME ZONE %(tz)s
), d AS (
    SELECT day, count(DISTINCT hex) AS aircraft, count(*) AS positions, max(distance_nm) AS max_range
    FROM p GROUP BY day
), h AS (
    SELECT DISTINCT ON (day) day, hour, n
    FROM (SELECT day, hour, count(DISTINCT hex) AS n FROM p GROUP BY day, hour) per_hour
    ORDER BY day, n DESC, hour
)
INSERT INTO daily_stats (day, aircraft, positions, max_range_nm, busiest_hour, busiest_hour_aircraft)
SELECT d.day, d.aircraft, d.positions, d.max_range, h.hour, h.n FROM d JOIN h USING (day)
ON CONFLICT (day) DO UPDATE SET
    aircraft = EXCLUDED.aircraft,
    positions = EXCLUDED.positions,
    max_range_nm = EXCLUDED.max_range_nm,
    busiest_hour = EXCLUDED.busiest_hour,
    busiest_hour_aircraft = EXCLUDED.busiest_hour_aircraft
"""

# Extends history_outline with positions in [%(start)s, %(end)s). A position
# counts only if the same aircraft's previous or next position is within 10
# minutes and reachable at under 800 kt: bad decodes jump hundreds of nm in a
# minute and would otherwise draw spikes; real long-range tracks pass.
# `%%` is a literal modulo: this query takes parameters, so psycopg reads `%`.
UPDATE_OUTLINE = """
WITH p AS (
    SELECT hex, time, lat, lon, alt_baro, distance_nm, bearing,
           lag(time) OVER w AS prev_time, lag(lat) OVER w AS prev_lat, lag(lon) OVER w AS prev_lon,
           lead(time) OVER w AS next_time, lead(lat) OVER w AS next_lat, lead(lon) OVER w AS next_lon
    FROM history_positions
    WHERE time >= %(start)s::timestamptz AND time < %(end)s::timestamptz
      AND distance_nm IS NOT NULL AND bearing IS NOT NULL
    WINDOW w AS (PARTITION BY hex ORDER BY time)
), jumps AS (
    SELECT *,
        CASE WHEN time - prev_time <= interval '10 minutes' THEN
            2 * 3440.065 * asin(sqrt(sin(radians(lat - prev_lat) / 2) ^ 2
                + cos(radians(lat)) * cos(radians(prev_lat)) * sin(radians(lon - prev_lon) / 2) ^ 2))
            / greatest(extract(epoch FROM time - prev_time) / 3600, 1.0 / 3600) END AS prev_kt,
        CASE WHEN next_time - time <= interval '10 minutes' THEN
            2 * 3440.065 * asin(sqrt(sin(radians(next_lat - lat) / 2) ^ 2
                + cos(radians(lat)) * cos(radians(next_lat)) * sin(radians(next_lon - lon) / 2) ^ 2))
            / greatest(extract(epoch FROM next_time - time) / 3600, 1.0 / 3600) END AS next_kt
    FROM p
), best AS (
    SELECT DISTINCT ON (deg) deg, lat, lon, distance_nm, alt_baro, hex, time
    FROM (SELECT floor(bearing)::int %% 360 AS deg, * FROM jumps WHERE prev_kt < 800 OR next_kt < 800) ok
    ORDER BY deg, distance_nm DESC
)
INSERT INTO history_outline (deg, lat, lon, distance_nm, alt_baro, hex, time)
SELECT deg, lat, lon, distance_nm, alt_baro, hex, time FROM best
ON CONFLICT (deg) DO UPDATE SET
    lat = EXCLUDED.lat, lon = EXCLUDED.lon, distance_nm = EXCLUDED.distance_nm,
    alt_baro = EXCLUDED.alt_baro, hex = EXCLUDED.hex, time = EXCLUDED.time
WHERE EXCLUDED.distance_nm > history_outline.distance_nm
"""

REFRESH_FLIGHTS = "REFRESH MATERIALIZED VIEW flights"

# Copies history_positions into gaps in the live positions (an ingest or database
# outage), so the live panels show what the receiver heard meanwhile. Only rows
# strictly inside a gap are copied: history is one sample per aircraft per minute,
# so a filled gap is closed and a rerun copies nothing. History has no wind,
# signal or accuracy fields, so those panels stay empty over a filled gap.
FILL_LIVE_GAPS = """
WITH gaps AS (
    SELECT prev, time AS next
    FROM (SELECT time, lag(time) OVER (ORDER BY time) AS prev FROM positions) t
    WHERE time - prev > interval '3 minutes'
)
INSERT INTO positions (time, hex, flight, squawk, source, lat, lon, alt_baro, on_ground, gs,
                       distance_nm, bearing)
SELECT h.time, h.hex, h.flight, h.squawk, h.source, h.lat, h.lon, h.alt_baro, h.on_ground, h.gs,
       h.distance_nm, h.bearing
FROM gaps JOIN history_positions h ON h.time > gaps.prev AND h.time < gaps.next
"""
