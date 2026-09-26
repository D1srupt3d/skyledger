import datetime
import gzip
import http.server
import threading
import urllib.error

import pytest

from skyledger.feed import Feed, NotFound

FILES = {
    "/tar1090/data/aircraft.json": b'{"now": 1, "aircraft": []}',
    "/tar1090/db-1/A.js": gzip.compress(b'{"children": []}'),  # gzipped body, no Content-Encoding
}


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/boom":
            self.send_error(500)
        elif self.path in FILES:
            body = FILES[self.path]
            self.send_response(200)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        else:
            self.send_error(404)

    def log_message(self, *args):
        pass


@pytest.fixture(scope="module")
def server():
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{srv.server_address[1]}"
    srv.shutdown()


def test_urls_keep_subpath():
    f = Feed("http://r/tar1090")
    assert f.aircraft == "http://r/tar1090/data/aircraft.json"
    assert f.stats == "http://r/tar1090/data/stats.json"
    assert f.receiver == "http://r/tar1090/data/receiver.json"
    assert f.index == "http://r/tar1090/"
    assert f.heatmap(datetime.date(2025, 3, 4), 7) == "http://r/tar1090/globe_history/2025/03/04/heatmap/07.bin.ttf"
    assert f.db("db-3.14", "files.js") == "http://r/tar1090/db-3.14/files.js"


def test_root_url():
    assert Feed("http://ultrafeeder/").aircraft == "http://ultrafeeder/data/aircraft.json"


def test_plain_and_gzipped(server):
    f = Feed(server + "/tar1090/")
    assert f.get_json(f.aircraft) == {"now": 1, "aircraft": []}
    assert f.get_json(f.db("db-1", "A.js")) == {"children": []}


def test_404_is_not_found(server):
    f = Feed(server + "/tar1090/")
    with pytest.raises(NotFound):
        f.get(f.heatmap(datetime.date(2025, 1, 1), 0))


def test_other_errors_propagate(server):
    with pytest.raises(urllib.error.HTTPError):
        Feed(server).get(server + "/boom")
