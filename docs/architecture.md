# Architecture

## Containers

| Service | Command | Runs |
|---|---|---|
| `timescaledb` | TimescaleDB community edition (`timescale/timescaledb-ha`) | always |
| `migrate` | `skyledger migrate` | once at start; the others wait for it |
| `ingest` | `skyledger ingest` | always: polls `aircraft.json` every 10 s, `stats.json` every minute |
| `history` | `skyledger history` | always: a run at start, then daily at `HISTORY_AT` |
| `grafana` | Grafana with provisioned datasource, dashboards and alerts | profile `grafana` |
| `demo-feed` | `skyledger demo-feed` | profile `demo` |

All skyledger containers use one image and differ only in the command. They only read from the receiver.

## Data

| Table | From | Kept |
|---|---|---|
| `positions` | `aircraft.json`: each position decoded since the last poll | `RETENTION_DAYS`, compressed after 7 days |
| `aircraft` | `aircraft.json`: per-airframe fields readsb looks up | forever |
| `receiver_stats` | `stats.json`, one row per minute | 1 year, compressed after 7 days |
| `history_positions` | heatmap files, one sample per aircraft per minute | forever, compressed after 7 days |
| `history_files` | one row per heatmap file attempted | forever |
| `daily_stats` | per local day from `history_positions` | forever |
| `aircraft_db`, `airlines` | tar1090's aircraft database on the receiver | replaced when its version changes |
| `history_outline` | furthest corroborated position per degree of bearing | forever, only ever extended |
| `flights` (materialized view) | `history_positions` split at 30-minute gaps | rebuilt after each import |
| `db_meta` | settings (`tz`, retention), first history day, aircraft database version | |

Migrations are numbered SQL files in `src/skyledger/migrations/`, applied in order and recorded in `schema_migrations`. Settings that come from the environment (live retention, the Grafana role's password, the time zone the dashboards use) are applied on every `migrate`, so editing `.env` takes effect on the next start.

## History import

Each run imports every file from the first history day through yesterday (UTC) that `history_files` doesn't list, so the first run is the backfill and later runs pick up one day; a missed night catches up. The first day is found once by probing the receiver (binary search, then stepping back over short gaps) and remembered in `db_meta`.

- A file's positions and its `history_files` row commit in one transaction: exactly once.
- A 404 is recorded as a real gap (the receiver was down that half hour), unless all 48 files of a day are 404, which looks like a broken web server and is retried instead.
- Other errors aren't recorded, so the next run retries them.
- After each day: `daily_stats` for that local day and the day before (a local day spans two UTC days of files), and `history_outline` extended with that day's positions.
- After the run: the aircraft database is reloaded if tar1090's version changed, and `flights` is rebuilt.

### Heatmap file format

readsb writes one file per UTC half hour (`globe_index.c`); tar1090 reads it (`script.js`). Little-endian 16-byte records, `int32 hex, int32 lat, int32 lon, int16 alt, int16 gs`:

- Leading index records, one per slice, until the first slice marker.
- Slice marker: `hex == 0xE7F7C9D`; the slice start in ms is `lat` (high 32 bits) and `lon` (low 32 bits) as unsigned; `alt` is the interval in ms.
- Callsign record: `lat >= 2^30`; the squawk is in the low 16 bits of `lat`, the callsign in the 8 bytes of `lon, alt, gs` (NUL padded).
- Position: address in the low 24 bits of `hex` (bit 24 set: non-ICAO, shown with `~`), readsb's address type in the top 5 bits; lat and lon in millionths of a degree; `alt` in 25 ft steps with -123 on the ground and -124 unknown; `gs` in tenths of a knot, -1 unknown.

The files may be served gzipped or plain; skyledger accepts both.

## Reception outline

A position counts toward the outline only if the same aircraft's previous or next position is within 10 minutes and reachable at under 800 kt. Bad decodes jump hundreds of nautical miles in a minute (thousands of knots) and would otherwise draw spikes; genuine long-range tracks pass. The all-time outline keeps, per whole degree of bearing, the furthest point that passes, and is only ever replaced by a further one.

## Dashboards

`tools/gen_dashboards.py` builds all panels once and assigns them by title to three dashboards (`skyledger-overview`, `skyledger-receiver`, `skyledger-history`). Local-time bucketing reads the configured time zone from `db_meta`, so nothing in the JSON is site-specific. Maps fit to their data; the coverage heatmap also carries the outline as a markers layer because Grafana can't compute an extent from a heatmap layer alone. CI fails if the committed JSON differs from what the generator produces.
