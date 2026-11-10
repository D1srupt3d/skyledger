// Package aircraftdb loads tar1090's aircraft database (registration, type, flags) and
// airline names, from the receiver itself.
package aircraftdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/D1srupt3d/skyledger/internal/feed"
)

var folder = regexp.MustCompile(`databaseFolder\s*=\s*"([^"]+)"`)

// DBVersion reads tar1090's index page, which sets `databaseFolder = "db-<version>";`.
func DBVersion(indexHTML string) (string, error) {
	m := folder.FindStringSubmatch(indexHTML)
	if m == nil {
		return "", errors.New("databaseFolder not found in tar1090's index page")
	}
	return m[1], nil
}

// FlagBits reads tar1090's flags string, one character per bit ("10" military, "0001" LADD).
func FlagBits(flags string) int16 {
	var bits int16
	for i, c := range flags {
		if c == '1' {
			bits |= 1 << i
		}
	}
	return bits
}

// DBRows turns one database shard into aircraft_db rows (hex, registration, type_code,
// description, flags), sorted by hex. Keys are the hex minus the shard prefix; each record
// is [registration, type, flags, description], any of them null.
func DBRows(shard string, data map[string]json.RawMessage) ([][]any, error) {
	var rows [][]any
	for key, raw := range data {
		if key == "children" {
			continue
		}
		var rec []*string
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("aircraft database %s%s: %w", shard, key, err)
		}
		rec = append(rec, make([]*string, max(0, 4-len(rec)))...)
		rows = append(rows, []any{strings.ToLower(shard + key), orNil(rec[0]), orNil(rec[1]), orNil(rec[3]),
			FlagBits(deref(rec[2]))})
	}
	slices.SortFunc(rows, func(a, b []any) int { return strings.Compare(a[0].(string), b[0].(string)) })
	return rows, nil
}

// Operator is one operators.js entry: name, country, callsign.
type Operator struct {
	N string `json:"n"`
	C string `json:"c"`
	R string `json:"r"`
}

// AirlineRows turns operators.js into airlines rows (code, name, country, callsign), sorted by code.
func AirlineRows(operators map[string]Operator) [][]any {
	var rows [][]any
	for code, o := range operators {
		rows = append(rows, []any{code, orNil(&o.N), orNil(&o.C), orNil(&o.R)})
	}
	slices.SortFunc(rows, func(a, b []any) int { return strings.Compare(a[0].(string), b[0].(string)) })
	return rows
}

// Load reloads aircraft_db and airlines if tar1090's database version changed. Returns the version.
func Load(ctx context.Context, conn *pgx.Conn, f *feed.Feed) (string, error) {
	index, err := f.Get(ctx, f.Index())
	if err != nil {
		return "", err
	}
	version, err := DBVersion(string(index))
	if err != nil {
		return "", err
	}
	var current string
	err = conn.QueryRow(ctx, "SELECT value FROM db_meta WHERE key = 'aircraft_db_version'").Scan(&current)
	if err == nil && current == version {
		return version, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var shards []string
	if err := f.GetJSON(ctx, f.DB(version, "files.js"), &shards); err != nil {
		return "", err
	}
	var aircraft [][]any
	for _, shard := range shards {
		var data map[string]json.RawMessage
		if err := f.GetJSON(ctx, f.DB(version, shard+".js"), &data); err != nil {
			return "", err
		}
		rows, err := DBRows(shard, data)
		if err != nil {
			return "", err
		}
		aircraft = append(aircraft, rows...)
	}
	var operators map[string]Operator
	if err := f.GetJSON(ctx, f.DB(version, "operators.js"), &operators); err != nil {
		return "", err
	}
	airlines := AirlineRows(operators)
	// One transaction: readers see the old database until the new one is complete.
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "TRUNCATE aircraft_db, airlines"); err != nil {
			return err
		}
		cols := []string{"hex", "registration", "type_code", "description", "flags"}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"aircraft_db"}, cols, pgx.CopyFromRows(aircraft)); err != nil {
			return err
		}
		cols = []string{"code", "name", "country", "callsign"}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"airlines"}, cols, pgx.CopyFromRows(airlines)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO db_meta VALUES ('aircraft_db_version', $1) "+
			"ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", version)
		return err
	})
	if err != nil {
		return "", err
	}
	slog.Info("aircraft database loaded", "version", version, "aircraft", len(aircraft), "airlines", len(airlines))
	return version, nil
}

// orNil is Python's `x or None`: an empty or missing string is NULL.
func orNil(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
