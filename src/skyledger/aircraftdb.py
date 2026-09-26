"""tar1090's aircraft database (registration, type, flags) and airline names, from the receiver itself."""

import logging
import re

from skyledger.feed import Feed

log = logging.getLogger(__name__)


def db_version(index_html):
    """tar1090's index page sets `databaseFolder = "db-<version>";`."""
    m = re.search(r'databaseFolder\s*=\s*"([^"]+)"', index_html)
    if not m:
        raise ValueError("databaseFolder not found in tar1090's index page")
    return m.group(1)


def flag_bits(flags):
    """tar1090's flags string, one character per bit ("10" military, "0001" LADD)."""
    return sum(1 << i for i, c in enumerate(flags or "") if c == "1")


def db_rows(shard, data):
    """One database shard -> aircraft_db rows. Keys are the hex minus the shard prefix."""
    return [
        ((shard + key).lower(), rec[0] or None, rec[1] or None, rec[3] or None, flag_bits(rec[2]))
        for key, rec in data.items() if key != "children"
    ]


def airline_rows(operators):
    return [(code, o.get("n") or None, o.get("c") or None, o.get("r") or None)
            for code, o in operators.items()]


def load(conn, feed: Feed):
    """Reload aircraft_db and airlines if tar1090's database version changed. Returns the version."""
    version = db_version(feed.get(feed.index).decode())
    row = conn.execute("SELECT value FROM db_meta WHERE key = 'aircraft_db_version'").fetchone()
    if row and row[0] == version:
        return version
    aircraft = []
    for shard in feed.get_json(feed.db(version, "files.js")):
        aircraft += db_rows(shard, feed.get_json(feed.db(version, f"{shard}.js")))
    airlines = airline_rows(feed.get_json(feed.db(version, "operators.js")))
    # One transaction: readers see the old database until the new one is complete.
    with conn.transaction(), conn.cursor() as cur:
        cur.execute("TRUNCATE aircraft_db, airlines")
        columns = "hex, registration, type_code, description, flags"
        with cur.copy(f"COPY aircraft_db ({columns}) FROM STDIN") as copy:
            for r in aircraft:
                copy.write_row(r)
        with cur.copy("COPY airlines (code, name, country, callsign) FROM STDIN") as copy:
            for r in airlines:
                copy.write_row(r)
        cur.execute("INSERT INTO db_meta VALUES ('aircraft_db_version', %s) "
                    "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", (version,))
    log.info("aircraft database %s: %d aircraft, %d airlines", version, len(aircraft), len(airlines))
    return version
