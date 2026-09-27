# skyledger

A ledger of everything your ADS-B receiver has heard.

skyledger sits next to an existing [readsb](https://github.com/wiedehopf/readsb) + [tar1090](https://github.com/wiedehopf/tar1090) receiver (ultrafeeder, adsb.im, or a plain install) and archives it in TimescaleDB, with Grafana dashboards on top:

- **Your receiver's own history, imported.** readsb's heatmap already keeps a replay of every aircraft every 15 to 30 seconds, but only tar1090's replay page can read it, and on ultrafeeder it's gone on the next container restart unless you mapped a volume. skyledger imports all of it (every day it can find, at one sample per aircraft per minute), then adds each new day nightly and keeps it forever.
- **Live positions** every 10 seconds, including what aircraft report beyond position: wind and temperature aloft, airspeeds, autopilot targets, emergency state, GPS integrity.
- **Flights**, derived from the history: a searchable flight log, flights per day, a weekday by hour grid, regulars, rare visitors, first-ever sightings.
- **Reception range**: an all-time outline of the furthest you've heard in every direction, with bad decodes filtered out, plus range by bearing and a coverage heatmap.
- **Receiver health**: signal, noise, strong signals, message rate, gain, and alerts when the receiver goes quiet or deaf, or GPS interference shows up.

Screenshots: see `docs/screenshots/` (taken from the built-in demo, which uses a fictional receiver).

## How it works

```
readsb / tar1090 (your receiver)
   │  TAR1090_URL=http://ultrafeeder/
   │    data/aircraft.json, stats.json ─────→ ingest  ──┐
   │    globe_history/.../heatmap/*.bin.ttf ─→ history ─┼─→ TimescaleDB ─→ Grafana
   │    aircraft database (db-*/…) ──────────→ history ─┘   (3 dashboards, alerts)
```

Four containers: `timescaledb` (TimescaleDB community edition, compressed), `ingest` (live), `history` (backfill, then nightly), `grafana`. skyledger only ever reads from your receiver. See [docs/architecture.md](docs/architecture.md).

## Requirements

- A readsb (wiedehopf's fork) + tar1090 receiver whose tar1090 page you can reach over HTTP.
- Docker with Compose v2, or a Kubernetes cluster and Helm (see [Kubernetes](#kubernetes)).
- For history: readsb's heatmap turned on (it is by default in ultrafeeder and adsb.im; see [Heatmap history](#heatmap-history)).
- An amd64 machine or a Raspberry Pi 4/5 (arm64). Put the database on an **SSD, not an SD card**: a database writes constantly and wears SD cards out.
- Disk: live data is roughly 130 MB a day before compression; history compresses to a few hundred MB a year. Plan for a few GB.

## Quick start

```bash
git clone https://github.com/D1srupt3d/skyledger.git
cd skyledger
cp .env.example .env
```

Edit `.env`: set `TAR1090_URL` and `TZ`, and fill in the three passwords (`openssl rand -hex 16` makes a good one). Then:

```bash
docker compose up -d
docker compose logs -f history
```

Open Grafana on `http://<this machine>:3000` (user `admin`, your `GRAFANA_ADMIN_PASSWORD`). Live data appears within seconds. The history container logs its backfill day by day; a year of history takes an hour or two, most of it a deliberate pause between downloads so your receiver isn't hammered.

### Try it without a receiver

```bash
TAR1090_URL=http://demo-feed:8080/ COMPOSE_PROFILES=grafana,demo docker compose up -d
```

(with the passwords set in `.env`). The demo feed invents a receiver in the middle of the North Atlantic with a week of history.

## Connecting to your receiver

`TAR1090_URL` is the address where your tar1090 map lives, **as seen from the skyledger containers**. It must end where `data/aircraft.json` starts.

| Setup | TAR1090_URL |
|---|---|
| ultrafeeder in Docker on the same machine | `http://ultrafeeder/` if skyledger joins ultrafeeder's Docker network (below); otherwise the host's address and ultrafeeder's published port |
| adsb.im | the address you open the map at, e.g. `http://192.168.1.50:8080/` |
| readsb + tar1090 installed on a Pi | `http://192.168.1.50/tar1090/` |

Check it from the skyledger machine: `curl <TAR1090_URL>data/aircraft.json` should print JSON.

To join ultrafeeder's network, find it with `docker network ls` and add a `compose.override.yml`:

```yaml
services:
  ingest: {networks: [default, adsb]}
  history: {networks: [default, adsb]}
networks:
  adsb:
    external: true
    name: ultrafeeder_default   # the name from docker network ls
```

For range, bearing and the outline, readsb needs your receiver location configured (it's in `receiver.json`; skyledger reads it at runtime and never stores it in a file).

## Heatmap history

skyledger imports readsb's heatmap files (`globe_history/YYYY/MM/DD/heatmap/*.bin.ttf`). If there are none, the history container says so in its log and live data still works.

- **ultrafeeder / adsb.im**: on by default (`READSB_ENABLE_HEATMAP`, every `READSB_HEATMAP_INTERVAL`=15 s). Without a volume for `/var/globe_history` it only survives until the container restarts, so skyledger only finds what's there now. Map `/var/globe_history` to disk if you want the receiver to keep its own copy too, and use `MAX_GLOBE_HISTORY` to cap it: skyledger keeps its copy forever.
- **readsb (wiedehopf) on a Pi**: add `--write-globe-history /var/globe_history --heatmap 30` to readsb's options (see tar1090's README), and make `/var/globe_history` writable by the `readsb` user.

## Configuration

All settings live in `.env` (Compose). The Helm chart sets them from its values instead (see [Kubernetes](#kubernetes)).

| Variable | Default | |
|---|---|---|
| `TAR1090_URL` | required | see above |
| `POSTGRES_PASSWORD`, `GRAFANA_DB_PASSWORD`, `GRAFANA_ADMIN_PASSWORD` | required | required for Compose; the Helm chart generates them or reads `existingSecret` |
| `POSTGRES_HOST` | `timescaledb` | (set by the Helm chart from values) database host |
| `POSTGRES_PORT` | `5432` | (set by the Helm chart from values) database port |
| `POSTGRES_USER` | `skyledger` | (set by the Helm chart from values) database user |
| `POSTGRES_DB` | `skyledger` | (set by the Helm chart from values) database name |
| `DATABASE_URL` | optional | a full `postgresql://` URL; overrides the `POSTGRES_*` parts (with the Helm chart, set it through `extraEnv`) |
| `TZ` | `UTC` | local days, busiest hour, when the nightly import runs |
| `COMPOSE_PROFILES` | `grafana` | (Compose only) remove for your own Grafana; add `demo` for the demo feed |
| `RETENTION_DAYS` | `90` | days of 10 s live positions (history is kept forever) |
| `HISTORY_AT` | `01:15` | nightly import time, local |
| `HISTORY_START` | `auto` | `YYYY-MM-DD` to skip older history |
| `GRAFANA_PORT` | `3000` | (Compose only) |
| `DB_BIND` | `127.0.0.1` | (Compose only) where the database port is published; `0.0.0.0` for a Grafana elsewhere |
| `DB_PORT` | `5432` | (Compose only) host port the database is published on |
| `SKYLEDGER_VERSION` | `0.3` | (Compose only) image tag |

## Using your own Grafana

1. Remove `COMPOSE_PROFILES=grafana` from `.env` (and set `DB_BIND=0.0.0.0` if Grafana runs on another machine).
2. Add the datasource from `charts/skyledger/grafana/datasources/skyledger.yml`, pointing `url` at this machine. Keep the uid `skyledger`: the dashboards refer to it. Grafana logs in as `skyledger_grafana`, which can only read.
3. Import the three dashboards from `charts/skyledger/grafana/dashboards/` (or point Grafana's provisioning at that folder), and the alert rules from `charts/skyledger/grafana/alerting/` if you want them.

Grafana 12 or newer is recommended (the reception map uses the Route layer, beta since 10.1).

## Kubernetes

The Helm chart installs the same stack as Compose: TimescaleDB, `ingest`,
`history`, and Grafana with the dashboards and alerts.

```bash
helm install skyledger oci://ghcr.io/d1srupt3d/charts/skyledger \
  --namespace skyledger --create-namespace \
  --set tar1090Url=http://ultrafeeder/ --set timezone=Europe/London
```

The notes it prints show how to open Grafana. Every setting is in the chart's
`values.yaml` (`helm show values oci://ghcr.io/d1srupt3d/charts/skyledger`).
Try it without a receiver: `--set demo.enabled=true` instead of `tar1090Url`.

**ArgoCD, Flux or `helm template`: set `existingSecret`.** The chart generates
its passwords once and reads them back on upgrades, which only works when Helm
talks to the cluster. Offline renders would generate new passwords every time.
Give it a Secret you manage with `POSTGRES_PASSWORD` (always),
`GRAFANA_DB_PASSWORD` (with the bundled database or Grafana) and
`GRAFANA_ADMIN_PASSWORD` (with the bundled Grafana).

**Your own Grafana:** `--set grafana.enabled=false`, then follow "Using your
own Grafana".

**Your own database:** `--set timescaledb.enabled=false --set
database.host=...`. skyledger then runs as a plain database owner. Before the
first start, a superuser on your database server runs:

```sql
CREATE ROLE skyledger LOGIN PASSWORD '...';
CREATE ROLE skyledger_grafana LOGIN PASSWORD '...';
CREATE DATABASE skyledger OWNER skyledger;
REVOKE CONNECT ON DATABASE skyledger FROM PUBLIC;
GRANT CONNECT ON DATABASE skyledger TO skyledger, skyledger_grafana;
\c skyledger
CREATE EXTENSION IF NOT EXISTS timescaledb;
```

`skyledger_grafana` gets read access to skyledger's tables and nothing else.
The chart's generated passwords can't match roles you created, so hand it
yours in a Secret (add `GRAFANA_DB_PASSWORD` and `GRAFANA_ADMIN_PASSWORD` if you
keep the bundled Grafana):

```bash
kubectl create namespace skyledger
kubectl -n skyledger create secret generic skyledger-db \
  --from-literal=POSTGRES_PASSWORD='...'
# then add to the helm install: --set existingSecret=skyledger-db
```

**Plain YAML:** `helm template skyledger oci://ghcr.io/d1srupt3d/charts/skyledger --set ...`.

**Uninstall** keeps the database volume and the generated Secret, so a
reinstall finds its data. The notes printed at install list the commands that
delete them for real (`helm get notes skyledger -n skyledger` shows them
again); for the release `skyledger` in namespace `skyledger`:

```bash
kubectl -n skyledger delete pvc data-skyledger-timescaledb-0
# only if the chart generated the Secret (no existingSecret):
kubectl -n skyledger delete secret skyledger
```

## Alerts

Four rules come provisioned: receiver stats stale, receiver decoding nothing, history import stale, and GPS degraded for many aircraft. They show as firing in Grafana, but nothing is sent anywhere until you add a contact point (Alerting, Contact points: email, Discord, ntfy, Telegram and so on) and route the `app=skyledger` alerts to it.

## Upgrading

```bash
git pull
docker compose pull
docker compose up -d
```

On Kubernetes: `helm upgrade skyledger oci://ghcr.io/d1srupt3d/charts/skyledger --namespace skyledger --reset-then-reuse-values` keeps your settings and picks up the new chart's defaults.

Database changes are applied automatically by the `migrate` container. Downgrades aren't supported. The dashboards are replaced on upgrade: to customise one, use Grafana's "Save as" and edit your copy.

## Troubleshooting

- **No history, and the log says "no heatmap history found"**: see [Heatmap history](#heatmap-history).
- **The dashboards are empty**: `docker compose logs ingest`. A "poll failed" line with a connection error means `TAR1090_URL` isn't reachable from the container.
- **No range, bearing or outline**: set your receiver location in readsb.
- **Compose says a variable is required**: fill it in `.env`.

## Development

```bash
uv sync
uv run pytest                         # unit tests
SKYLEDGER_TEST_DSN=postgresql://skyledger:test@localhost:5432/skyledger uv run pytest   # plus integration
uv run python tools/gen_dashboards.py # regenerate dashboards after editing the generator
```

The two integration test files need `SKYLEDGER_TEST_DSN` to be a superuser on an empty TimescaleDB (community edition), such as `timescale/timescaledb-ha`, whose bootstrap superuser is the `POSTGRES_USER` you start it with (`skyledger` in the DSN above, password `test`): the tenant test creates roles and databases. Chart tests need `helm` 4 on PATH (`uv run pytest tests/unit/test_chart.py`); CI also installs the chart on kind. CI runs everything, including a real TimescaleDB.

## Credits

skyledger reads data produced by [readsb](https://github.com/wiedehopf/readsb) and [tar1090](https://github.com/wiedehopf/tar1090) by wiedehopf, and the aircraft database tar1090 ships (maintained through [Mictronics](https://github.com/Mictronics/readsb) and [tar1090-db](https://github.com/wiedehopf/tar1090-db)). It contains none of their code, and fetches the database from your own receiver at runtime. skyledger is not affiliated with FlightAware, ADS-B Exchange, adsb.im or the authors of readsb and tar1090.

A hobby project, maintained on a best-effort basis. Issues and pull requests are welcome.

## License

[MIT](LICENSE)
