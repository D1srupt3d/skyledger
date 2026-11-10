// Package web is `skyledger web`: the web UI and its JSON API, read only, for LAN or
// VPN use (there is no login; put an auth proxy in front before exposing it).
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/D1srupt3d/skyledger/internal/config"
)

// Port the web UI listens on inside the container (Compose publishes WEB_PORT).
const Port = 1200

//go:embed ui
var embedded embed.FS

// ReadOnlyConfig is the database config for web: the server and database from
// DATABASE_URL, but always as the read-only role, whoever DATABASE_URL names.
// Read-only transactions are a second guard behind the role's SELECT-only grants.
func ReadOnlyConfig(s config.Settings) (*pgxpool.Config, error) {
	if s.ReadOnlyDBPassword == "" {
		return nil, errors.New("READONLY_DB_PASSWORD is required for web")
	}
	cfg, err := pgxpool.ParseConfig(s.DatabaseURL)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.User = "skyledger_grafana"
	cfg.ConnConfig.Password = s.ReadOnlyDBPassword
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	return cfg, nil
}

// Handler serves the embedded UI, /healthz (the process is up) and /readyz (the
// database answers). Only GET (and HEAD); anything else is 405.
func Handler(db interface{ Ping(context.Context) error }) http.Handler {
	ui, err := fs.Sub(embedded, "ui")
	if err != nil {
		panic(err) // the embed path is fixed at build time
	}
	files := http.FileServerFS(ui)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			slog.Warn("database unreachable", "error", err) // pgx errors name the user, never the password
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		// The UI changes with each release; make the browser check rather than reuse a stale copy.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	return mux
}

// Run is `skyledger web`.
func Run(ctx context.Context, s config.Settings) error {
	return serve(ctx, s, fmt.Sprintf(":%d", Port))
}

// serve listens on addr until ctx ends. The pool connects on first use, so web
// starts (and /readyz reports 503) while the database is still down.
func serve(ctx context.Context, s config.Settings, addr string) error {
	cfg, err := ReadOnlyConfig(s)
	if err != nil {
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: Handler(pool), ReadHeaderTimeout: 10 * time.Second}
	slog.Info("web listening", "addr", ln.Addr().String(),
		"db_host", cfg.ConnConfig.Host, "db_name", cfg.ConnConfig.Database)

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(stop); err != nil {
		return err
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
