"""Build heatmap replay files. For tests and the demo feed; skyledger itself only reads them."""

import struct

from skyledger.heatmap import MAGIC


def _s32(v):
    v &= 0xFFFFFFFF
    return v - (1 << 32) if v >= 1 << 31 else v


def record(hx, lat, lon, alt=0, gs=0):
    return struct.pack("<iiihh", _s32(hx), lat, lon, alt, gs)


def marker(ms, interval_ms=15000):
    return record(MAGIC, _s32(ms >> 32), _s32(ms), interval_ms, 0)


def position(addr, lat, lon, alt_ft=None, gs_kt=None, on_ground=False, source=0):
    """A position record. addr: 24-bit int, plus 1 << 24 for non-ICAO. source: readsb addrtype."""
    alt = -123 if on_ground else (-124 if alt_ft is None else round(alt_ft / 25))
    gs = -1 if gs_kt is None else round(gs_kt * 10)
    return record(addr | (source << 27), round(lat * 1e6), round(lon * 1e6), alt, gs)


def callsign(addr, name, squawk):
    lon, alt, gs = struct.unpack("<ihh", name.encode().ljust(8, b"\0")[:8])
    return record(addr, (1 << 30) | squawk, lon, alt, gs)


def build(slices, interval_ms=15000):
    """slices: list of (start_ms, [records]). Returns file bytes: index, then marker + records per slice."""
    index, body, offset = [], [], len(slices)
    for start_ms, records in slices:
        index.append(record(offset, 0, 0))
        body.append(marker(start_ms, interval_ms))
        body.extend(records)
        offset += 1 + len(records)
    return b"".join(index + body)
