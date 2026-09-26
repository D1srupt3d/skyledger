import threading

import pytest

from skyledger import demo


@pytest.fixture(scope="module")
def feed_url():
    srv = demo.server(0)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    yield f"http://127.0.0.1:{srv.server_address[1]}/"
    srv.shutdown()
