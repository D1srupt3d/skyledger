import datetime
import gzip

import pytest

from skyledger import heatmap as hm
from skyledger import heatmap_writer as w

RX = (40.0, -70.0)
T0 = 1790000040000  # a whole UTC minute, in ms
A, B = 0xABC123, (1 << 24) | 0x00BEEF  # B is a non-ICAO address
COL = {c: i for i, c in enumerate(hm.COLUMNS)}


def sample():
    return w.build([
        (T0, [
            w.position(A, 41.0, -70.0, alt_ft=35000, gs_kt=450),  # before A's callsign record
            w.callsign(A, "TEST1", 1200),
            w.position(B, -33.9, 151.2, gs_kt=200, source=5),     # mlat, southern latitude, alt unknown
            w.callsign(B, "", 7700),                              # squawk only
        ]),
        (T0 + 15000, [w.position(A, 41.01, -70.0, alt_ft=35000, gs_kt=450)]),  # same minute: dropped
        (T0 + 60000, [w.position(A, 41.02, -70.0, on_ground=True)]),           # ground, speed unknown
    ])


def test_decode_rules():
    rows = hm.decode(sample(), RX)
    assert len(rows) == 3
    a0, b, a1 = rows
    ts = datetime.datetime.fromtimestamp(T0 / 1000, datetime.UTC)
    assert a0[COL["time"]] == ts and a0[COL["hex"]] == "abc123" and a0[COL["source"]] == "adsb_icao"
    assert a0[COL["flight"]] == "TEST1" and a0[COL["squawk"]] == "1200"  # earliest-record fallback
    assert a0[COL["alt_baro"]] == 35000 and a0[COL["on_ground"]] is False and a0[COL["gs"]] == 450.0
    assert abs(a0[COL["distance_nm"]] - 60.0) < 0.2 and abs(a0[COL["bearing"]]) < 0.01  # 1 deg due north
    assert b[COL["hex"]] == "~00beef" and b[COL["source"]] == "mlat" and b[COL["lat"]] == -33.9
    assert b[COL["alt_baro"]] is None and b[COL["on_ground"]] is False
    assert b[COL["flight"]] is None and b[COL["squawk"]] == "7700"
    assert a1[COL["time"]] == ts + datetime.timedelta(minutes=1)
    assert a1[COL["on_ground"]] is True and a1[COL["alt_baro"]] is None and a1[COL["gs"]] is None
    assert a1[COL["flight"]] == "TEST1"


def test_gzip_accepted():
    assert hm.decode(gzip.compress(sample()), RX) == hm.decode(sample(), RX)


def test_bad_length_rejected():
    with pytest.raises(ValueError):
        hm.decode(sample() + b"x", RX)


def test_empty_file():
    assert hm.decode(b"", RX) == []


def test_range_and_bearing():
    d, brg = hm.range_and_bearing(40.0, -70.0, 40.0, -69.0)
    assert 45 < d < 46.5 and 89 < brg < 90.5  # 1 deg of longitude at 40N, due east


def test_no_receiver_location():
    rows = hm.decode(sample(), None)
    assert len(rows) == 3 and all(r[COL["distance_nm"]] is None and r[COL["bearing"]] is None for r in rows)
