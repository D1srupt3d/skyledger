"""Settings from environment variables, validated once at startup."""

import datetime
import os
from collections.abc import Mapping
from dataclasses import dataclass
from urllib.parse import quote
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError


class ConfigError(ValueError):
    """A setting is missing or invalid. The message names the variable."""


@dataclass(frozen=True)
class Settings:
    tar1090_url: str
    database_url: str
    tz: ZoneInfo
    retention_days: int = 90
    poll_seconds: float = 10.0
    fetch_pause: float = 0.2
    history_at: datetime.time = datetime.time(1, 15)
    history_start: datetime.date | None = None
    grafana_db_password: str | None = None

    @classmethod
    def from_env(cls, env: Mapping[str, str] | None = None) -> "Settings":
        env = os.environ if env is None else env

        def get(name, default=None):
            value = env.get(name, "").strip()
            return value if value else default

        url = get("TAR1090_URL")
        if not url:
            raise ConfigError("TAR1090_URL is required: the base URL of your tar1090, e.g. http://ultrafeeder/")
        if not url.startswith(("http://", "https://")):
            raise ConfigError(f"TAR1090_URL must start with http:// or https://, got {url!r}")
        # Everything else is joined onto it, so a missing slash would drop the last path segment.
        if not url.endswith("/"):
            url += "/"

        database_url = get("DATABASE_URL")
        if not database_url:
            password = get("POSTGRES_PASSWORD")
            if not password:
                raise ConfigError("POSTGRES_PASSWORD (or DATABASE_URL) is required")
            # safe="": quote() leaves "/" alone by default, which breaks a password containing one.
            database_url = (
                f"postgresql://{quote(get('POSTGRES_USER', 'skyledger'), safe='')}:{quote(password, safe='')}"
                f"@{get('POSTGRES_HOST', 'timescaledb')}:{get('POSTGRES_PORT', '5432')}"
                f"/{quote(get('POSTGRES_DB', 'skyledger'), safe='')}"
            )

        tz_name = get("TZ", "UTC")
        try:
            tz = ZoneInfo(tz_name)
        except (ZoneInfoNotFoundError, ValueError) as e:
            raise ConfigError(f"TZ {tz_name!r} is not a known time zone (e.g. Europe/London)") from e

        return cls(
            tar1090_url=url,
            database_url=database_url,
            tz=tz,
            retention_days=_number(get, "RETENTION_DAYS", 90, int, minimum=1),
            poll_seconds=_number(get, "POLL_SECONDS", 10.0, float, minimum=1),
            fetch_pause=_number(get, "FETCH_PAUSE", 0.2, float, minimum=0),
            history_at=_time(get("HISTORY_AT", "01:15")),
            history_start=_date(get("HISTORY_START")),
            grafana_db_password=get("GRAFANA_DB_PASSWORD"),
        )


def _number(get, name, default, kind, minimum):
    raw = get(name)
    if raw is None:
        return default
    try:
        value = kind(raw)
    except ValueError as e:
        raise ConfigError(f"{name} must be a number, got {raw!r}") from e
    if value < minimum:
        raise ConfigError(f"{name} must be at least {minimum}, got {raw!r}")
    return value


def _time(raw):
    try:
        return datetime.time.fromisoformat(raw)
    except ValueError as e:
        raise ConfigError(f"HISTORY_AT must be HH:MM, got {raw!r}") from e


def _date(raw):
    if raw is None or raw.lower() == "auto":
        return None
    try:
        return datetime.date.fromisoformat(raw)
    except ValueError as e:
        raise ConfigError(f"HISTORY_START must be YYYY-MM-DD or auto, got {raw!r}") from e
