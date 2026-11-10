// Package migrate applies numbered SQL migrations, then the runtime settings that come from the environment.
package migrate

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/D1srupt3d/skyledger/internal/config"
)

//go:embed migrations/*.sql
var files embed.FS

var name = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// LockID can be any constant; it only has to be the same for every skyledger process.
const LockID = 0x5C71ED6E

// Migration is one numbered SQL file.
type Migration struct {
	Version int
	Name    string
}

// Discover returns the migration files sorted by version, or of names when given (tests).
// It rejects bad names and duplicate versions.
func Discover(names ...string) ([]Migration, error) {
	if names == nil {
		entries, _ := fs.ReadDir(files, "migrations")
		for _, e := range entries {
			names = append(names, e.Name())
		}
	}
	found := map[int]string{}
	var out []Migration
	for _, n := range names {
		m := name.FindStringSubmatch(n)
		if m == nil {
			return nil, fmt.Errorf("bad migration file name '%s': want NNNN_description.sql", n)
		}
		v, _ := strconv.Atoi(m[1])
		if prev, ok := found[v]; ok {
			return nil, fmt.Errorf("two migrations with version %d: %s, %s", v, prev, n)
		}
		found[v] = n
		out = append(out, Migration{v, n})
	}
	slices.SortFunc(out, func(a, b Migration) int { return a.Version - b.Version })
	return out, nil
}

// Read returns a shipped migration's SQL.
func Read(n string) (string, error) {
	b, err := files.ReadFile("migrations/" + n)
	return string(b), err
}

// Apply runs every migration not yet in schema_migrations, then the runtime settings.
func Apply(ctx context.Context, conn *pgx.Conn, s config.Settings) error {
	// Session-level lock: ingest and history both start after migrate, but a
	// user running `skyledger migrate` by hand at the same time must wait.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", LockID); err != nil {
		return err
	}
	// The unlock must run even when ctx is cancelled; a closed connection drops the lock anyway.
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", LockID) //nolint:errcheck
	if _, err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version integer PRIMARY KEY, "+
		"name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	rows, _ := conn.Query(ctx, "SELECT version FROM schema_migrations")
	applied, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return err
	}
	all, err := Discover()
	if err != nil {
		return err
	}
	for _, m := range all {
		if slices.Contains(applied, m.Version) {
			continue
		}
		slog.Info("applying migration", "name", m.Name)
		sql, err := Read(m.Name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			// No arguments: pgx sends it as one simple query, so a file may hold many statements.
			if _, err := tx.Exec(ctx, sql); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.Version, m.Name)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", m.Name, err)
		}
	}
	return ApplySettings(ctx, conn, s)
}

// SetMeta stores one db_meta value.
func SetMeta(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, key, value string) error {
	_, err := q.Exec(ctx, "INSERT INTO db_meta VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value",
		key, value)
	return err
}

// ApplySettings writes the settings that come from the environment, on every run.
func ApplySettings(ctx context.Context, conn *pgx.Conn, s config.Settings) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		// Dashboards bucket by local day through this value, so no time zone is hard-coded in them.
		if err := SetMeta(ctx, tx, "tz", s.TZ.String()); err != nil {
			return err
		}
		var current string
		err := tx.QueryRow(ctx, "SELECT value FROM db_meta WHERE key = 'retention_days'").Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if current != strconv.Itoa(s.RetentionDays) {
			if _, err := tx.Exec(ctx, "SELECT remove_retention_policy('positions', if_exists => true)"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "SELECT add_retention_policy('positions', drop_after => make_interval(days => $1))",
				s.RetentionDays); err != nil {
				return err
			}
			if err := SetMeta(ctx, tx, "retention_days", strconv.Itoa(s.RetentionDays)); err != nil {
				return err
			}
			slog.Info("live position retention set", "days", s.RetentionDays)
		}
		if s.GrafanaDBPassword != "" {
			// Utility statements can't take bind parameters, so the password is quoted as a literal. Never logged.
			lit, err := conn.PgConn().EscapeString(s.GrafanaDBPassword)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "ALTER ROLE skyledger_grafana LOGIN PASSWORD '"+lit+"'"); err != nil {
				return err
			}
		}
		return nil
	})
}

// Connect opens a connection that logs the server's warnings (0002 warns through one).
func Connect(ctx context.Context, url string, timeout time.Duration) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnectTimeout = timeout
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		if n.SeverityUnlocalized == "WARNING" {
			slog.Warn("postgres: " + n.Message)
		} else {
			slog.Debug("postgres: " + n.Message)
		}
	}
	return pgx.ConnectConfig(ctx, cfg)
}

// Run is `skyledger migrate`.
func Run(ctx context.Context, s config.Settings) error {
	conn, err := Connect(ctx, s.DatabaseURL, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if err := Apply(ctx, conn, s); err != nil {
		return err
	}
	slog.Info("schema up to date")
	return nil
}
