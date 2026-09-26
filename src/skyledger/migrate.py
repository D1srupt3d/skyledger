"""Apply numbered SQL migrations, then the runtime settings that come from the environment."""

import importlib.resources
import logging
import re

import psycopg
from psycopg import sql

log = logging.getLogger(__name__)

NAME = re.compile(r"^(\d{4})_[a-z0-9_]+\.sql$")
# Any constant works; it only has to be the same for every skyledger process.
LOCK_ID = 0x5C71ED6E


def discover(names=None):
    """Migration files as [(version, name)], sorted. Rejects bad names and duplicate versions."""
    folder = importlib.resources.files("skyledger") / "migrations"
    if names is None:
        names = [p.name for p in folder.iterdir() if p.name.endswith(".sql")]
    found = {}
    for name in names:
        m = NAME.match(name)
        if not m:
            raise ValueError(f"bad migration file name {name!r}: want NNNN_description.sql")
        version = int(m.group(1))
        if version in found:
            raise ValueError(f"two migrations with version {version}: {found[version]}, {name}")
        found[version] = name
    return sorted(found.items())


def read(name):
    return (importlib.resources.files("skyledger") / "migrations" / name).read_text()


def apply(conn, settings):
    # Session-level lock: ingest and history both start after migrate, but a
    # user running `skyledger migrate` by hand at the same time must wait.
    conn.execute("SELECT pg_advisory_lock(%s)", (LOCK_ID,))
    try:
        conn.execute("CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, "
                     "name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())")
        applied = {r[0] for r in conn.execute("SELECT version FROM schema_migrations")}
        for version, name in discover():
            if version in applied:
                continue
            log.info("applying migration %s", name)
            with conn.transaction():
                conn.execute(read(name))
                conn.execute("INSERT INTO schema_migrations (version, name) VALUES (%s, %s)", (version, name))
        apply_settings(conn, settings)
    finally:
        conn.execute("SELECT pg_advisory_unlock(%s)", (LOCK_ID,))


def set_meta(conn, key, value):
    conn.execute("INSERT INTO db_meta VALUES (%s, %s) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value",
                 (key, str(value)))


def apply_settings(conn, settings):
    with conn.transaction():
        # Dashboards bucket by local day through this value, so no time zone is hard-coded in them.
        set_meta(conn, "tz", settings.tz.key)
        current = conn.execute("SELECT value FROM db_meta WHERE key = 'retention_days'").fetchone()
        if current is None or int(current[0]) != settings.retention_days:
            conn.execute("SELECT remove_retention_policy('positions', if_exists => true)")
            conn.execute("SELECT add_retention_policy('positions', drop_after => make_interval(days => %s))",
                         (settings.retention_days,))
            set_meta(conn, "retention_days", settings.retention_days)
            log.info("live position retention set to %d days", settings.retention_days)
        if settings.grafana_db_password:
            # Utility statements can't take bind parameters; Literal quotes it safely. Never logged.
            conn.execute(sql.SQL("ALTER ROLE skyledger_grafana LOGIN PASSWORD {}").format(
                sql.Literal(settings.grafana_db_password)))


def run(settings):
    with psycopg.connect(settings.database_url, autocommit=True, connect_timeout=10) as conn:
        # psycopg drops server notices without a handler; 0002 warns through one.
        conn.add_notice_handler(lambda d: log.log(
            logging.WARNING if d.severity_nonlocalized == "WARNING" else logging.DEBUG,
            "postgres: %s", d.message_primary))
        apply(conn, settings)
    log.info("schema up to date")
    return 0
