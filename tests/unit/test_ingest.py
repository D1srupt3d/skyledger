import re

from skyledger import ingest, migrate

AIRCRAFT = {"now": 1000.0, "aircraft": [
    {"hex": "a1", "lat": 1.0, "lon": 2.0, "seen_pos": 1.5, "alt_baro": "ground", "flight": "ABC1    ",
     "emergency": "none", "nav_modes": []},
    {"hex": "a2", "lat": 1.0, "lon": 2.0, "seen_pos": 30.0, "alt_baro": 30000},   # stale: dropped
    {"hex": "a3", "alt_baro": 30000},                                             # no position: dropped
    {"hex": "a4", "lat": 1.0, "lon": 2.0, "seen_pos": 0.2, "alt_baro": 36000, "flight": "   ",
     "emergency": "general", "wd": 270, "ws": 85, "oat": -48, "tat": -21, "mach": 0.78,
     "nav_modes": ["autopilot", "lnav"], "dbFlags": 1, "type": "adsb_icao", "nic": 6, "nac_p": 10},
]}
COL = {c: i for i, c in enumerate(ingest.POSITION_COLUMNS)}


def table_columns(table):
    sql = migrate.read("0001_init.sql")
    body = re.search(rf"CREATE TABLE {table} \((.*?)\n\);", sql, re.S).group(1)
    return [line.split()[0] for line in body.strip().splitlines() if line.strip()]


def test_parse_aircraft():
    pos, ac = ingest.parse_aircraft(AIRCRAFT, 10)
    assert [r[COL["hex"]] for r in pos] == ["a1", "a4"]
    a1, a4 = pos
    assert a1[COL["flight"]] == "ABC1" and a1[COL["alt_baro"]] is None and a1[COL["on_ground"]] is True
    assert a1[COL["emergency"]] is None and a1[COL["nav_modes"]] == [] and a1[COL["wind_speed"]] is None
    assert a4[COL["flight"]] is None and a4[COL["on_ground"]] is False and a4[COL["alt_baro"]] == 36000
    assert a4[COL["emergency"]] == "general" and a4[COL["wind_dir"]] == 270 and a4[COL["wind_speed"]] == 85
    assert a4[COL["oat"]] == -48 and a4[COL["mach"]] == 0.78 and a4[COL["nav_modes"]] == ["autopilot", "lnav"]
    assert a4[COL["source"]] == "adsb_icao" and a4[COL["nic"]] == 6 and a4[COL["nac_p"]] == 10
    assert a1[COL["time"]].timestamp() == 998.5 and a1[COL["time"]].tzinfo is not None
    assert ac[0][7] == 0 and ac[1][7] == 1  # dbFlags defaults to 0
    assert all(len(r) == len(ingest.POSITION_COLUMNS) for r in pos)
    assert ingest.INSERT_POSITION.count("%s") == len(ingest.POSITION_COLUMNS)
    assert ingest.UPSERT_AIRCRAFT.count("%s") == len(ac[0])


def test_code_matches_schema():
    assert sorted(ingest.POSITION_COLUMNS) == sorted(table_columns("positions"))
    assert list(ingest.STATS_COLUMNS) == table_columns("receiver_stats")


STATS = {"now": 1790376975.0, "gain_db": 43.4, "estimated_ppm": -2.7, "aircraft_with_pos": 47,
         "aircraft_without_pos": 6,
         "last1min": {"start": 1790376900.0, "end": 1790376960.0, "messages": 900000, "messages_valid": 24980,
                      "position_count_total": 2800, "max_distance": 279093,
                      "local": {"signal": -11.0, "noise": -31.0, "peak_signal": -0.9, "strong_signals": 1200,
                                "samples_dropped": 0, "samples_lost": 0}}}


def test_parse_stats():
    row = dict(zip(ingest.STATS_COLUMNS, ingest.parse_stats(STATS), strict=True))
    assert row["time"].timestamp() == 1790376960.0 and row["window_seconds"] == 60.0
    assert row["signal"] == -11.0 and row["noise"] == -31.0 and row["messages_valid"] == 24980
    assert row["max_distance_m"] == 279093 and row["gain_db"] == 43.4 and row["ppm"] == -2.7
    assert row["aircraft_with_pos"] == 47 and row["samples_lost"] == 0


def test_parse_stats_network_only_readsb():
    stats = {**STATS, "last1min": {k: v for k, v in STATS["last1min"].items() if k != "local"}}
    row = dict(zip(ingest.STATS_COLUMNS, ingest.parse_stats(stats), strict=True))
    assert row["signal"] is None and row["noise"] is None and row["messages_valid"] == 24980


def test_parse_stats_without_window():
    assert ingest.parse_stats({"now": 1.0}) is None
    assert ingest.parse_stats({"last1min": {}}) is None
