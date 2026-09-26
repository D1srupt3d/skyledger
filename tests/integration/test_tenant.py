"""skyledger as a non-superuser tenant of a shared TimescaleDB (the Helm chart with
timescaledb.enabled=false).

SKYLEDGER_TEST_DSN must be a superuser: this test plays the platform too,
creating the tenant's owner, database and extension the way a shared server's
setup job would.
"""

import os
from zoneinfo import ZoneInfo

import psycopg
import pytest
from psycopg.conninfo import make_conninfo

from skyledger import ingest, migrate
from skyledger.config import Settings
from skyledger.feed import Feed

DSN = os.environ.get("SKYLEDGER_TEST_DSN")
pytestmark = [pytest.mark.integration, pytest.mark.skipif(not DSN, reason="SKYLEDGER_TEST_DSN not set")]


def dsn(user, password, db):
    return make_conninfo(DSN, user=user, password=password, dbname=db)


# The three tests run in order: test_2 and test_3 rely on test_1's migrate and ingest.
@pytest.fixture(scope="module")
def tenant():
    with psycopg.connect(DSN, autocommit=True) as admin:
        for stmt in [
            "DROP DATABASE IF EXISTS tenant_test WITH (FORCE)",
            "DROP DATABASE IF EXISTS other_tenant WITH (FORCE)",
            "DROP ROLE IF EXISTS tenant_owner",
            "CREATE ROLE tenant_owner LOGIN PASSWORD 'owner-test'",
            "DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'skyledger_grafana') THEN "
            "CREATE ROLE skyledger_grafana; END IF; END $$",
            "ALTER ROLE skyledger_grafana LOGIN PASSWORD 'grafana-test'",
            "CREATE DATABASE tenant_test OWNER tenant_owner",
            "CREATE DATABASE other_tenant",
            "REVOKE CONNECT ON DATABASE tenant_test FROM PUBLIC",
            "REVOKE CONNECT ON DATABASE other_tenant FROM PUBLIC",
            "GRANT CONNECT ON DATABASE tenant_test TO tenant_owner, skyledger_grafana",
        ]:
            admin.execute(stmt)
    with psycopg.connect(make_conninfo(DSN, dbname="tenant_test"), autocommit=True) as c:
        c.execute("CREATE EXTENSION IF NOT EXISTS timescaledb")


def test_1_migrate_and_ingest_as_tenant_owner(tenant, feed_url):
    settings = Settings(tar1090_url=feed_url, database_url=dsn("tenant_owner", "owner-test", "tenant_test"),
                        tz=ZoneInfo("UTC"))
    with psycopg.connect(settings.database_url, autocommit=True) as c:
        migrate.apply(c, settings)
        migrate.apply(c, settings)
        ingest.poll_once(c, Feed(feed_url), settings, with_stats=True)
        # Dashboards read compressed chunks too; grants must reach them.
        c.execute("SELECT compress_chunk(ch) FROM show_chunks('positions') ch")


def test_2_grafana_reads_its_tenant_only(tenant):
    with psycopg.connect(dsn("skyledger_grafana", "grafana-test", "tenant_test"), autocommit=True) as g:
        assert g.execute("SELECT count(*) FROM positions").fetchone()[0] > 0
        assert g.execute("SELECT count(*) FROM receiver_stats").fetchone()[0] == 1
        assert g.execute("SELECT has_table_privilege('flights', 'SELECT')").fetchone()[0]
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            g.execute("DELETE FROM positions")
    with pytest.raises(psycopg.OperationalError, match="permission denied"):
        psycopg.connect(dsn("skyledger_grafana", "grafana-test", "other_tenant"))


def test_3_grafana_is_not_a_server_wide_reader(tenant):
    with psycopg.connect(DSN, autocommit=True) as admin:
        assert not admin.execute(
            "SELECT pg_has_role('skyledger_grafana', 'pg_read_all_data', 'MEMBER')").fetchone()[0]
