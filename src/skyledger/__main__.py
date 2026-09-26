"""Command line: `skyledger migrate|ingest|history|demo-feed`."""

import argparse
import logging
import sys

from skyledger import __version__
from skyledger.config import ConfigError, Settings


def main(argv=None):
    parser = argparse.ArgumentParser(prog="skyledger", description=__doc__)
    parser.add_argument("--version", action="version", version=f"skyledger {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("migrate", help="create or upgrade the database schema, then exit")
    sub.add_parser("ingest", help="poll the receiver's live feed and stats (runs forever)")
    sub.add_parser("history", help="import heatmap history now and then nightly (runs forever)")
    demo = sub.add_parser("demo-feed", help="serve a synthetic tar1090 for tests and the demo profile")
    demo.add_argument("--port", type=int, default=8080)
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s")

    if args.command == "demo-feed":
        from skyledger import demo
        return demo.serve(args.port)
    try:
        settings = Settings.from_env()
    except ConfigError as e:
        print(f"skyledger: {e}", file=sys.stderr)
        return 2
    if args.command == "migrate":
        from skyledger import migrate
        return migrate.run(settings)
    if args.command == "ingest":
        from skyledger import ingest
        return ingest.run(settings)
    from skyledger import history
    return history.run(settings)


if __name__ == "__main__":
    sys.exit(main())
