// Package integration runs skyledger end to end against a real TimescaleDB (community
// edition) and the demo feed.
//
// SKYLEDGER_TEST_DSN must be a superuser on a TimescaleDB server, e.g. the CI service container:
// postgresql://skyledger:test@localhost:5432/skyledger. The tests create their own databases
// (pipeline_test, tenant_test, other_tenant) and the roles they need, so reruns start clean.
package integration

import (
	"context"
	"errors"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.yaml.in/yaml/v3"

	"github.com/D1srupt3d/skyledger/internal/config"
	"github.com/D1srupt3d/skyledger/internal/dashboards"
	"github.com/D1srupt3d/skyledger/internal/demo"
	"github.com/D1srupt3d/skyledger/internal/feed"
	"github.com/D1srupt3d/skyledger/internal/history"
	"github.com/D1srupt3d/skyledger/internal/ingest"
	"github.com/D1srupt3d/skyledger/internal/migrate"
	"github.com/D1srupt3d/skyledger/internal/probe"
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
		GrafanaDBPassword: "grafana-test"}

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

	step("every dashboard query runs", func(t *testing.T) {
		now := time.Now().UTC()
		frm, to := "'"+now.AddDate(0, 0, -30).Format(time.RFC3339Nano)+"'", "'"+now.Format(time.RFC3339Nano)+"'"
		filter := regexp.MustCompile(`\$__timeFilter\(([\w.]+)\)`)
		group := regexp.MustCompile(`\$__timeGroupAlias\(([\w.]+), *\$__interval(?:, *NULL)?\)`)
		queries := dashboards.AllQueries()
		if len(queries) <= 30 {
			t.Fatalf("%d queries", len(queries))
		}
		for name, sql := range queries {
			e := filter.ReplaceAllString(sql, "${1} BETWEEN "+frm+" AND "+to)
			e = group.ReplaceAllString(e, "time_bucket('5 minutes', ${1}) AS time")
			e = strings.NewReplacer("$__timeFrom()", frm, "$__timeTo()", to).Replace(e)
			if strings.Contains(e, "$__") {
				t.Errorf("%s: macro left in %s", name, e)
				continue
			}
			rows, err := grafana.Query(ctx, e)
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

	step("every alert query runs", func(t *testing.T) {
		raw, err := os.ReadFile("../../charts/skyledger/grafana/alerting/skyledger.yml")
		if err != nil {
			t.Fatal(err)
		}
		var alerting struct {
			Groups []struct {
				Rules []struct {
					Title string
					Data  []struct {
						Model struct {
							RawSQL string `yaml:"rawSql"`
						}
					}
				}
			}
		}
		if err := yaml.Unmarshal(raw, &alerting); err != nil {
			t.Fatal(err)
		}
		rules := alerting.Groups[0].Rules
		if len(rules) != 4 {
			t.Fatalf("%d rules", len(rules))
		}
		for _, r := range rules {
			rows, err := grafana.Query(ctx, r.Data[0].Model.RawSQL)
			if err == nil {
				for rows.Next() {
				}
				err = rows.Err()
			}
			if err != nil {
				t.Errorf("%s: %v", r.Title, err)
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
