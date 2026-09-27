"""The demo feed must be readable by skyledger's real parsers, end to end."""

import datetime
import json

from skyledger import aircraftdb, demo, heatmap, ingest

NOW = datetime.datetime(2026, 5, 20, 12, 34, 56, tzinfo=datetime.UTC).timestamp()


def get(path, now=NOW):
    status, _, body = demo.respond(path, now)
    return status, body


def minutes(start, hours):
    return range(int(start), int(start + hours * 3600), 60)


def test_live_feed_parses():
    status, body = get("/data/aircraft.json")
    pos, ac = ingest.parse_aircraft(json.loads(body), 10)
    assert status == 200 and len(pos) == len(demo.visible(NOW)) == len(ac)
    col = {c: i for i, c in enumerate(ingest.POSITION_COLUMNS)}
    assert all(r[col["nic"]] == 8 for r in pos)
    # The military one is a rare visitor: find a moment it's in range.
    rch = next(p for p in demo.PLANES if p.mil)
    when = next(t for t in minutes(NOW, 72) if rch.at(t))
    _, ac = ingest.parse_aircraft(json.loads(get("/data/aircraft.json", when)[1]), 10)
    assert sum(1 for a in ac if a[7] == 1) == 1


def test_positions_stay_in_range_and_move():
    for t in minutes(NOW, 24):
        for p, a in demo.visible(t):
            b = p.at(t + 30)
            d, _ = heatmap.range_and_bearing(*demo.RECEIVER, *a)
            assert d < 270, (p.callsign, d)
            assert b is None or a != b


def test_planes_come_and_go():
    """Each plane leaves range for over 30 minutes between passes, so history splits into many flights."""
    for p in demo.PLANES:
        seen = "".join("x" if p.at(t) else "." for t in minutes(NOW, 24 * demo.DEMO_DAYS))  # x: in range
        passes = [r for r in seen.split(".") if r]
        gaps = [g for g in seen.strip(".").split("x") if g]
        assert len(passes) >= 2, p.callsign
        assert min(len(g) for g in gaps) > 30, p.callsign


def test_never_an_empty_sky():
    assert min(len(demo.visible(t)) for t in minutes(NOW, 24 * demo.DEMO_DAYS)) >= 2


def test_one_rare_visitor():
    days = {}
    for t in minutes(NOW - 86400 * demo.DEMO_DAYS, 24 * demo.DEMO_DAYS):
        for p, _ in demo.visible(t):
            days.setdefault(p.type_code, set()).add(t // 86400)
    rare = [t for t, d in days.items() if len(d) <= 3]
    assert rare == ["C17"], {t: len(d) for t, d in days.items()}


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
    start = datetime.datetime.combine(day, datetime.time(5), datetime.UTC).timestamp()
    expected = sum(len(demo.visible(t)) for t in minutes(start, 0.5))  # one per aircraft per minute
    assert status == 200 and len(rows) == expected > 0
    assert all(r[col["flight"]] for r in rows) and max(r[col["distance_nm"]] for r in rows) < 270


def test_heatmap_only_finished_recent_half_hours():
    today = datetime.datetime.fromtimestamp(NOW, datetime.UTC).date()
    assert get(f"/globe_history/{today:%Y/%m/%d}/heatmap/47.bin.ttf")[0] == 404   # later today
    assert get(f"/globe_history/{today:%Y/%m/%d}/heatmap/00.bin.ttf")[0] == 200   # earlier today
    old = today - datetime.timedelta(days=demo.DEMO_DAYS + 1)
    assert get(f"/globe_history/{old:%Y/%m/%d}/heatmap/00.bin.ttf")[0] == 404
    assert get("/nope")[0] == 404
