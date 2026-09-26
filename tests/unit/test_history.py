import datetime
from zoneinfo import ZoneInfo

import pytest

from skyledger import history
from skyledger.config import Settings
from skyledger.feed import NotFound

D = datetime.date
UTC = datetime.UTC
LONDON = ZoneInfo("Europe/London")


def test_next_run_later_today_and_tomorrow():
    at = datetime.time(1, 15)
    now = datetime.datetime(2026, 1, 10, 0, 30, tzinfo=UTC)
    assert history.next_run(now, at, UTC) == datetime.datetime(2026, 1, 10, 1, 15, tzinfo=UTC)
    now = datetime.datetime(2026, 1, 10, 1, 15, tzinfo=UTC)  # exactly at: next day
    assert history.next_run(now, at, UTC) == datetime.datetime(2026, 1, 11, 1, 15, tzinfo=UTC)


def test_next_run_uses_local_time():
    # 23:30 UTC on 2026-06-30 is 00:30 on 2026-07-01 in London (BST): next 01:15 is 00:15 UTC.
    now = datetime.datetime(2026, 6, 30, 23, 30, tzinfo=UTC)
    got = history.next_run(now, datetime.time(1, 15), LONDON)
    assert got.astimezone(UTC) == datetime.datetime(2026, 7, 1, 0, 15, tzinfo=UTC)


def test_next_run_across_dst_change():
    # Clocks go forward in London on 2026-03-29; 01:15 local that morning doesn't exist,
    # zoneinfo resolves it forwards. The run still lands on that day, once.
    now = datetime.datetime(2026, 3, 28, 12, 0, tzinfo=UTC)
    got = history.next_run(now, datetime.time(1, 15), LONDON)
    assert got.date() == D(2026, 3, 29)


def exists_between(first, last, gaps=()):
    return lambda d: first <= d <= last and d not in gaps


def test_find_first_day():
    y = D(2026, 9, 24)
    assert history.find_first_day(exists_between(D(2024, 12, 12), y), y) == D(2024, 12, 12)


def test_find_first_day_steps_over_gaps():
    y = D(2026, 9, 24)
    first = D(2025, 3, 1)
    gaps = {first + datetime.timedelta(days=k) for k in range(2, 9)}  # receiver off for a week, early on
    assert history.find_first_day(exists_between(first, y, gaps), y) == first


def test_find_first_day_none():
    assert history.find_first_day(lambda d: False, D(2026, 9, 24)) is None


def test_find_first_day_older_than_horizon():
    y = D(2026, 9, 24)
    assert history.find_first_day(lambda d: True, y, horizon_days=100) == y - datetime.timedelta(days=100)


class FakeFeed:
    def __init__(self, files):
        self.files = files

    def heatmap(self, day, half):
        return (day, half)

    def get(self, key):
        if key in self.files:
            return self.files[key]
        raise NotFound(key)


class NoDB:
    """import_day must not touch the database when it records nothing."""

    def transaction(self):
        raise AssertionError("wrote to the database")

    execute = transaction


@pytest.fixture
def settings():
    return Settings.from_env({"TAR1090_URL": "http://r/", "POSTGRES_PASSWORD": "x", "FETCH_PAUSE": "0"})


def test_whole_day_of_404_is_retried_not_recorded(settings):
    day = D(2026, 1, 5)
    assert history.import_day(NoDB(), FakeFeed({}), day, set(), None, settings, day) == (0, 48)


def test_already_imported_files_are_skipped(settings):
    day = D(2026, 1, 5)
    done = {f"{day:%Y/%m/%d}/heatmap/{h:02d}.bin.ttf" for h in range(48)}
    assert history.import_day(NoDB(), FakeFeed({}), day, done, None, settings, day) == (0, 0)
