package queries

// The web UI's own SQL, for what no dashboard panel shows. Plain bind
// parameters (no Grafana macros); the caller works out every time bound.

// LiveAircraft is each aircraft's latest position since $1, closest first.
const LiveAircraft = `SELECT p.hex, p.flight, a.registration, a.type_code, a.operator,
  coalesce(a.db_flags & 1 <> 0, false) AS military,
  p.alt_baro, p.on_ground, p.gs, p.track, p.distance_nm, p.bearing, p.squawk, p.emergency, p.time
FROM (SELECT DISTINCT ON (hex) * FROM positions WHERE time > $1 ORDER BY hex, time DESC) p
LEFT JOIN aircraft a USING (hex)
ORDER BY p.distance_nm NULLS LAST, p.hex`

// LiveTrails is one point (the latest) per aircraft per 30 s since $1, oldest first.
const LiveTrails = `SELECT DISTINCT ON (hex, time_bucket('30 seconds', time)) hex, bearing, distance_nm
FROM positions WHERE time > $1 AND distance_nm IS NOT NULL AND bearing IS NOT NULL
ORDER BY hex, time_bucket('30 seconds', time), time DESC`

// LastHeard is the latest flight before $2 of each hex in $1.
const LastHeard = `SELECT hex, max(last_seen) FROM flights
WHERE hex = ANY($1::text[]) AND last_seen < $2 GROUP BY hex`

// WithoutPosition is the newest receiver_stats count of aircraft without a position since $1, or NULL.
const WithoutPosition = `SELECT (SELECT aircraft_without_pos FROM receiver_stats
  WHERE time > $1 ORDER BY time DESC LIMIT 1)`

// ReceiverLocation is the receiver's lat and lon from db_meta, NULL until history has stored them.
const ReceiverLocation = `SELECT (SELECT value::float8 FROM db_meta WHERE key = 'rx_lat'),
  (SELECT value::float8 FROM db_meta WHERE key = 'rx_lon')`

// AircraftOnDay is daily_stats' aircraft count for local date $1, NULL before the nightly import.
const AircraftOnDay = "SELECT (SELECT aircraft FROM daily_stats WHERE day = $1::date)"
