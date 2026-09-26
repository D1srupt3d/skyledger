"""Decoder for readsb's heatmap replay files (what tar1090's history replay reads).

Format, little-endian 16-byte records `int32 hex, int32 lat, int32 lon, int16 alt, int16 gs`:
leading index records; slice markers (`hex == MAGIC`, ms timestamp in lat/lon words);
callsign records (`lat >= 2**30`); positions otherwise. Writer: readsb globe_index.c;
reader: tar1090 script.js. See docs/architecture.md.
"""

import datetime
import gzip
import math
import struct

MAGIC = 0xE7F7C9D
# readsb addrtype_t, in enum order: the same names aircraft.json uses for
# `type`, so history rows and live rows share `source` values.
SOURCES = (
    "adsb_icao", "adsb_icao_nt", "adsr_icao", "tisb_icao", "adsc", "mlat", "other",
    "mode_s", "adsb_other", "adsr_other", "tisb_trackfile", "tisb_other", "mode_ac", "unknown",
)
ALT_GROUND = -123
ALT_UNKNOWN = -124
EARTH_RADIUS_NM = 3440.065
COLUMNS = ("time", "hex", "flight", "squawk", "source", "lat", "lon", "alt_baro",
           "on_ground", "gs", "distance_nm", "bearing")


def range_and_bearing(rx_lat, rx_lon, lat, lon):
    """Great-circle distance (nm) and initial bearing (deg) from the receiver."""
    p1, p2 = math.radians(rx_lat), math.radians(lat)
    dl = math.radians(lon - rx_lon)
    a = math.sin((p2 - p1) / 2) ** 2 + math.cos(p1) * math.cos(p2) * math.sin(dl / 2) ** 2
    dist = 2 * EARTH_RADIUS_NM * math.asin(math.sqrt(a))
    y = math.sin(dl) * math.cos(p2)
    x = math.cos(p1) * math.sin(p2) - math.sin(p1) * math.cos(p2) * math.cos(dl)
    return dist, math.degrees(math.atan2(y, x)) % 360


def address(hex_field):
    addr = f"{hex_field & 0xFFFFFF:06x}"
    return "~" + addr if hex_field & (1 << 24) else addr


def decode(raw, receiver):
    """Heatmap file bytes -> rows in COLUMNS order, first sample per aircraft per minute."""
    if raw[:2] == b"\x1f\x8b":
        raw = gzip.decompress(raw)
    if len(raw) % 16:
        raise ValueError(f"not a heatmap file: {len(raw)} bytes")
    slice_ts = None
    latest_ident = {}   # hex -> (flight, squawk), updated as callsign records appear
    first_ident = {}    # hex -> first (flight, squawk), for positions before it
    seen_minutes = set()
    rows = []
    for hx, lat, lon, alt, gs in struct.iter_unpack("<iiihh", raw):
        if hx == MAGIC:
            slice_ts = ((lat & 0xFFFFFFFF) << 32 | (lon & 0xFFFFFFFF)) / 1000
            continue
        if slice_ts is None:
            continue  # index records before the first slice
        key = address(hx)
        # Southern latitudes are negative, so this can't match a position.
        if lat >= 1 << 30:
            name = struct.pack("<ihh", lon, alt, gs)
            flight = name.split(b"\0")[0].decode("ascii", "replace").strip() or None
            ident = (flight, f"{lat & 0xFFFF:04d}")
            latest_ident[key] = ident
            first_ident.setdefault(key, ident)
            continue
        minute = int(slice_ts // 60)
        if (key, minute) in seen_minutes:
            continue
        seen_minutes.add((key, minute))
        la, lo = lat / 1e6, lon / 1e6
        # No receiver location configured in readsb: keep the position, skip range and bearing.
        dist, bearing = range_and_bearing(receiver[0], receiver[1], la, lo) if receiver else (None, None)
        src = (hx & 0xFFFFFFFF) >> 27
        rows.append([
            datetime.datetime.fromtimestamp(slice_ts, datetime.UTC), key,
            None, None,  # flight, squawk: filled below
            SOURCES[src] if src < len(SOURCES) else "unknown",
            la, lo,
            None if alt in (ALT_GROUND, ALT_UNKNOWN) else alt * 25,
            alt == ALT_GROUND,
            None if gs == -1 else gs / 10,
            dist, bearing,
        ])
        rows[-1][2], rows[-1][3] = latest_ident.get(key, (None, None))
    # Positions seen before their aircraft's first callsign record in this file.
    for row in rows:
        if row[2] is None and row[3] is None and row[1] in first_ident:
            row[2], row[3] = first_ident[row[1]]
    return [tuple(r) for r in rows]
