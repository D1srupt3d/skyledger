// Package ingest is the live ingest: fresh positions from aircraft.json, receiver health from stats.json.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/D1srupt3d/skyledger/internal/config"
	"github.com/D1srupt3d/skyledger/internal/feed"
	"github.com/D1srupt3d/skyledger/internal/migrate"
	"github.com/D1srupt3d/skyledger/internal/probe"
)

// StatsEvery: readsb rewrites stats.json once a minute.
const StatsEvery = time.Minute

// Column is one table column and the type its parameter is sent as. JSON numbers go
// as float8 and Postgres casts them on insert, like it did for the Python ingester.
type Column struct{ Name, Key, Cast string }

// Passthrough maps positions columns to the aircraft.json keys stored as-is. Adding a
// field is one line here plus the column in a new migration; the INSERT is generated
// from this, so columns and values can't drift apart.
var Passthrough = []Column{
	{"squawk", "squawk", "text"},
	{"source", "type", "text"},
	{"lat", "lat", "float8"},
	{"lon", "lon", "float8"},
	{"alt_geom", "alt_geom", "float8"},
	{"gs", "gs", "float8"},
	{"track", "track", "float8"},
	{"baro_rate", "baro_rate", "float8"},
	{"distance_nm", "r_dst", "float8"},
	{"bearing", "r_dir", "float8"},
	{"rssi", "rssi", "float8"},
	{"wind_dir", "wd", "float8"},
	{"wind_speed", "ws", "float8"},
	{"oat", "oat", "float8"},
	{"tat", "tat", "float8"},
	{"ias", "ias", "float8"},
	{"tas", "tas", "float8"},
	{"mach", "mach", "float8"},
	{"roll", "roll", "float8"},
	{"mag_heading", "mag_heading", "float8"},
	{"true_heading", "true_heading", "float8"},
	{"track_rate", "track_rate", "float8"},
	{"geom_rate", "geom_rate", "float8"},
	{"nav_altitude_mcp", "nav_altitude_mcp", "float8"},
	{"nav_altitude_fms", "nav_altitude_fms", "float8"},
	{"nav_heading", "nav_heading", "float8"},
	{"nav_qnh", "nav_qnh", "float8"},
	{"nav_modes", "nav_modes", "text[]"},
	// The aircraft's own GPS integrity (NIC) and accuracy (NACp) categories:
	// a region-wide drop is how GPS jamming shows up.
	{"nic", "nic", "float8"},
	{"nac_p", "nac_p", "float8"},
}

// PositionColumns is every positions column, in row order.
var PositionColumns = append([]Column{
	{"time", "", "timestamptz"},
	{"hex", "", "text"},
	{"flight", "", "text"},
	{"alt_baro", "", "float8"},
	{"on_ground", "", "bool"},
	{"emergency", "", "text"},
}, Passthrough...)

var InsertPosition = insert("positions", PositionColumns, "")

const UpsertAircraft = `
INSERT INTO aircraft (hex, registration, type_code, description, operator,
    year, category, db_flags, first_seen, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (hex) DO UPDATE SET
    registration = EXCLUDED.registration,
    type_code = EXCLUDED.type_code,
    description = EXCLUDED.description,
    operator = EXCLUDED.operator,
    year = EXCLUDED.year,
    category = EXCLUDED.category,
    db_flags = EXCLUDED.db_flags,
    last_seen = EXCLUDED.last_seen
`

// StatsColumns is every receiver_stats column, in row order.
var StatsColumns = []Column{{"time", "", "timestamptz"}}

func init() {
	for _, n := range []string{"window_seconds", "signal", "noise", "peak_signal", "strong_signals", "messages",
		"messages_valid", "positions", "max_distance_m", "aircraft_with_pos", "aircraft_without_pos",
		"gain_db", "ppm", "samples_dropped", "samples_lost"} {
		StatsColumns = append(StatsColumns, Column{n, "", "float8"})
	}
	InsertStats = insert("receiver_stats", StatsColumns, " ON CONFLICT (time) DO NOTHING")
}

var InsertStats string

func insert(table string, cols []Column, suffix string) string {
	names, params := make([]string, len(cols)), make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.Name
		params[i] = fmt.Sprintf("$%d::%s", i+1, c.Cast)
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)%s", table, strings.Join(names, ", "), strings.Join(params, ", "), suffix)
}

// Snapshot is one aircraft.json.
type Snapshot struct {
	Now      float64          `json:"now"`
	Aircraft []map[string]any `json:"aircraft"`
}

// ParseAircraft splits one aircraft.json snapshot into position rows and aircraft rows.
//
// Only positions decoded within the last poll (maxAge seconds) are kept: readsb repeats
// the last known position until it goes stale, and storing those repeats would duplicate rows.
func ParseAircraft(snap Snapshot, maxAge float64) (positions, aircraft [][]any, err error) {
	for _, a := range snap.Aircraft {
		seenPos, ok := a["seen_pos"].(float64)
		if _, hasLat := a["lat"]; !hasLat || !ok || seenPos >= maxAge {
			continue
		}
		hex, _ := a["hex"].(string)
		if hex == "" {
			return nil, nil, errors.New("aircraft.json: an aircraft without hex")
		}
		at := time.UnixMicro(int64(math.Round((snap.Now - seenPos) * 1e6))).UTC()
		alt := a["alt_baro"]
		// readsb sends the string "ground" instead of an altitude on the ground.
		onGround := alt == "ground"
		if onGround {
			alt = nil
		}
		emergency := a["emergency"]
		if emergency == "none" {
			emergency = nil
		}
		var flight any
		if s, _ := a["flight"].(string); strings.TrimSpace(s) != "" {
			flight = strings.TrimSpace(s)
		}
		row := []any{at, hex, flight, alt, onGround, text(emergency)}
		for _, c := range Passthrough {
			v := a[c.Key]
			switch c.Cast {
			case "text":
				v = text(v)
			case "text[]":
				v = textArray(v)
			}
			row = append(row, v)
		}
		positions = append(positions, row)
		flags, _ := a["dbFlags"].(float64)
		aircraft = append(aircraft, []any{hex, text(a["r"]), text(a["t"]), text(a["desc"]), text(a["ownOp"]),
			text(a["year"]), text(a["category"]), int16(flags), at, at})
	}
	return positions, aircraft, nil
}

// text sends a JSON value to a text column the way Postgres would have cast it.
func text(v any) any {
	switch x := v.(type) {
	case nil, string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

func textArray(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = fmt.Sprint(x)
	}
	return out
}

// ParseStats turns stats.json into one receiver_stats row for its last complete minute, or nil if absent.
//
// `local` only exists when readsb has its own SDR (not a network-only readsb), so those fields may be nil.
func ParseStats(stats map[string]any) []any {
	m, _ := stats["last1min"].(map[string]any)
	end, ok := m["end"].(float64)
	if !ok {
		return nil
	}
	local, _ := m["local"].(map[string]any)
	var window any
	if start, ok := m["start"].(float64); ok {
		window = end - start
	}
	return []any{
		time.UnixMicro(int64(math.Round(end * 1e6))).UTC(),
		window,
		local["signal"],
		local["noise"],
		local["peak_signal"],
		local["strong_signals"],
		m["messages"],
		m["messages_valid"],
		m["position_count_total"],
		m["max_distance"],
		stats["aircraft_with_pos"],
		stats["aircraft_without_pos"],
		stats["gain_db"],
		stats["estimated_ppm"],
		local["samples_dropped"],
		local["samples_lost"],
	}
}

// PollOnce is one ingest cycle: fresh positions, and receiver stats when asked. Returns positions stored.
func PollOnce(ctx context.Context, conn *pgx.Conn, f *feed.Feed, s config.Settings, withStats bool) (int, error) {
	var snap Snapshot
	if err := f.GetJSON(ctx, f.Aircraft(), &snap); err != nil {
		return 0, err
	}
	positions, aircraft, err := ParseAircraft(snap, s.Poll.Seconds())
	if err != nil {
		return 0, err
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, r := range positions {
			batch.Queue(InsertPosition, r...)
		}
		for _, r := range aircraft {
			batch.Queue(UpsertAircraft, r...)
		}
		return tx.SendBatch(ctx, batch).Close()
	})
	if err != nil {
		return 0, err
	}
	if withStats {
		var stats map[string]any
		if err := f.GetJSON(ctx, f.Stats(), &stats); err != nil {
			return 0, err
		}
		if row := ParseStats(stats); row != nil {
			if _, err := conn.Exec(ctx, InsertStats, row...); err != nil {
				return 0, err
			}
		}
	}
	return len(positions), nil
}

// Run is `skyledger ingest`: poll until ctx is done. One bad poll never stops the loop.
func Run(ctx context.Context, s config.Settings) error {
	f, err := feed.New(s.Tar1090URL, 5*time.Second)
	if err != nil {
		return err
	}
	var conn *pgx.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close(context.WithoutCancel(ctx))
		}
	}()
	var lastStats time.Time
	slog.Info("polling", "url", f.Aircraft(), "every", s.Poll)
	for {
		started := time.Now()
		withStats := lastStats.IsZero() || started.Sub(lastStats) >= StatsEvery
		err := func() error {
			if conn == nil {
				c, err := migrate.Connect(ctx, s.DatabaseURL, 5*time.Second)
				if err != nil {
					return err
				}
				conn = c
			}
			_, err := PollOnce(ctx, conn, f, s, withStats)
			return err
		}()
		if err == nil {
			if withStats {
				lastStats = started
			}
			probe.Touch(probe.Ready)
		} else if ctx.Err() == nil {
			slog.Warn("poll failed", "err", err)
			if conn != nil {
				_ = conn.Close(context.WithoutCancel(ctx))
				conn = nil
			}
		}
		probe.Touch(probe.Alive)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.Poll - time.Since(started)):
		}
	}
}
