// Command skyledger: `skyledger migrate|ingest|history|demo-feed`.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/D1srupt3d/skyledger/internal/config"
	"github.com/D1srupt3d/skyledger/internal/demo"
	"github.com/D1srupt3d/skyledger/internal/history"
	"github.com/D1srupt3d/skyledger/internal/ingest"
	"github.com/D1srupt3d/skyledger/internal/migrate"
	"github.com/D1srupt3d/skyledger/internal/version"
)

const usage = `usage: skyledger [--version] {migrate,ingest,history,demo-feed} ...

Archive a readsb/tar1090 ADS-B receiver in TimescaleDB.

commands:
  migrate     create or upgrade the database schema, then exit
  ingest      poll the receiver's live feed and stats (runs forever)
  history     import heatmap history now and then nightly (runs forever)
  demo-feed   serve a synthetic tar1090 for tests and the demo profile (--port, default 8080)
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, lookup func(string) (string, bool)) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	case "--version":
		_, _ = fmt.Fprintf(stdout, "skyledger %s\n", version.Version)
		return 0
	case "demo-feed":
		fs := flag.NewFlagSet("demo-feed", flag.ContinueOnError)
		fs.SetOutput(stderr)
		port := fs.Int("port", 8080, "port to listen on")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if err := demo.Serve(*port); err != nil {
			_, _ = fmt.Fprintf(stderr, "skyledger: %v\n", err)
			return 1
		}
		return 0
	case "migrate", "ingest", "history":
	default:
		_, _ = fmt.Fprintf(stderr, "skyledger: unknown command '%s'\n\n%s", args[0], usage)
		return 2
	}
	settings, err := config.FromEnv(lookup)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "skyledger: %v\n", err)
		return 2
	}
	commands := map[string]func(context.Context, config.Settings) error{
		"migrate": migrate.Run, "ingest": ingest.Run, "history": history.Run,
	}
	if err := commands[args[0]](ctx, settings); err != nil {
		_, _ = fmt.Fprintf(stderr, "skyledger: %v\n", err)
		return 1
	}
	return 0
}
