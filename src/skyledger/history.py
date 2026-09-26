"""History: import readsb's heatmap replay files, then keep the derived tables current.

Runs once at start and then daily at HISTORY_AT (local TZ). Each run imports every
file from the first history day through yesterday (UTC) that history_files doesn't
list yet, so the first run is the backfill, later runs pick up one day, and a
missed night catches up by itself.
"""

import datetime
import logging
import time

import psycopg

from skyledger import aircraftdb, heatmap, sql
from skyledger.feed import Feed, NotFound
from skyledger.ingest import ALIVE, READY

log = logging.getLogger(__name__)

HALF_HOURS = range(48)
# Half hours probed to decide whether a day has any history at all.
PROBES = (0, 12, 24, 36, 47)
# How far before a found day to look for earlier history across a gap.
LOOKBACK_DAYS = 14
RETRY_AFTER = 900  # seconds, after a failed run


def next_run(now, at, tz):
    """Next HISTORY_AT in local time strictly after `now` (aware datetime)."""
    local = now.astimezone(tz)
    candidate = datetime.datetime.combine(local.date(), at, tzinfo=tz)
    if candidate <= local:
        candidate = datetime.datetime.combine(local.date() + datetime.timedelta(days=1), at, tzinfo=tz)
    return candidate


def find_first_day(exists, yesterday, horizon_days=3650):
    """Earliest day with heatmap files, or None if there is no history.

    Binary search assumes history is mostly contiguous back from yesterday; the
    lookback afterwards steps over short gaps (a receiver that was off for a few days).
    """
    if not any(exists(yesterday - datetime.timedelta(days=d)) for d in range(3)):
        return None
    lo = yesterday - datetime.timedelta(days=horizon_days)
    if exists(lo):
        return lo
    hi = yesterday
    while (hi - lo).days > 1:
        mid = lo + datetime.timedelta(days=(hi - lo).days // 2)
        if exists(mid):
            hi = mid
        else:
            lo = mid
    while True:
        earlier = next((hi - datetime.timedelta(days=k) for k in range(1, LOOKBACK_DAYS + 1)
                        if exists(hi - datetime.timedelta(days=k))), None)
        if earlier is None:
            return hi
        hi = earlier


def day_exists(feed, day):
    for half in PROBES:
        try:
            feed.get(feed.heatmap(day, half))
            return True
        except NotFound:
            continue
    return False


def import_day(conn, feed, day, done, receiver, settings, first_day):
    """Import one UTC day's files not yet in history_files. Returns (imported, failed)."""
    fetched, failed = {}, 0
    for half in HALF_HOURS:
        path = f"{day:%Y/%m/%d}/heatmap/{half:02d}.bin.ttf"
        if path in done:
            continue
        try:
            fetched[path] = feed.get(feed.heatmap(day, half))
        except NotFound:
            fetched[path] = None
        except Exception as e:  # noqa: BLE001 - not recorded, so the next run retries it
            log.warning("%s: %r, retrying next run", path, e)
            failed += 1
        time.sleep(settings.fetch_pause)
    # A whole day of 404s looks like a broken web server rather than a receiver
    # outage: record nothing, so the next run asks again.
    if fetched and all(raw is None for raw in fetched.values()) and len(fetched) == len(HALF_HOURS):
        log.warning("%s: every file 404, retrying next run", day)
        return 0, failed + len(fetched)
    for path, raw in fetched.items():
        rows = heatmap.decode(raw, receiver) if raw else []  # 404: the receiver was down that half hour
        with conn.transaction(), conn.cursor() as cur:
            with cur.copy(f"COPY history_positions ({', '.join(heatmap.COLUMNS)}) FROM STDIN") as copy:
                for row in rows:
                    copy.write_row(row)
            cur.execute("INSERT INTO history_files (path, rows) VALUES (%s, %s)", (path, len(rows)))
    if fetched:
        # A local day spans two UTC days of files: refresh the day before too, which
        # completes the partial edge day left by the previous import. Never before the
        # first day: that local day would only ever hold a few hours.
        first = max(day - datetime.timedelta(days=1), first_day)
        conn.execute(sql.REFRESH_DAILY, {"tz": settings.tz.key, "first": first, "last": day})
        start = datetime.datetime.combine(day, datetime.time(), datetime.UTC)
        conn.execute(sql.UPDATE_OUTLINE, {"start": start, "end": start + datetime.timedelta(days=1)})
    return len(fetched), failed


def receiver_location(feed):
    rx = feed.get_json(feed.receiver)
    if "lat" in rx and "lon" in rx:
        return rx["lat"], rx["lon"]
    log.warning("receiver.json has no location: set it in readsb for range, bearing and the outline")
    return None


def meta(conn, key):
    row = conn.execute("SELECT value FROM db_meta WHERE key = %s", (key,)).fetchone()
    return row[0] if row else None


def run_once(conn, feed, settings, today=None):
    today = today or datetime.datetime.now(datetime.UTC).date()
    yesterday = today - datetime.timedelta(days=1)
    first_day = settings.history_start
    if first_day is None and meta(conn, "history_first_day"):
        first_day = datetime.date.fromisoformat(meta(conn, "history_first_day"))
    if first_day is None:
        log.info("looking for the oldest heatmap history on the receiver")
        first_day = find_first_day(lambda d: day_exists(feed, d), yesterday)
        if first_day is None:
            log.warning("no heatmap history found at %s. Enable readsb's heatmap (--heatmap 30 with "
                        "--write-globe-history; ultrafeeder: see the README). Live data still works; "
                        "checking again next run.", feed.url("globe_history/"))
            return {"imported": 0, "failed": 0}
        conn.execute("INSERT INTO db_meta VALUES ('history_first_day', %s) "
                     "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", (first_day.isoformat(),))
        log.info("history starts %s (%d days)", first_day, (yesterday - first_day).days + 1)

    receiver = receiver_location(feed)
    done = {r[0] for r in conn.execute("SELECT path FROM history_files")}
    if done and not conn.execute("SELECT 1 FROM history_outline LIMIT 1").fetchone():
        conn.execute(sql.UPDATE_OUTLINE, {"start": "-infinity", "end": "infinity"})

    imported = failed = 0
    total = (yesterday - first_day).days + 1
    day = first_day
    while day <= yesterday:
        ALIVE.touch()
        n, f = import_day(conn, feed, day, done, receiver, settings, first_day)
        if n:
            log.info("%s: %d files (day %d of %d)", day, n, (day - first_day).days + 1, total)
        imported, failed = imported + n, failed + f
        day += datetime.timedelta(days=1)

    # The aircraft db load is long and uninterruptible: start it with a fresh liveness window.
    ALIVE.touch()
    try:
        aircraftdb.load(conn, feed)
    except Exception as e:  # noqa: BLE001 - types and operators are nice to have, never block history
        log.warning("aircraft database not loaded: %r", e)
    populated = conn.execute(
        "SELECT ispopulated FROM pg_matviews WHERE matviewname = 'flights'").fetchone()[0]
    if imported or not populated:
        started = time.monotonic()
        # The rebuild is the longest single step: the liveness window starts fresh here.
        ALIVE.touch()
        conn.execute(sql.REFRESH_FLIGHTS)
        log.info("flights rebuilt in %.0fs", time.monotonic() - started)
    log.info("history run done: %d files imported, %d to retry", imported, failed)
    return {"imported": imported, "failed": failed}


def sleep_until(wake, now=lambda: datetime.datetime.now(datetime.UTC)):
    """Sleep until `wake`, touching ALIVE every minute so a liveness probe can tell sleeping from hung.

    A test's `now` must advance (from its fake sleep) or this never returns.
    """
    while (left := (wake - now()).total_seconds()) > 0:
        ALIVE.touch()
        time.sleep(min(left, 60))


def run(settings):
    feed = Feed(settings.tar1090_url)
    while True:
        try:
            with psycopg.connect(settings.database_url, autocommit=True, connect_timeout=10) as conn:
                run_once(conn, feed, settings)
                READY.touch()
            wake = next_run(datetime.datetime.now(datetime.UTC), settings.history_at, settings.tz)
        except Exception as e:  # noqa: BLE001 - a failed run is retried, the service keeps going
            log.exception("history run failed: %r", e)
            wake = datetime.datetime.now(datetime.UTC) + datetime.timedelta(seconds=RETRY_AFTER)
        log.info("next history run at %s", wake.isoformat(timespec="minutes"))
        sleep_until(wake)
