"""Live ingest: fresh positions from aircraft.json, receiver health from stats.json."""

import datetime
import logging
import pathlib
import time

import psycopg

from skyledger.feed import Feed

log = logging.getLogger(__name__)

# Probes: `alive` is touched every loop (the loop isn't hung), `ready` after
# every successful write (data is flowing). The Dockerfile health check reads `ready`.
ALIVE = pathlib.Path("/tmp/alive")
READY = pathlib.Path("/tmp/ready")
STATS_EVERY = 60  # readsb rewrites stats.json once a minute

# positions column -> aircraft.json key, for fields stored as-is. Adding a
# field is one line here plus the column in a new migration; the
# INSERT is generated from this, so columns and values can't drift apart.
PASSTHROUGH = {
    "squawk": "squawk",
    "source": "type",
    "lat": "lat",
    "lon": "lon",
    "alt_geom": "alt_geom",
    "gs": "gs",
    "track": "track",
    "baro_rate": "baro_rate",
    "distance_nm": "r_dst",
    "bearing": "r_dir",
    "rssi": "rssi",
    "wind_dir": "wd",
    "wind_speed": "ws",
    "oat": "oat",
    "tat": "tat",
    "ias": "ias",
    "tas": "tas",
    "mach": "mach",
    "roll": "roll",
    "mag_heading": "mag_heading",
    "true_heading": "true_heading",
    "track_rate": "track_rate",
    "geom_rate": "geom_rate",
    "nav_altitude_mcp": "nav_altitude_mcp",
    "nav_altitude_fms": "nav_altitude_fms",
    "nav_heading": "nav_heading",
    "nav_qnh": "nav_qnh",
    "nav_modes": "nav_modes",
    # The aircraft's own GPS integrity (NIC) and accuracy (NACp) categories:
    # a region-wide drop is how GPS jamming shows up.
    "nic": "nic",
    "nac_p": "nac_p",
}

POSITION_COLUMNS = ["time", "hex", "flight", "alt_baro", "on_ground", "emergency", *PASSTHROUGH]

INSERT_POSITION = (
    f"INSERT INTO positions ({', '.join(POSITION_COLUMNS)}) "
    f"VALUES ({', '.join(['%s'] * len(POSITION_COLUMNS))})"
)

UPSERT_AIRCRAFT = """
INSERT INTO aircraft (hex, registration, type_code, description, operator,
    year, category, db_flags, first_seen, last_seen)
VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
ON CONFLICT (hex) DO UPDATE SET
    registration = EXCLUDED.registration,
    type_code = EXCLUDED.type_code,
    description = EXCLUDED.description,
    operator = EXCLUDED.operator,
    year = EXCLUDED.year,
    category = EXCLUDED.category,
    db_flags = EXCLUDED.db_flags,
    last_seen = EXCLUDED.last_seen
"""

def parse_aircraft(feed, max_age):
    """Split one aircraft.json snapshot into position rows and aircraft rows.

    Only positions decoded within the last poll are kept: readsb repeats the
    last known position until it goes stale, and storing those repeats would
    duplicate rows.
    """
    positions, aircraft = [], []
    for a in feed["aircraft"]:
        seen_pos = a.get("seen_pos")
        if "lat" not in a or seen_pos is None or seen_pos >= max_age:
            continue
        at = datetime.datetime.fromtimestamp(feed["now"] - seen_pos, datetime.UTC)
        alt = a.get("alt_baro")
        # readsb sends the string "ground" instead of an altitude on the ground.
        on_ground = alt == "ground"
        emergency = a.get("emergency")
        row = {
            "time": at,
            "hex": a["hex"],
            "flight": (a.get("flight") or "").strip() or None,
            "alt_baro": None if on_ground else alt,
            "on_ground": on_ground,
            "emergency": None if emergency == "none" else emergency,
        }
        row.update({col: a.get(key) for col, key in PASSTHROUGH.items()})
        positions.append(tuple(row[col] for col in POSITION_COLUMNS))
        aircraft.append((
            a["hex"], a.get("r"), a.get("t"), a.get("desc"), a.get("ownOp"),
            a.get("year"), a.get("category"), a.get("dbFlags", 0), at, at,
        ))
    return positions, aircraft

STATS_COLUMNS = ("time", "window_seconds", "signal", "noise", "peak_signal", "strong_signals", "messages",
                 "messages_valid", "positions", "max_distance_m", "aircraft_with_pos", "aircraft_without_pos",
                 "gain_db", "ppm", "samples_dropped", "samples_lost")
INSERT_STATS = (
    f"INSERT INTO receiver_stats ({', '.join(STATS_COLUMNS)}) "
    f"VALUES ({', '.join(['%s'] * len(STATS_COLUMNS))}) ON CONFLICT (time) DO NOTHING"
)


def parse_stats(stats):
    """stats.json -> one receiver_stats row for its last complete minute, or None if absent.

    `local` only exists when readsb has its own SDR (not a network-only readsb), so
    those fields may be None.
    """
    m = stats.get("last1min")
    if not m or "end" not in m:
        return None
    local = m.get("local", {})
    row = {
        "time": datetime.datetime.fromtimestamp(m["end"], datetime.UTC),
        "window_seconds": m["end"] - m["start"] if "start" in m else None,
        "signal": local.get("signal"),
        "noise": local.get("noise"),
        "peak_signal": local.get("peak_signal"),
        "strong_signals": local.get("strong_signals"),
        "messages": m.get("messages"),
        "messages_valid": m.get("messages_valid"),
        "positions": m.get("position_count_total"),
        "max_distance_m": m.get("max_distance"),
        "aircraft_with_pos": stats.get("aircraft_with_pos"),
        "aircraft_without_pos": stats.get("aircraft_without_pos"),
        "gain_db": stats.get("gain_db"),
        "ppm": stats.get("estimated_ppm"),
        "samples_dropped": local.get("samples_dropped"),
        "samples_lost": local.get("samples_lost"),
    }
    return tuple(row[c] for c in STATS_COLUMNS)


def poll_once(conn, feed, settings, with_stats):
    """One ingest cycle: fresh positions, and receiver stats when asked. Returns positions stored."""
    positions, aircraft = parse_aircraft(feed.get_json(feed.aircraft), settings.poll_seconds)
    with conn.transaction(), conn.cursor() as cur:
        cur.executemany(INSERT_POSITION, positions)
        cur.executemany(UPSERT_AIRCRAFT, aircraft)
    if with_stats:
        row = parse_stats(feed.get_json(feed.stats))
        if row:
            conn.execute(INSERT_STATS, row)
    return len(positions)


def run(settings):
    feed = Feed(settings.tar1090_url, timeout=5)
    conn = None
    last_stats = float("-inf")
    log.info("polling %s every %ss", feed.aircraft, settings.poll_seconds)
    while True:
        started = time.monotonic()
        try:
            if conn is None:
                conn = psycopg.connect(settings.database_url, autocommit=True, connect_timeout=5)
            with_stats = started - last_stats >= STATS_EVERY
            poll_once(conn, feed, settings, with_stats)
            if with_stats:
                last_stats = started
            READY.touch()
        except Exception as e:  # noqa: BLE001 - one bad poll must not kill the loop
            log.warning("poll failed: %r", e)
            if conn is not None:
                conn.close()
            conn = None
        ALIVE.touch()
        time.sleep(max(0.0, settings.poll_seconds - (time.monotonic() - started)))
