"""The demo feed must be readable by skyledger's real parsers, end to end."""

import datetime
import json

from skyledger import aircraftdb, demo, heatmap, ingest

NOW = datetime.datetime(2026, 5, 20, 12, 34, 56, tzinfo=datetime.UTC).timestamp()


def get(path, now=NOW):
    status, _, body = demo.respond(path, now)
    return status, body


def test_live_feed_parses():
    status, body = get("/data/aircraft.json")
    pos, ac = ingest.parse_aircraft(json.loads(body), 10)
    assert status == 200 and len(pos) == len(demo.PLANES) == len(ac)
    col = {c: i for i, c in enumerate(ingest.POSITION_COLUMNS)}
    assert any(r[col["emergency"]] == "general" for r in pos)
    assert any(r[col["wind_speed"]] for r in pos) and all(r[col["nic"]] == 8 for r in pos)
    assert sum(1 for a in ac if a[7] == 1) == 1  # one military


def test_positions_stay_in_range_and_move():
    for p in demo.PLANES:
        a, b = p.at(NOW), p.at(NOW + 60)
        assert a != b
        d, _ = heatmap.range_and_bearing(*demo.RECEIVER, *a)
        assert d < 270, (p.callsign, d)


def test_stats_parse():
    status, body = get("/data/stats.json")
    row = dict(zip(ingest.STATS_COLUMNS, ingest.parse_stats(json.loads(body)), strict=True))
    assert status == 200 and row["signal"] == -12.0 and row["time"].timestamp() % 60 == 0


def test_database():
    status, body = get("/")
    version = aircraftdb.db_version(body.decode())
    assert status == 200 and version == demo.DB_VERSION
    shards = json.loads(get(f"/{version}/files.js")[1])
    rows = [r for s in shards for r in aircraftdb.db_rows(s, json.loads(get(f"/{version}/{s}.js")[1]))]
    assert {r[0] for r in rows} == {f"{p.hex:06x}" for p in demo.PLANES}
    assert sum(1 for r in rows if r[4] & 1) == 1
    assert aircraftdb.airline_rows(json.loads(get(f"/{version}/operators.js")[1]))[0][1] == "Demo Air"


def test_heatmap_history():
    day = datetime.datetime.fromtimestamp(NOW, datetime.UTC).date() - datetime.timedelta(days=1)
    status, body = get(f"/globe_history/{day:%Y/%m/%d}/heatmap/10.bin.ttf")
    rows = heatmap.decode(body, demo.RECEIVER)
    col = {c: i for i, c in enumerate(heatmap.COLUMNS)}
    assert status == 200 and len(rows) == 30 * len(demo.PLANES)  # one per aircraft per minute
    assert all(r[col["flight"]] for r in rows) and max(r[col["distance_nm"]] for r in rows) < 270


def test_heatmap_only_finished_recent_half_hours():
    today = datetime.datetime.fromtimestamp(NOW, datetime.UTC).date()
    assert get(f"/globe_history/{today:%Y/%m/%d}/heatmap/47.bin.ttf")[0] == 404   # later today
    assert get(f"/globe_history/{today:%Y/%m/%d}/heatmap/00.bin.ttf")[0] == 200   # earlier today
    old = today - datetime.timedelta(days=demo.DEMO_DAYS + 1)
    assert get(f"/globe_history/{old:%Y/%m/%d}/heatmap/00.bin.ttf")[0] == 404
    assert get("/nope")[0] == 404
