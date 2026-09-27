# Changelog

## 0.3.1

- History dashboard: "Flights by weekday and hour" is now "Flights per hour, last 7 days": one row per date instead of every weekday folded together, and it always shows the last 7 days (it was blank most of the time on the dashboard's default 24 h range). Narrow columns, so all 24 hours fit without scrolling sideways.
- The two maps fit to all layers again. Fitting to one named layer (0.3.0) never works in Grafana 13; with all layers, re-selecting the view frames your area. The README's Troubleshooting has the details.

## 0.3.0

- Kubernetes: a Helm chart (`oci://ghcr.io/d1srupt3d/charts/skyledger`) that installs the whole stack, TimescaleDB and Grafana included; switch either off to bring your own.
- Breaking: `deploy/kubernetes/` is gone. Use the chart, or `helm template` for plain YAML.
- The Grafana provisioning files moved to `charts/skyledger/grafana/` (Compose mounts them from there; `git pull` is enough).
- The reception maps frame your reception area instead of every point ever heard.
- TimescaleDB runs with `max_locks_per_transaction=128`, for queries across many months of chunks.
- The "Receiver stats stale" (receiver down) and "History import stale" alert descriptions name both `docker compose logs` and `kubectl logs`.

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
