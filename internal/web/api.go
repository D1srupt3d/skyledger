package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/D1srupt3d/skyledger/internal/geo"
)

// DB is what the API needs from the database; *pgxpool.Pool satisfies it.
type DB interface {
	Ping(context.Context) error
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

var _ DB = (*pgxpool.Pool)(nil)

// cache holds one endpoint's encoded JSON for ttl. It builds under the lock, so
// concurrent requests wait for one build and reuse it; errors are never cached.
// ponytail: one mutex per endpoint; per-parameter keys when endpoints take a time range (phase 3).
type cache struct {
	ttl  time.Duration
	mu   sync.Mutex
	body []byte
	at   time.Time
}

func (c *cache) get(ctx context.Context, build func(context.Context) (any, error)) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.body != nil && time.Since(c.at) < c.ttl {
		return c.body, nil
	}
	v, err := build(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	c.body, c.at = append(body, '\n'), time.Now()
	return c.body, nil
}

func writeJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// writeError logs the real error and tells the client nothing about it.
func writeError(w http.ResponseWriter, path string, err error) {
	if errors.Is(err, context.Canceled) { // the client went away: nothing to report
		slog.Debug("api", "path", path, "err", err)
	} else {
		slog.Error("api", "path", path, "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte("{\"error\":\"database query failed\"}\n"))
}

// api holds what the endpoint builders (now.go) query with.
type api struct {
	db DB
	tz *time.Location

	mapMu  sync.Mutex
	mapAt  [2]float64 // the receiver location mapped was built for
	mapped *geo.Map
}

// route serves one cached builder as GET path. A build gets 10 s, so a hung
// query fails the request instead of stalling every waiter on the cache.
func route(mux *http.ServeMux, path string, ttl time.Duration, build func(context.Context) (any, error)) {
	c := &cache{ttl: ttl}
	bounded := func(ctx context.Context) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return build(ctx)
	}
	mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		body, err := c.get(r.Context(), bounded)
		if err != nil {
			writeError(w, path, err)
			return
		}
		writeJSON(w, body)
	})
}

// secure adds the security headers to every response.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
