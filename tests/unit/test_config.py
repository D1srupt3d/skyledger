import datetime

import pytest

from skyledger.config import ConfigError, Settings

BASE = {"TAR1090_URL": "http://receiver/tar1090", "POSTGRES_PASSWORD": "pw"}


def test_defaults_and_derived_values():
    s = Settings.from_env(BASE)
    assert s.tar1090_url == "http://receiver/tar1090/"  # trailing slash added
    assert s.database_url == "postgresql://skyledger:pw@timescaledb:5432/skyledger"
    assert s.tz.key == "UTC"
    assert s.retention_days == 90 and s.poll_seconds == 10.0 and s.fetch_pause == 0.2
    assert s.history_at == datetime.time(1, 15) and s.history_start is None


def test_password_is_url_quoted():
    s = Settings.from_env({**BASE, "POSTGRES_PASSWORD": "p@ss/word"})
    assert "p%40ss%2Fword@" in s.database_url


def test_database_url_wins():
    s = Settings.from_env({"TAR1090_URL": "http://r/", "DATABASE_URL": "postgresql://u:p@h/d"})
    assert s.database_url == "postgresql://u:p@h/d"


def test_overrides():
    s = Settings.from_env({**BASE, "TZ": "Europe/London", "RETENTION_DAYS": "30", "HISTORY_AT": "03:30",
                           "HISTORY_START": "2025-01-02", "POLL_SECONDS": "5"})
    assert s.tz.key == "Europe/London" and s.retention_days == 30 and s.poll_seconds == 5.0
    assert s.history_at == datetime.time(3, 30) and s.history_start == datetime.date(2025, 1, 2)


def test_history_start_auto():
    assert Settings.from_env({**BASE, "HISTORY_START": "auto"}).history_start is None


@pytest.mark.parametrize("env, message", [
    ({"POSTGRES_PASSWORD": "pw"}, "TAR1090_URL is required"),
    ({**BASE, "TAR1090_URL": "receiver/"}, "must start with http"),
    ({"TAR1090_URL": "http://r/"}, "POSTGRES_PASSWORD"),
    ({**BASE, "TZ": "Mars/Olympus"}, "TZ"),
    ({**BASE, "HISTORY_AT": "25:99"}, "HISTORY_AT"),
    ({**BASE, "RETENTION_DAYS": "ninety"}, "RETENTION_DAYS must be a number"),
    ({**BASE, "RETENTION_DAYS": "0"}, "RETENTION_DAYS must be at least 1"),
    ({**BASE, "HISTORY_START": "last year"}, "HISTORY_START"),
])
def test_invalid(env, message):
    with pytest.raises(ConfigError, match=message):
        Settings.from_env(env)


def test_blank_values_mean_default():
    assert Settings.from_env({**BASE, "TZ": "  ", "RETENTION_DAYS": ""}).retention_days == 90
