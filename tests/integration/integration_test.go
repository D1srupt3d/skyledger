// Package integration runs skyledger end to end against a real TimescaleDB (community
// edition) and the demo feed.
//
// SKYLEDGER_TEST_DSN must be a superuser on a TimescaleDB server, e.g. the CI service container:
// postgresql://skyledger:test@localhost:5432/skyledger. The tests create their own databases
// (pipeline_test, tenant_test, other_tenant) and the roles they need, so reruns start clean.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/D1srupt3d/skyledger/internal/config"
	"github.com/D1srupt3d/skyledger/internal/dashboards"
	"github.com/D1srupt3d/skyledger/internal/demo"
	"github.com/D1srupt3d/skyledger/internal/feed"
	"github.com/D1srupt3d/skyledger/internal/history"
	"github.com/D1srupt3d/skyledger/internal/ingest"
	"github.com/D1srupt3d/skyledger/internal/migrate"
	"github.com/D1srupt3d/skyledger/internal/probe"
	"github.com/D1srupt3d/skyledger/internal/queries"
	"github.com/D1srupt3d/skyledger/internal/web"
)

var ctx = context.Background()

func dsnConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	dsn := os.Getenv("SKYLEDGER_TEST_DSN")
	if dsn == "" {
		t.Skip("SKYLEDGER_TEST_DSN not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// connect opens a connection to the test server as user/password on database db ("" keeps the DSN's).
func connect(t *testing.T, base *pgx.ConnConfig, user, password, db string) (*pgx.Conn, error) {
	t.Helper()
	cfg := base.Copy()
	if user != "" {
		cfg.User, cfg.Password = user, password
	}
	if db != "" {
		cfg.Database = db
	}
	return pgx.ConnectConfig(ctx, cfg)
}

func mustConnect(t *testing.T, base *pgx.ConnConfig, user, password, db string) *pgx.Conn {
	t.Helper()
	c, err := connect(t, base, user, password, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	return c
}

func exec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func scalar[T any](t *testing.T, c *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := c.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func demoFeed(t *testing.T) (string, *feed.Feed) {
	t.Helper()
	srv := httptest.NewServer(demo.Handler())
	t.Cleanup(srv.Close)
	f, err := feed.New(srv.URL, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return srv.URL + "/", f
}

func redirectProbes(t *testing.T) {
	dir := t.TempDir()
	probe.Alive, probe.Ready = filepath.Join(dir, "alive"), filepath.Join(dir, "ready")
	t.Cleanup(func() { probe.Alive, probe.Ready = "/tmp/alive", "/tmp/ready" })
}

func isInsufficientPrivilege(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "42501"
}

func meta(t *testing.T, c *pgx.Conn, key string) string {
	t.Helper()
	v, err := history.Meta(ctx, c, key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A fresh install whose readsb has a location but no heatmap history yet: the
// location is still stored (RunOnce returns early when it finds no history).
func TestLocationWithoutHistory(t *testing.T) {
	base := dsnConfig(t)
	redirectProbes(t)
	admin := mustConnect(t, base, "", "", "")
	exec(t, admin, "DROP DATABASE IF EXISTS location_test WITH (FORCE)")
	exec(t, admin, "CREATE DATABASE location_test")
	conn := mustConnect(t, base, "", "", "location_test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/data/receiver.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"lat": 50.9, "lon": -0.11}`))
	}))
	t.Cleanup(srv.Close)
	f, err := feed.New(srv.URL, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	s := config.Settings{Tar1090URL: srv.URL + "/", TZ: time.UTC, RetentionDays: 30}
	if err := migrate.Apply(ctx, conn, s); err != nil {
		t.Fatal(err)
	}
	if _, err := history.RunOnce(ctx, conn, f, s, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if lat, lon := meta(t, conn, "rx_lat"), meta(t, conn, "rx_lon"); lat != "50.9" || lon != "-0.11" {
		t.Errorf("receiver location %q, %q", lat, lon)
	}
}

func TestPipeline(t *testing.T) {
	base := dsnConfig(t)
	redirectProbes(t)
	admin := mustConnect(t, base, "", "", "")
	exec(t, admin, "DROP DATABASE IF EXISTS pipeline_test WITH (FORCE)")
	exec(t, admin, "CREATE DATABASE pipeline_test")
	conn := mustConnect(t, base, "", "", "pipeline_test")

	url, f := demoFeed(t)
	london, _ := time.LoadLocation("Europe/London")
	s := config.Settings{Tar1090URL: url, TZ: london, Poll: 10 * time.Second, RetentionDays: 30,
		ReadOnlyDBPassword: "grafana-test"}

	step := func(name string, fn func(t *testing.T)) {
		if !t.Run(name, fn) {
			t.FailNow() // later steps build on this one
		}
	}

	step("migrate is idempotent", func(t *testing.T) {
		// What the old 0001 left behind; 0002 must take it away again.
		exec(t, conn, "DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'skyledger_grafana') THEN "+
			"CREATE ROLE skyledger_grafana; END IF; END $$")
		exec(t, conn, "GRANT pg_read_all_data TO skyledger_grafana")
		for range 2 {
			if err := migrate.Apply(ctx, conn, s); err != nil {
				t.Fatal(err)
			}
		}
		if scalar[bool](t, conn, "SELECT pg_has_role('skyledger_grafana', 'pg_read_all_data', 'MEMBER')") {
			t.Error("pg_read_all_data still granted")
		}
		all, _ := migrate.Discover()
		if n := scalar[int](t, conn, "SELECT count(*) FROM schema_migrations"); n != len(all) {
			t.Errorf("%d migrations recorded, %d shipped", n, len(all))
		}
		if tz := scalar[string](t, conn, "SELECT value FROM db_meta WHERE key = 'tz'"); tz != "Europe/London" {
			t.Error(tz)
		}
		rows, _ := conn.Query(ctx, "SELECT hypertable_name, proc_name FROM timescaledb_information.jobs "+
			"WHERE hypertable_name IS NOT NULL")
		jobs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Table, Proc string }])
		if err != nil {
			t.Fatal(err)
		}
		tables, retention := map[string]bool{}, false
		for _, j := range jobs {
			tables[j.Table] = true
			retention = retention || (j.Table == "positions" && strings.Contains(j.Proc, "retention"))
		}
		if !tables["positions"] || !tables["history_positions"] || !tables["receiver_stats"] || !retention {
			t.Errorf("jobs %v", jobs)
		}
		// A changed retention replaces the policy.
		s2 := s
		s2.RetentionDays = 31
		if err := migrate.ApplySettings(ctx, conn, s2); err != nil {
			t.Fatal(err)
		}
		if v := scalar[string](t, conn, "SELECT value FROM db_meta WHERE key = 'retention_days'"); v != "31" {
			t.Error(v)
		}
		if err := migrate.ApplySettings(ctx, conn, s); err != nil {
			t.Fatal(err)
		}
	})

	step("ingest", func(t *testing.T) {
		n, err := ingest.PollOnce(ctx, conn, f, s, true)
		if err != nil {
			t.Fatal(err)
		}
		if n < 2 { // the demo never has an empty sky
			t.Errorf("%d positions", n)
		}
		for table, want := range map[string]int{"positions": n, "receiver_stats": 1, "aircraft": n} {
			if got := scalar[int](t, conn, "SELECT count(*) FROM "+table); got != want {
				t.Errorf("%s: %d rows, want %d", table, got, want)
			}
		}
		// The numbers arrived as numbers, the lists as lists.
		if v := scalar[int](t, conn, "SELECT count(*) FROM positions WHERE nic = 8 AND nav_modes IS NOT NULL "+
			"AND wind_speed > 0"); v == 0 {
			t.Error("typed fields not stored")
		}
	})

	step("history", func(t *testing.T) {
		res, err := history.RunOnce(ctx, conn, f, s, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if res != (history.Result{Imported: 48 * demo.Days}) {
			t.Errorf("%+v", res)
		}
		// The web map centers on the receiver (demo.Receiver).
		if lat, lon := meta(t, conn, "rx_lat"), meta(t, conn, "rx_lon"); lat != "45" || lon != "-35" {
			t.Errorf("receiver location %q, %q", lat, lon)
		}
		if n := scalar[int](t, conn, "SELECT count(*) FROM history_files"); n != 48*demo.Days {
			t.Errorf("%d files", n)
		}
		// One row per aircraft per minute in range, over the whole days imported.
		today := time.Now().UTC().Truncate(24 * time.Hour)
		first := today.AddDate(0, 0, -demo.Days).Unix()
		expected := 0
		for at := first; at < today.Unix(); at += 60 {
			expected += len(demo.Visible(float64(at)))
		}
		if n := scalar[int](t, conn, "SELECT count(*) FROM history_positions"); n != expected {
			t.Errorf("%d history positions, want %d", n, expected)
		}
		if n := scalar[int](t, conn, "SELECT count(DISTINCT hex) FROM history_positions"); n != len(demo.Planes) {
			t.Errorf("%d aircraft", n)
		}
		for sql, check := range map[string]func(int) bool{
			"SELECT count(*) FROM daily_stats":     func(n int) bool { return n >= demo.Days },
			"SELECT count(*) FROM history_outline": func(n int) bool { return n > 20 },
			"SELECT count(*) FROM flights":         func(n int) bool { return n > 0 },
			"SELECT count(*) FROM aircraft_db":     func(n int) bool { return n == len(demo.Planes) },
			"SELECT count(*) FROM airlines":        func(n int) bool { return n == 2 },
		} {
			if n := scalar[int](t, conn, sql); !check(n) {
				t.Errorf("%s: %d", sql, n)
			}
		}
		// A second run finds nothing new.
		if res, err := history.RunOnce(ctx, conn, f, s, time.Now().UTC()); err != nil || res != (history.Result{}) {
			t.Errorf("%+v %v", res, err)
		}
	})

	step("history rebuilds a missing outline", func(t *testing.T) {
		exec(t, conn, "DELETE FROM history_outline")
		if _, err := history.RunOnce(ctx, conn, f, s, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if n := scalar[int](t, conn, "SELECT count(*) FROM history_outline"); n <= 20 {
			t.Errorf("%d outline rows", n)
		}
	})

	step("history fills live gaps", func(t *testing.T) {
		// Live data an hour apart yesterday, then the rows from ingest now: two gaps an outage left.
		noon := time.Now().UTC().Truncate(24*time.Hour).Add(12*time.Hour).AddDate(0, 0, -1)
		edges := []any{noon, noon.Add(time.Hour)}
		for _, e := range edges {
			exec(t, conn, "INSERT INTO positions (time, hex, lat, lon, on_ground) VALUES ($1, 'edge', 0, 0, false)", e)
		}
		liveBefore := scalar[int](t, conn, "SELECT count(*) FROM positions")
		if _, err := history.RunOnce(ctx, conn, f, s, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		inside := " WHERE time > $1 AND time < $2 AND hex <> 'edge'"
		inHistory := scalar[int](t, conn, "SELECT count(*) FROM history_positions"+inside, edges...)
		if inHistory == 0 {
			t.Fatal("no history inside the gap")
		}
		if n := scalar[int](t, conn, "SELECT count(*) FROM positions"+inside, edges...); n != inHistory {
			t.Errorf("%d live rows in the gap, want %d", n, inHistory)
		}
		// Nothing before the first live row: filling the past is history's job, not live's.
		if n := scalar[int](t, conn, "SELECT count(*) FROM positions WHERE time < "+
			"(SELECT min(time) FROM positions WHERE hex = 'edge')"); n != 0 {
			t.Errorf("%d rows before the first live row", n)
		}
		// Every gap is closed now, so another run copies nothing.
		filled := scalar[int](t, conn, "SELECT count(*) FROM positions")
		if filled <= liveBefore+inHistory { // the gap from noon + 1h to now was filled too
			t.Errorf("%d rows after filling, %d before, %d in the first gap", filled, liveBefore, inHistory)
		}
		if _, err := history.RunOnce(ctx, conn, f, s, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if n := scalar[int](t, conn, "SELECT count(*) FROM positions"); n != filled {
			t.Errorf("rerun copied %d more rows", n-filled)
		}
	})

	grafana := mustConnect(t, base, "skyledger_grafana", "grafana-test", "pipeline_test")

	step("grafana role is read only", func(t *testing.T) {
		if n := scalar[int](t, grafana, "SELECT count(*) FROM flights"); n == 0 {
			t.Error("grafana sees no flights")
		}
		if _, err := grafana.Exec(ctx, "DELETE FROM positions"); !isInsufficientPrivilege(err) {
			t.Errorf("DELETE as grafana: %v", err)
		}
	})

	step("web connects read only", func(t *testing.T) {
		// DATABASE_URL names the owner; web must still log in as the read-only role.
		owner := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=pipeline_test",
			base.Host, base.Port, base.User, base.Password)
		cfg, err := web.ReadOnlyConfig(config.Settings{DatabaseURL: owner, ReadOnlyDBPassword: "grafana-test"})
		if err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		if err := pool.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		var user string
		if err := pool.QueryRow(ctx, "SELECT current_user").Scan(&user); err != nil || user != "skyledger_grafana" {
			t.Errorf("connected as %q: %v", user, err)
		}
		_, err = pool.Exec(ctx, "DELETE FROM positions")
		var pg *pgconn.PgError
		// 42501: insufficient privilege; 25006: read-only transaction. Either refuses the write.
		if !errors.As(err, &pg) || (pg.Code != "42501" && pg.Code != "25006") {
			t.Errorf("DELETE through web's pool: %v", err)
		}
	})

	step("every dashboard query runs", func(t *testing.T) {
		now := time.Now().UTC()
		all := dashboards.AllQueries()
		if len(all) <= 30 {
			t.Fatalf("%d queries", len(all))
		}
		for name, sql := range all {
			e, args, err := queries.Expand(sql, now.AddDate(0, 0, -30), now, "5 minutes")
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			rows, err := grafana.Query(ctx, e, args...)
			if err == nil {
				for rows.Next() {
				}
				err = rows.Err()
			}
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
}

// skyledger as a non-superuser tenant of a shared TimescaleDB (the Helm chart with
// timescaledb.enabled=false). This test plays the platform too, creating the tenant's
// owner, database and extension the way a shared server's setup job would.
func TestTenant(t *testing.T) {
	base := dsnConfig(t)
	admin := mustConnect(t, base, "", "", "")
	for _, stmt := range []string{
		"DROP DATABASE IF EXISTS tenant_test WITH (FORCE)",
		"DROP DATABASE IF EXISTS other_tenant WITH (FORCE)",
		"DROP ROLE IF EXISTS tenant_owner",
		"CREATE ROLE tenant_owner LOGIN PASSWORD 'owner-test'",
		"DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'skyledger_grafana') THEN " +
			"CREATE ROLE skyledger_grafana; END IF; END $$",
		"ALTER ROLE skyledger_grafana LOGIN PASSWORD 'grafana-test'",
		"CREATE DATABASE tenant_test OWNER tenant_owner",
		"CREATE DATABASE other_tenant",
		"REVOKE CONNECT ON DATABASE tenant_test FROM PUBLIC",
		"REVOKE CONNECT ON DATABASE other_tenant FROM PUBLIC",
		"GRANT CONNECT ON DATABASE tenant_test TO tenant_owner, skyledger_grafana",
	} {
		exec(t, admin, stmt)
	}
	exec(t, mustConnect(t, base, "", "", "tenant_test"), "CREATE EXTENSION IF NOT EXISTS timescaledb")

	url, f := demoFeed(t)
	s := config.Settings{Tar1090URL: url, TZ: time.UTC, Poll: 10 * time.Second, RetentionDays: 90}

	ok := t.Run("migrate and ingest as tenant owner", func(t *testing.T) {
		c := mustConnect(t, base, "tenant_owner", "owner-test", "tenant_test")
		for range 2 {
			if err := migrate.Apply(ctx, c, s); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ingest.PollOnce(ctx, c, f, s, true); err != nil {
			t.Fatal(err)
		}
		// Dashboards read compressed chunks too; grants must reach them.
		exec(t, c, "SELECT compress_chunk(ch) FROM show_chunks('positions') ch")
	})
	if !ok {
		t.FailNow()
	}

	t.Run("grafana reads its tenant only", func(t *testing.T) {
		g := mustConnect(t, base, "skyledger_grafana", "grafana-test", "tenant_test")
		if n := scalar[int](t, g, "SELECT count(*) FROM positions"); n == 0 {
			t.Error("no positions")
		}
		if n := scalar[int](t, g, "SELECT count(*) FROM receiver_stats"); n != 1 {
			t.Errorf("%d stats rows", n)
		}
		if !scalar[bool](t, g, "SELECT has_table_privilege('flights', 'SELECT')") {
			t.Error("can't read flights")
		}
		if _, err := g.Exec(ctx, "DELETE FROM positions"); !isInsufficientPrivilege(err) {
			t.Errorf("DELETE as grafana: %v", err)
		}
		_, err := connect(t, base, "skyledger_grafana", "grafana-test", "other_tenant")
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("other tenant: %v", err)
		}
	})

	t.Run("grafana is not a server-wide reader", func(t *testing.T) {
		if scalar[bool](t, admin, "SELECT pg_has_role('skyledger_grafana', 'pg_read_all_data', 'MEMBER')") {
			t.Error("pg_read_all_data granted")
		}
	})
}

// The commands' Run loops against a real database: migrate once, ingest and history until stopped.
func TestRunLoops(t *testing.T) {
	base := dsnConfig(t)
	redirectProbes(t)
	admin := mustConnect(t, base, "", "", "")
	exec(t, admin, "DROP DATABASE IF EXISTS runloops_test WITH (FORCE)")
	exec(t, admin, "CREATE DATABASE runloops_test")
	dsn, err := neturl.Parse(os.Getenv("SKYLEDGER_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/runloops_test"
	feedURL, _ := demoFeed(t)
	s := config.Settings{Tar1090URL: feedURL, DatabaseURL: dsn.String(), TZ: time.UTC, Poll: time.Second,
		RetentionDays: 90}
	if err := migrate.Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	if err := ingest.Run(c, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(probe.Ready); err != nil {
		t.Error("ready not touched after a successful poll")
	}
	if n := scalar[int](t, mustConnect(t, base, "", "", "runloops_test"), "SELECT count(*) FROM receiver_stats"); n != 1 {
		t.Errorf("%d stats rows: stats are polled once a minute, not every poll", n)
	}
}

// The Now page's JSON API against a seeded database, through web's read-only pool.
func TestWebNow(t *testing.T) {
	base := dsnConfig(t)
	admin := mustConnect(t, base, "", "", "")
	exec(t, admin, "DROP DATABASE IF EXISTS web_test WITH (FORCE)")
	exec(t, admin, "CREATE DATABASE web_test")
	t.Cleanup(func() { exec(t, admin, "DROP DATABASE IF EXISTS web_test WITH (FORCE)") })
	conn := mustConnect(t, base, "", "", "web_test")
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	s := config.Settings{TZ: london, RetentionDays: 30, ReadOnlyDBPassword: "grafana-test"}
	if err := migrate.Apply(ctx, conn, s); err != nil {
		t.Fatal(err)
	}
	owner := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=web_test",
		base.Host, base.Port, base.User, base.Password)
	cfg, err := web.ReadOnlyConfig(config.Settings{DatabaseURL: owner, ReadOnlyDBPassword: "grafana-test"})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// A fresh handler per phase: each has its own caches.
	getJSON := func(t *testing.T, srv *httptest.Server, path string, v any) {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	type far struct {
		NM   float64 `json:"nm"`
		Deg  int     `json:"deg"`
		Time string  `json:"time"`
	}
	type outlines struct {
		AllTime       []*float64 `json:"all_time"`
		Today         []*float64 `json:"today"`
		FurthestToday *far       `json:"furthest_today"`
		FurthestEver  *far       `json:"furthest_ever"`
	}

	t.Run("empty", func(t *testing.T) {
		srv := httptest.NewServer(web.Handler(pool, london))
		defer srv.Close()
		var live map[string]any
		getJSON(t, srv, "/api/live", &live)
		st := live["status"].(map[string]any)
		if st["level"] != "silent" || st["age_s"] != nil {
			t.Errorf("status %v", st)
		}
		for _, k := range []string{"aircraft", "notable"} {
			if l, ok := live[k].([]any); !ok || len(l) != 0 {
				t.Errorf("%s: %#v", k, live[k])
			}
		}
		var o outlines
		getJSON(t, srv, "/api/outlines", &o)
		if len(o.AllTime) != 360 || len(o.Today) != 360 || o.FurthestEver != nil || o.FurthestToday != nil {
			t.Errorf("outlines %+v", o)
		}
		for i := range 360 {
			if o.AllTime[i] != nil || o.Today[i] != nil {
				t.Fatalf("deg %d not null", i)
			}
		}
		var traffic map[string]any
		getJSON(t, srv, "/api/traffic", &traffic)
		if traffic["aircraft_yesterday"] != nil || traffic["aircraft_today"] != 0.0 {
			t.Errorf("traffic counts %v %v", traffic["aircraft_today"], traffic["aircraft_yesterday"])
		}
		var m map[string]any
		getJSON(t, srv, "/api/map", &m)
		if _, ok := m["center"]; !ok || m["center"] != nil {
			t.Errorf("map center %v", m["center"])
		}
		for _, k := range []string{"land", "borders", "cities"} {
			if l, ok := m[k].([]any); !ok || len(l) != 0 {
				t.Errorf("map %s: %#v", k, m[k])
			}
		}
	})

	now := time.Now().UTC()
	ago := func(s int) time.Time { return now.Add(-time.Duration(s) * time.Second) }
	pos := "INSERT INTO positions (time, hex, flight, squawk, lat, lon, alt_baro, on_ground, distance_nm, bearing) " +
		"VALUES ($1, $2, $3, $4, 51, 0, $5, false, $6, $7)"
	exec(t, conn, "INSERT INTO db_meta VALUES ('rx_lat', '50.90'), ('rx_lon', '-0.11')")
	exec(t, conn, "INSERT INTO aircraft (hex, type_code, db_flags, first_seen, last_seen) VALUES "+
		"('de0001', 'B38M', 0, $1, $1), ('de0002', 'C17', 1, $1, $1), ('de0003', 'C172', 0, $1, $1), "+
		"('de0004', NULL, 0, $1, $1)", now)
	for _, s := range []int{50, 20} {
		exec(t, conn, pos, ago(s), "de0001", "<b>DMO1</b>", nil, 37000, 120, 45)
	}
	for s := 10; s < 300; s += 30 {
		exec(t, conn, pos, ago(s), "de0002", "RCH99", nil, 28000, 80, 135)
	}
	exec(t, conn, pos, ago(15), "de0003", "DEMOX1", "7700", 3000, 15, 300)
	exec(t, conn, pos, ago(30), "de0004", nil, nil, nil, nil, nil)
	exec(t, conn, "INSERT INTO history_outline VALUES (45, 52, 1, 200, 30000, 'de0001', '2026-10-01T12:00:00Z')")
	old := now.AddDate(0, 0, -3)
	exec(t, conn, "INSERT INTO history_positions (time, hex, lat, lon, on_ground) VALUES "+
		"($1, 'de0002', 51, 0, false), ($2, 'de0002', 51, 0, false)", old, old.Add(time.Minute))
	t.Run("live before flights is filled", func(t *testing.T) {
		// flights is WITH NO DATA until history's first refresh; a military aircraft must not break /api/live.
		srv := httptest.NewServer(web.Handler(pool, london))
		defer srv.Close()
		var live struct {
			Notable []struct {
				Kind      string  `json:"kind"`
				Hex       string  `json:"hex"`
				LastHeard *string `json:"last_heard"`
			} `json:"notable"`
		}
		getJSON(t, srv, "/api/live", &live)
		military := 0
		for _, n := range live.Notable {
			if n.Kind == "military" {
				military++
				if n.Hex != "de0002" || n.LastHeard != nil {
					t.Errorf("military %+v", n)
				}
			}
		}
		if military != 1 {
			t.Errorf("notable %+v", live.Notable)
		}
	})
	exec(t, conn, "REFRESH MATERIALIZED VIEW flights")
	exec(t, conn, "INSERT INTO receiver_stats (time, aircraft_without_pos) VALUES ($1, 2)", ago(30))
	yesterday := time.Now().In(london).AddDate(0, 0, -1).Format("2006-01-02")
	exec(t, conn, "INSERT INTO daily_stats (day, aircraft, positions) VALUES ($1::date, 7, 0)", yesterday)

	srv := httptest.NewServer(web.Handler(pool, london))
	defer srv.Close()

	t.Run("live", func(t *testing.T) {
		var live struct {
			Status struct {
				Level string   `json:"level"`
				AgeS  *float64 `json:"age_s"`
			} `json:"status"`
			Counts struct {
				WithPosition    int  `json:"with_position"`
				WithoutPosition *int `json:"without_position"`
			} `json:"counts"`
			Aircraft []struct {
				Hex      string       `json:"hex"`
				Callsign *string      `json:"callsign"`
				Military bool         `json:"military"`
				Distance *float64     `json:"distance_nm"`
				Trail    [][2]float64 `json:"trail"`
			} `json:"aircraft"`
			Notable []struct {
				Kind      string  `json:"kind"`
				Hex       string  `json:"hex"`
				LastHeard *string `json:"last_heard"`
			} `json:"notable"`
		}
		getJSON(t, srv, "/api/live", &live)
		if live.Status.Level != "live" || live.Status.AgeS == nil {
			t.Errorf("status %+v", live.Status)
		}
		if live.Counts.WithPosition != 4 || live.Counts.WithoutPosition == nil || *live.Counts.WithoutPosition != 2 {
			t.Errorf("counts %+v", live.Counts)
		}
		ac := live.Aircraft
		if len(ac) != 4 || ac[0].Hex != "de0003" || ac[3].Hex != "de0004" || ac[3].Distance != nil {
			t.Fatalf("aircraft %+v", ac)
		}
		for _, a := range ac {
			switch a.Hex {
			case "de0001":
				if a.Callsign == nil || *a.Callsign != "<b>DMO1</b>" {
					t.Errorf("callsign %v", a.Callsign)
				}
			case "de0002":
				if !a.Military || len(a.Trail) < 9 {
					t.Errorf("de0002 military %v, %d trail points", a.Military, len(a.Trail))
				}
				if a.Trail[0] != [2]float64{135, 80} {
					t.Errorf("trail point %v", a.Trail[0])
				}
			case "de0004":
				if a.Trail == nil || len(a.Trail) != 0 {
					t.Errorf("de0004 trail %#v", a.Trail)
				}
			}
		}
		n := live.Notable
		if len(n) != 2 || n[0].Kind != "emergency" || n[0].Hex != "de0003" ||
			n[1].Kind != "military" || n[1].Hex != "de0002" || n[1].LastHeard == nil {
			t.Fatalf("notable %+v", n)
		}
		heard, err := time.Parse(time.RFC3339, *n[1].LastHeard)
		if err != nil || now.Sub(heard) < 71*time.Hour || now.Sub(heard) > 73*time.Hour {
			t.Errorf("last heard %s %v", *n[1].LastHeard, err)
		}
	})

	t.Run("outlines", func(t *testing.T) {
		var o outlines
		getJSON(t, srv, "/api/outlines", &o)
		if len(o.AllTime) != 360 || o.AllTime[45] == nil || *o.AllTime[45] != 200 {
			t.Errorf("all_time[45] %v", o.AllTime)
		}
		if len(o.Today) != 360 || o.Today[45] == nil || *o.Today[45] != 120 {
			t.Errorf("today[45] %v", o.Today)
		}
		if o.FurthestEver == nil || *o.FurthestEver != (far{200, 45, "2026-10-01T12:00:00Z"}) {
			t.Errorf("furthest_ever %+v", o.FurthestEver)
		}
		if o.FurthestToday == nil || *o.FurthestToday != (far{NM: 120, Deg: 45}) {
			t.Errorf("furthest_today %+v", o.FurthestToday)
		}
	})

	t.Run("traffic", func(t *testing.T) {
		var tr struct {
			Tracked struct {
				StepS    int      `json:"step_s"`
				T        []int64  `json:"t"`
				Aircraft []*int64 `json:"aircraft"`
			} `json:"tracked"`
			Altitude map[string]any `json:"altitude"`
			Today    int            `json:"aircraft_today"`
			Yday     *int           `json:"aircraft_yesterday"`
		}
		getJSON(t, srv, "/api/traffic", &tr)
		tk := tr.Tracked
		if tk.StepS != 300 || len(tk.T) != 289 || len(tk.Aircraft) != 289 {
			t.Fatalf("tracked step %d, %d times, %d values", tk.StepS, len(tk.T), len(tk.Aircraft))
		}
		for i := 1; i < len(tk.T); i++ {
			if tk.T[i]-tk.T[i-1] != 300 {
				t.Fatalf("t[%d] - t[%d] = %d", i, i-1, tk.T[i]-tk.T[i-1])
			}
		}
		recent := false
		for i, v := range tk.Aircraft {
			if now.Unix()-tk.T[i] > 600 && v != nil {
				t.Errorf("t %d: %d aircraft, want null", tk.T[i], *v)
			}
			if i >= len(tk.T)-3 && v != nil && *v >= 1 {
				recent = true
			}
		}
		if !recent {
			t.Error("no aircraft in the last three buckets")
		}
		for _, k := range []string{"t", "ground", "below_10k", "10k_30k", "30k_up"} {
			if l, ok := tr.Altitude[k].([]any); !ok || len(l) != 25 {
				t.Errorf("altitude %s: %v", k, tr.Altitude[k])
			}
		}
		if tr.Altitude["step_s"] != 3600.0 {
			t.Errorf("altitude step %v", tr.Altitude["step_s"])
		}
		if tr.Today != 4 || tr.Yday == nil || *tr.Yday != 7 {
			t.Errorf("today %d yesterday %v", tr.Today, tr.Yday)
		}
	})

	t.Run("map", func(t *testing.T) {
		var m struct {
			Center *[2]float64 `json:"center"`
			Land   []any       `json:"land"`
		}
		getJSON(t, srv, "/api/map", &m)
		if m.Center == nil || *m.Center != [2]float64{50.90, -0.11} || len(m.Land) == 0 {
			t.Errorf("center %v, %d land rings", m.Center, len(m.Land))
		}
	})

	t.Run("web queries run read only", func(t *testing.T) {
		var user string
		if err := pool.QueryRow(ctx, "SELECT current_user").Scan(&user); err != nil || user != "skyledger_grafana" {
			t.Fatalf("pool user %q: %v", user, err)
		}
		for name, q := range map[string]struct {
			sql  string
			args []any
		}{
			"LiveAircraft":     {queries.LiveAircraft, []any{ago(60)}},
			"LiveTrails":       {queries.LiveTrails, []any{ago(300)}},
			"LastHeard":        {queries.LastHeard, []any{[]string{"de0002"}, ago(3600)}},
			"WithoutPosition":  {queries.WithoutPosition, []any{ago(120)}},
			"ReceiverLocation": {queries.ReceiverLocation, nil},
			"FlightsReady":     {queries.FlightsReady, nil},
			"AircraftOnDay":    {queries.AircraftOnDay, []any{yesterday}},
		} {
			rows, err := pool.Query(ctx, q.sql, q.args...)
			n := 0
			if err == nil {
				for rows.Next() {
					n++
				}
				err = rows.Err()
			}
			if err != nil || n == 0 {
				t.Errorf("%s: %d rows, %v", name, n, err)
			}
		}
	})
}
