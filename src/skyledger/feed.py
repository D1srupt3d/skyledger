"""Everything skyledger reads from the receiver, derived from one base URL."""

import datetime
import gzip
import json
import urllib.error
import urllib.request
from urllib.parse import urljoin


class NotFound(Exception):
    """The receiver answered 404: the file doesn't exist (as opposed to an outage)."""


class Feed:
    def __init__(self, base_url: str, timeout: float = 30):
        self.base = base_url if base_url.endswith("/") else base_url + "/"
        self.timeout = timeout

    def url(self, path: str) -> str:
        return urljoin(self.base, path)

    @property
    def aircraft(self):
        return self.url("data/aircraft.json")

    @property
    def stats(self):
        return self.url("data/stats.json")

    @property
    def receiver(self):
        return self.url("data/receiver.json")

    @property
    def index(self):
        return self.base

    def heatmap(self, day: datetime.date, half: int) -> str:
        """tar1090 replay file for one UTC half hour (0-47)."""
        return self.url(f"globe_history/{day:%Y/%m/%d}/heatmap/{half:02d}.bin.ttf")

    def db(self, version: str, name: str) -> str:
        """A file in tar1090's aircraft database folder, e.g. db("db-3.14.1718", "files.js")."""
        return self.url(f"{version}/{name}")

    def get(self, url: str) -> bytes:
        """GET a URL. tar1090 serves some files gzipped whatever the client asks, so unwrap by magic."""
        try:
            with urllib.request.urlopen(url, timeout=self.timeout) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as e:
            if e.code == 404:
                raise NotFound(url) from e
            raise
        return gzip.decompress(raw) if raw[:2] == b"\x1f\x8b" else raw

    def get_json(self, url: str):
        return json.loads(self.get(url))
