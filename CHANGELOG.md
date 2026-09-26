# Changelog

## 0.2.0

- Kubernetes: example manifests in `deploy/kubernetes/`, for a TimescaleDB you already run.
- skyledger no longer needs a superuser: it runs as the owner of its own database.
- The Grafana role reads skyledger's tables only. It used to read every database on the server; upgrading removes that.
- The history service writes heartbeat files for container health checks.

## 0.1.0

First release.

- Live ingest of positions (with weather, autopilot, emergency and GPS integrity fields) and receiver statistics.
- Import of readsb's heatmap replay history, backfilled then kept current nightly.
- Flights, daily statistics, an all-time reception outline and tar1090's aircraft database.
- Three Grafana dashboards and four alert rules.
- A demo feed for trying skyledger without a receiver.
