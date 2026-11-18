package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/D1srupt3d/skyledger/internal/config"
)

type fakeDB struct{ err error }

func (f *fakeDB) Ping(context.Context) error { return f.err }
func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, f.err
}
func (f *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return errRow{f.err} }

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	rec := get(t, Handler(&fakeDB{err: errors.New("down")}, time.UTC), "GET", "/healthz")
	if rec.Code != 200 || rec.Body.String() != "ok\n" {
		t.Errorf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestReadyzReportsDatabase(t *testing.T) {
	db := &fakeDB{err: errors.New("connection refused")}
	h := Handler(db, time.UTC)
	if rec := get(t, h, "GET", "/readyz"); rec.Code != 503 || rec.Body.String() != "database unreachable\n" {
		t.Errorf("down: %d %q", rec.Code, rec.Body.String())
	}
	db.err = nil // the database comes back: no restart needed
	if rec := get(t, h, "GET", "/readyz"); rec.Code != 200 || rec.Body.String() != "ok\n" {
		t.Errorf("up: %d %q", rec.Code, rec.Body.String())
	}
}

func TestShellIsEmbedded(t *testing.T) {
	rec := get(t, Handler(&fakeDB{}, time.UTC), "GET", "/")
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "<title>skyledger</title>") {
		t.Errorf("body %q", rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("cache control %q", cc)
	}
}

func TestRoutes(t *testing.T) {
	h := Handler(&fakeDB{}, time.UTC)
	if rec := get(t, h, "GET", "/nope"); rec.Code != 404 {
		t.Errorf("unknown path: %d", rec.Code)
	}
	for _, path := range []string{"/healthz", "/readyz", "/", "/api/live"} {
		if rec := get(t, h, "POST", path); rec.Code != 405 {
			t.Errorf("POST %s: %d", path, rec.Code)
		}
	}
}

func TestReadOnlyConfigReplacesUser(t *testing.T) {
	for _, dsn := range []string{
		"postgresql://owner:ownerpw@db:5432/sky",
		"host=db user=owner password=ownerpw dbname=sky",
	} {
		cfg, err := ReadOnlyConfig(config.Settings{DatabaseURL: dsn, ReadOnlyDBPassword: "ro-pw"})
		if err != nil {
			t.Fatalf("%s: %v", dsn, err)
		}
		c := cfg.ConnConfig
		if c.User != "skyledger_grafana" || c.Password != "ro-pw" || c.Host != "db" || c.Database != "sky" {
			t.Errorf("%s: user %q password %q host %q db %q", dsn, c.User, c.Password, c.Host, c.Database)
		}
		if c.RuntimeParams["default_transaction_read_only"] != "on" {
			t.Errorf("%s: not read only: %v", dsn, c.RuntimeParams)
		}
	}
}

func TestReadOnlyConfigNeedsPassword(t *testing.T) {
	_, err := ReadOnlyConfig(config.Settings{DatabaseURL: "postgresql://owner:pw@db/sky"})
	if err == nil || err.Error() != "READONLY_DB_PASSWORD is required for web" {
		t.Errorf("err %v", err)
	}
}

func TestServeStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	s := config.Settings{DatabaseURL: "postgresql://owner:pw@127.0.0.1:1/sky", ReadOnlyDBPassword: "ro"}
	if err := serve(ctx, s, "127.0.0.1:0"); err != nil {
		t.Errorf("serve: %v", err)
	}
}
