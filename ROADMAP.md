# Roadmap

Ideas, not promises. Contributions welcome: open an issue first for anything large.

## Planned

### Backup and restore docs

History is kept forever, but the database is often the only copy: on ultrafeeder without a volume, the receiver's heatmap files are gone on restart. The README should cover backing up and restoring for Compose and for the Helm chart, including TimescaleDB's restore steps (`timescaledb_pre_restore()` / `timescaledb_post_restore()`, same TimescaleDB version on both sides).

## Low priority

### More than one receiver

skyledger archives exactly one receiver (`TAR1090_URL`). Supporting several would mean:

- a receiver id on the per-receiver tables (`positions`, `receiver_stats`, `history_positions`, `history_files`, `daily_stats`, `history_outline`) and on the `flights` view, in a new migration;
- `ingest` and `history` polling a list of URLs instead of one;
- a receiver variable on the dashboards and alerts.

`aircraft_db` and `airlines` are shared and wouldn't change. The maintainer runs a single adsb.im receiver, so this waits for someone who needs it.
