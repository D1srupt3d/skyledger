"""End to end against a real TimescaleDB (community edition) and the demo feed.

Needs SKYLEDGER_TEST_DSN pointing at an EMPTY database, e.g. the CI service container:
postgresql://postgres:test@localhost:5432/skyledger
"""

import datetime
import importlib.util
import os
import pathlib
import re
from zoneinfo import ZoneInfo

import psycopg
import pytest

from skyledger import demo, history, ingest, migrate
from skyledger.config import Settings
from skyledger.feed import Feed

DSN = os.environ.get("SKYLEDGER_TEST_DSN")
pytestmark = [pytest.mark.integration, pytest.mark.skipif(not DSN, reason="SKYLEDGER_TEST_DSN not set")]
ROOT = pathlib.Path(__file__).resolve().parents[2]


@pytest.fixture(scope="module")
def settings(feed_url):
    return Settings(tar1090_url=feed_url, database_url=DSN, tz=ZoneInfo("Europe/London"), fetch_pause=0,
                    retention_days=30, grafana_db_password="grafana-test")


@pytest.fixture(scope="module")
def conn(settings):
    with psycopg.connect(DSN, autocommit=True) as c:
        yield c


@pytest.fixture(scope="module")
def grafana_conn():
    with psycopg.connect(re.sub(r"//[^@]*@", "//skyledger_grafana:grafana-test@", DSN), autocommit=True) as g:
        yield g


def count(conn, sql):
    return conn.execute(sql).fetchone()[0]


def test_1_migrate_is_idempotent(conn, settings):
    # What the old 0001 left behind; 0002 must take it away again.
    conn.execute("DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'skyledger_grafana') THEN "
                 "CREATE ROLE skyledger_grafana; END IF; END $$")
    conn.execute("GRANT pg_read_all_data TO skyledger_grafana")
    migrate.apply(conn, settings)
    migrate.apply(conn, settings)
    assert not count(conn, "SELECT pg_has_role('skyledger_grafana', 'pg_read_all_data', 'MEMBER')")
    assert count(conn, "SELECT count(*) FROM schema_migrations") == len(migrate.discover())
    assert count(conn, "SELECT value FROM db_meta WHERE key = 'tz'") == "Europe/London"
    jobs = conn.execute("SELECT hypertable_name, proc_name FROM timescaledb_information.jobs "
                        "WHERE hypertable_name IS NOT NULL").fetchall()
    names = {h for h, _ in jobs}
    assert {"positions", "history_positions", "receiver_stats"} <= names, jobs
    assert any(h == "positions" and "retention" in p for h, p in jobs)


def test_2_ingest(conn, settings):
    feed = Feed(settings.tar1090_url)
    assert ingest.poll_once(conn, feed, settings, with_stats=True) == len(demo.PLANES)
    assert count(conn, "SELECT count(*) FROM positions") == len(demo.PLANES)
    assert count(conn, "SELECT count(*) FROM receiver_stats") == 1
    assert count(conn, "SELECT count(*) FROM aircraft WHERE db_flags & 1 <> 0") == 1


def test_3_history(conn, settings):
    feed = Feed(settings.tar1090_url)
    result = history.run_once(conn, feed, settings)
    days = demo.DEMO_DAYS
    assert result == {"imported": 48 * days, "failed": 0}
    assert count(conn, "SELECT count(*) FROM history_files") == 48 * days
    assert count(conn, "SELECT count(*) FROM history_positions") == 48 * days * 30 * len(demo.PLANES)
    assert count(conn, "SELECT count(*) FROM daily_stats") >= days
    assert count(conn, "SELECT count(*) FROM history_outline") > 20
    assert count(conn, "SELECT count(*) FROM flights") > 0
    assert count(conn, "SELECT count(*) FROM aircraft_db") == len(demo.PLANES)
    assert count(conn, "SELECT count(*) FROM airlines") == 2
    # A second run finds nothing new.
    assert history.run_once(conn, feed, settings) == {"imported": 0, "failed": 0}


def test_4_grafana_role_is_read_only(grafana_conn):
    assert count(grafana_conn, "SELECT count(*) FROM flights") > 0
    with pytest.raises(psycopg.errors.InsufficientPrivilege):
        grafana_conn.execute("DELETE FROM positions")


def load_generator():
    spec = importlib.util.spec_from_file_location("gen_dashboards", ROOT / "tools" / "gen_dashboards.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_5_every_dashboard_query_runs(grafana_conn):
    now = datetime.datetime.now(datetime.UTC)
    frm = (now - datetime.timedelta(days=30)).isoformat()
    queries = load_generator().all_queries()
    assert len(queries) > 30
    for name, sql in queries.items():
        e = re.sub(r"\$__timeFilter\(([\w.]+)\)", rf"\1 BETWEEN '{frm}' AND '{now.isoformat()}'", sql)
        group = r"time_bucket('5 minutes', \1) AS time"
        e = re.sub(r"\$__timeGroupAlias\(([\w.]+), *\$__interval\)", group, e)
        e = e.replace("$__timeFrom()", f"'{frm}'").replace("$__timeTo()", f"'{now.isoformat()}'")
        assert "$__" not in e, (name, e)
        grafana_conn.execute(e).fetchall()


def test_6_every_alert_query_runs(grafana_conn):
    import yaml

    alerting = yaml.safe_load((ROOT / "grafana/provisioning/alerting/skyledger.yml").read_text())
    rules = alerting["groups"][0]["rules"]
    assert len(rules) == 4
    for rule in rules:
        grafana_conn.execute(rule["data"][0]["model"]["rawSql"]).fetchall()
