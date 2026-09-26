"""A fake tar1090 serving synthetic, deterministic data: for tests, screenshots and trying skyledger.

Fictional aircraft fly repeating straight tracks across a made-up receiver in the middle
of the North Atlantic. Everything is computed from the clock, so any two requests for the
same moment agree, and the heatmap history covers the last DEMO_DAYS days.
"""

import datetime
import http.server
import json
import logging
import math
import os
import re

from skyledger import heatmap_writer as w

log = logging.getLogger(__name__)

RECEIVER = (45.0, -35.0)
DB_VERSION = "db-demo-1"
DEMO_DAYS = int(os.environ.get("DEMO_DAYS", "7"))
HALF_HOUR = 1800


class Plane:
    """Flies from `bearing` at `radius` nm, straight through near the receiver, and repeats."""

    def __init__(
        self, index, callsign, bearing, radius, alt, speed, offset_min, mil=False, squawk=2000, miss_nm=0.0
    ):
        self.hex = 0xDE0000 + index
        self.callsign, self.alt, self.speed, self.mil, self.squawk = callsign, alt, speed, mil, squawk
        self.start = _destination(*RECEIVER, bearing, radius)
        # Aim `miss_nm` to the side of the receiver, so closest approaches differ.
        aim = _destination(*RECEIVER, (bearing + 90) % 360, miss_nm)
        self.end = (2 * aim[0] - self.start[0], 2 * aim[1] - self.start[1])
        self.period = 2 * radius / speed * 3600
        self.offset = offset_min * 60

    def at(self, t):
        f = ((t - self.offset) % self.period) / self.period
        return (
            self.start[0] + (self.end[0] - self.start[0]) * f,
            self.start[1] + (self.end[1] - self.start[1]) * f,
        )


def _destination(lat, lon, bearing, nm):
    d = nm / 3440.065
    p1, l1, b = math.radians(lat), math.radians(lon), math.radians(bearing)
    p2 = math.asin(math.sin(p1) * math.cos(d) + math.cos(p1) * math.sin(d) * math.cos(b))
    l2 = l1 + math.atan2(math.sin(b) * math.sin(d) * math.cos(p1), math.cos(d) - math.sin(p1) * math.sin(p2))
    return math.degrees(p2), math.degrees(l2)


PLANES = [
    Plane(i, cs, brg, rad, alt, spd, off, mil=mil, squawk=sq, miss_nm=miss)
    for i, (cs, brg, rad, alt, spd, off, mil, sq, miss) in enumerate(
        [
            ("DMO101", 20, 240, 37000, 480, 0, False, 2101, 30),
            ("DMO202", 70, 220, 35000, 460, 9, False, 2202, 5),
            ("DMO303", 110, 200, 39000, 500, 17, False, 2303, 60),
            ("DMO404", 160, 250, 33000, 450, 26, False, 2404, 15),
            ("DMO505", 200, 230, 36000, 470, 35, False, 2505, 45),
            ("DMO606", 250, 260, 38000, 490, 44, False, 2606, 8),
            ("DMO707", 290, 210, 34000, 455, 53, False, 2707, 70),
            ("DMO808", 330, 245, 40000, 505, 61, False, 2808, 20),
            ("DEMOX1", 45, 120, 3000, 160, 4, False, 7000, 1),  # low and slow: "lowest aircraft"
            ("RCH99", 135, 180, 28000, 420, 21, True, 4521, 25),  # military flag
            ("DMO911", 300, 200, 31000, 440, 38, False, 7700, 35),  # squawking 7700
        ],
        start=1,
    )
]


def aircraft_json(now):
    aircraft = []
    for p in PLANES:
        lat, lon = p.at(now)
        a = {
            "hex": f"{p.hex:06x}",
            "type": "adsb_icao",
            "flight": f"{p.callsign:<8}",
            "lat": round(lat, 6),
            "lon": round(lon, 6),
            "alt_baro": p.alt,
            "alt_geom": p.alt + 400,
            "gs": p.speed,
            "track": 90.0,
            "seen_pos": 0.4,
            "seen": 0.2,
            "rssi": -20.0,
            "squawk": f"{p.squawk:04d}",
            "emergency": "general" if p.squawk == 7700 else "none",
            "nic": 8,
            "nac_p": 10,
            "r": f"DEMO-{p.hex & 0xFF:02d}",
            "t": "B38M" if p.alt > 20000 else "C172",
            "desc": "DEMO JETLINER" if p.alt > 20000 else "DEMO LIGHT AIRCRAFT",
            "ownOp": "DEMO AIR",
            "category": "A3" if p.alt > 20000 else "A1",
        }
        if p.mil:
            a["dbFlags"] = 1
        if p.alt >= 20000:
            # Weather aloft: a westerly jet that strengthens with altitude, cold air.
            a.update(
                {
                    "wd": 270,
                    "ws": 40 + p.alt // 1000,
                    "oat": -56,
                    "tat": -30,
                    "ias": 280,
                    "tas": p.speed,
                    "mach": 0.78,
                    "roll": 0.0,
                    "nav_altitude_mcp": p.alt,
                    "nav_modes": ["autopilot", "lnav"],
                    "nav_qnh": 1013.2,
                }
            )
        aircraft.append(a)
    return {"now": now, "messages": int(now) % 10_000_000, "aircraft": aircraft}


def stats_json(now):
    end = now - now % 60
    return {
        "now": now,
        "gain_db": 42.1,
        "estimated_ppm": -1.5,
        "aircraft_with_pos": len(PLANES),
        "aircraft_without_pos": 2,
        "last1min": {
            "start": end - 60,
            "end": end,
            "messages": 600000,
            "messages_valid": 18000,
            "position_count_total": 2100,
            "max_distance": 240 * 1852,
            "local": {
                "signal": -12.0,
                "noise": -32.0,
                "peak_signal": -1.5,
                "strong_signals": 700,
                "samples_dropped": 0,
                "samples_lost": 0,
            },
        },
    }


def heatmap_file(day, half):
    start = datetime.datetime.combine(day, datetime.time(), datetime.UTC).timestamp() + half * HALF_HOUR
    slices = []
    for k in range(120):  # 15 s slices
        t = start + k * 15
        records = []
        for p in PLANES:
            if k % 4 == 0:  # callsign records about once a minute, like readsb
                records.append(w.callsign(p.hex, p.callsign, p.squawk))
            lat, lon = p.at(t)
            records.append(w.position(p.hex, lat, lon, alt_ft=p.alt, gs_kt=p.speed))
        slices.append((int(t * 1000), records))
    return w.build(slices)


def db_files():
    shard = {
        f"{p.hex:06X}"[2:]: [
            f"DEMO-{p.hex & 0xFF:02d}",
            "B38M" if p.alt > 20000 else "C172",
            "10" if p.mil else "00",
            "DEMO JETLINER" if p.alt > 20000 else "DEMO LIGHT AIRCRAFT",
        ]
        for p in PLANES
    }
    return {
        "files.js": ["DE"],
        "DE.js": shard,
        "operators.js": {
            "DMO": {"n": "Demo Air", "c": "Nowhere", "r": "DEMO"},
            "RCH": {"n": "Demo Air Force", "c": "Nowhere", "r": "REACH"},
        },
    }


HEATMAP = re.compile(r"^/globe_history/(\d{4})/(\d{2})/(\d{2})/heatmap/(\d{2})\.bin\.ttf$")


def respond(path, now):
    """(status, content_type, body) for a request path at time `now`."""
    if path == "/":
        return 200, "text/html", f'<script>databaseFolder = "{DB_VERSION}";</script>'.encode()
    if path == "/data/aircraft.json":
        return 200, "application/json", json.dumps(aircraft_json(now)).encode()
    if path == "/data/stats.json":
        return 200, "application/json", json.dumps(stats_json(now)).encode()
    if path == "/data/receiver.json":
        return (
            200,
            "application/json",
            json.dumps({"lat": RECEIVER[0], "lon": RECEIVER[1], "version": "skyledger demo"}).encode(),
        )
    if path.startswith(f"/{DB_VERSION}/"):
        files = db_files()
        name = path.rsplit("/", 1)[1]
        if name in files:
            return 200, "application/javascript", json.dumps(files[name]).encode()
    m = HEATMAP.match(path)
    if m:
        y, mo, d, half = map(int, m.groups())
        day = datetime.date(y, mo, d)
        start = datetime.datetime.combine(day, datetime.time(), datetime.UTC).timestamp() + half * HALF_HOUR
        today = datetime.datetime.fromtimestamp(now, datetime.UTC).date()
        # Only finished half hours within the last DEMO_DAYS days exist, like a real receiver.
        if half < 48 and start + HALF_HOUR <= now and (today - day).days <= DEMO_DAYS:
            return 200, "application/octet-stream", heatmap_file(day, half)
    return 404, "text/plain", b"not found"


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        now = datetime.datetime.now(datetime.UTC).timestamp()
        status, ctype, body = respond(self.path.split("?")[0], now)
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def server(port=0):
    return http.server.ThreadingHTTPServer(("0.0.0.0", port), Handler)


def serve(port):
    srv = server(port)
    log.info(
        "demo tar1090 on :%d (receiver at %s, %d days of history)", srv.server_address[1], RECEIVER, DEMO_DAYS
    )
    srv.serve_forever()
    return 0
