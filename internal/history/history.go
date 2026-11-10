// Package history imports readsb's heatmap replay files, then keeps the derived tables current.
//
// It runs once at start and then daily at HISTORY_AT (local TZ). Each run imports every
// file from the first history day through yesterday (UTC) that history_files doesn't
// list yet, so the first run is the backfill, later runs pick up one day, and a
// missed night catches up by itself.
package history

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/D1srupt3d/skyledger/internal/aircraftdb"
	"github.com/D1srupt3d/skyledger/internal/config"
	"github.com/D1srupt3d/skyledger/internal/feed"
	"github.com/D1srupt3d/skyledger/internal/heatmap"
	"github.com/D1srupt3d/skyledger/internal/migrate"
	"github.com/D1srupt3d/skyledger/internal/probe"
)

const (
	HalfHours = 48
	// LookbackDays is how far before a found day to look for earlier history across a gap.
	LookbackDays = 14
	// RetryAfter a failed run.
	RetryAfter = 15 * time.Minute
)

// Probes are the half hours probed to decide whether a day has any history at all.
var Probes = []int{0, 12, 24, 36, 47}

const day = 24 * time.Hour

// NextRun is the next HISTORY_AT in local time strictly after now.
func NextRun(now, at time.Time, tz *time.Location) time.Time {
	local := now.In(tz)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), at.Hour(), at.Minute(), at.Second(), 0, tz)
	if !candidate.After(local) {
		candidate = time.Date(local.Year(), local.Month(), local.Day()+1, at.Hour(), at.Minute(), at.Second(), 0, tz)
	}
	return candidate
}

// FindFirstDay is the earliest day with heatmap files, or nil if there is no history.
//
// Binary search assumes history is mostly contiguous back from yesterday; the
// lookback afterwards steps over short gaps (a receiver that was off for a few days).
func FindFirstDay(exists func(time.Time) (bool, error), yesterday time.Time, horizonDays int) (*time.Time, error) {
	any3 := false
	for d := range 3 {
		ok, err := exists(yesterday.AddDate(0, 0, -d))
		if err != nil {
			return nil, err
		}
		if ok {
			any3 = true
			break
		}
	}
	if !any3 {
		return nil, nil
	}
	lo := yesterday.AddDate(0, 0, -horizonDays)
	if ok, err := exists(lo); err != nil || ok {
		return &lo, err
	}
	hi := yesterday
	for days(lo, hi) > 1 {
		mid := lo.AddDate(0, 0, days(lo, hi)/2)
		ok, err := exists(mid)
		if err != nil {
			return nil, err
		}
		if ok {
			hi = mid
		} else {
			lo = mid
		}
	}
	for {
		var earlier *time.Time
		for k := 1; k <= LookbackDays; k++ {
			d := hi.AddDate(0, 0, -k)
			ok, err := exists(d)
			if err != nil {
				return nil, err
			}
			if ok {
				earlier = &d
				break
			}
		}
		if earlier == nil {
			return &hi, nil
		}
		hi = *earlier
	}
}

// days between two UTC midnights.
func days(a, b time.Time) int { return int(b.Sub(a) / day) }

// DayExists probes a few half hours of day for a heatmap file.
func DayExists(ctx context.Context, f *feed.Feed, d time.Time) (bool, error) {
	for _, half := range Probes {
		_, err := f.Get(ctx, f.Heatmap(d, half))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, feed.ErrNotFound) {
			return false, err
		}
	}
	return false, nil
}

// Path is a heatmap file's key in history_files.
func Path(d time.Time, half int) string {
	return fmt.Sprintf("%s/heatmap/%02d.bin.ttf", d.Format("2006/01/02"), half)
}

type fetched struct {
	path string
	raw  []byte // nil: 404, the receiver was down that half hour
}

// ImportDay imports one UTC day's files not yet in done. Returns files imported and to retry.
// It touches conn only when there is something to record.
func ImportDay(ctx context.Context, conn *pgx.Conn, f *feed.Feed, d time.Time, done map[string]bool,
	rx *heatmap.Receiver, s config.Settings, firstDay time.Time) (int, int, error) {
	var files []fetched
	failed, missing := 0, 0
	for half := range HalfHours {
		path := Path(d, half)
		if done[path] {
			continue
		}
		raw, err := f.Get(ctx, f.Heatmap(d, half))
		switch {
		case err == nil:
			files = append(files, fetched{path, raw})
		case errors.Is(err, feed.ErrNotFound):
			files = append(files, fetched{path, nil})
			missing++
		case ctx.Err() != nil:
			return 0, 0, ctx.Err()
		default: // not recorded, so the next run retries it
			slog.Warn("heatmap file failed, retrying next run", "path", path, "err", err)
			failed++
		}
		if err := sleep(ctx, s.FetchPause); err != nil {
			return 0, 0, err
		}
	}
	// A whole day of 404s looks like a broken web server rather than a receiver
	// outage: record nothing, so the next run asks again.
	if missing == HalfHours {
		slog.Warn("every file 404, retrying next run", "day", d.Format(time.DateOnly))
		return 0, failed + missing, nil
	}
	for _, file := range files {
		var rows []heatmap.Row
		if file.raw != nil {
			var err error
			if rows, err = heatmap.Decode(file.raw, rx); err != nil {
				return 0, 0, fmt.Errorf("%s: %w", file.path, err)
			}
		}
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			src := pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) { return rows[i].Values(), nil })
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{"history_positions"}, heatmap.Columns, src); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO history_files (path, rows) VALUES ($1, $2)", file.path, len(rows))
			return err
		})
		if err != nil {
			return 0, 0, err
		}
	}
	if len(files) > 0 {
		// A local day spans two UTC days of files: refresh the day before too, which
		// completes the partial edge day left by the previous import. Never before the
		// first day: that local day would only ever hold a few hours.
		first := d.AddDate(0, 0, -1)
		if first.Before(firstDay) {
			first = firstDay
		}
		if _, err := conn.Exec(ctx, RefreshDaily, s.TZ.String(), first, d); err != nil {
			return 0, 0, err
		}
		if _, err := conn.Exec(ctx, UpdateOutline, d, d.Add(day)); err != nil {
			return 0, 0, err
		}
	}
	return len(files), failed, nil
}

// ReceiverLocation reads receiver.json; nil when readsb has no location set.
func ReceiverLocation(ctx context.Context, f *feed.Feed) (*heatmap.Receiver, error) {
	var rx struct{ Lat, Lon *float64 }
	if err := f.GetJSON(ctx, f.Receiver(), &rx); err != nil {
		return nil, err
	}
	if rx.Lat == nil || rx.Lon == nil {
		slog.Warn("receiver.json has no location: set it in readsb for range, bearing and the outline")
		return nil, nil
	}
	return &heatmap.Receiver{Lat: *rx.Lat, Lon: *rx.Lon}, nil
}

// Meta reads one db_meta value; "" when unset.
func Meta(ctx context.Context, conn *pgx.Conn, key string) (string, error) {
	var v string
	err := conn.QueryRow(ctx, "SELECT value FROM db_meta WHERE key = $1", key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// Result of one run.
type Result struct{ Imported, Failed int }

// RunOnce imports everything missing up to yesterday (UTC, relative to today), then
// refreshes the derived tables.
func RunOnce(ctx context.Context, conn *pgx.Conn, f *feed.Feed, s config.Settings, today time.Time) (Result, error) {
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	var firstDay time.Time
	if s.HistoryStart != nil {
		firstDay = *s.HistoryStart
	} else if v, err := Meta(ctx, conn, "history_first_day"); err != nil {
		return Result{}, err
	} else if v != "" {
		if firstDay, err = time.Parse(time.DateOnly, v); err != nil {
			return Result{}, err
		}
	}
	if firstDay.IsZero() {
		slog.Info("looking for the oldest heatmap history on the receiver")
		found, err := FindFirstDay(func(d time.Time) (bool, error) { return DayExists(ctx, f, d) }, yesterday, 3650)
		if err != nil {
			return Result{}, err
		}
		if found == nil {
			slog.Warn("no heatmap history found. Enable readsb's heatmap (--heatmap 30 with "+
				"--write-globe-history; ultrafeeder: see the README). Live data still works; "+
				"checking again next run.", "url", f.URL("globe_history/"))
			return Result{}, nil
		}
		firstDay = *found
		if err := migrate.SetMeta(ctx, conn, "history_first_day", firstDay.Format(time.DateOnly)); err != nil {
			return Result{}, err
		}
		slog.Info("history found", "starts", firstDay.Format(time.DateOnly), "days", days(firstDay, yesterday)+1)
	}

	rx, err := ReceiverLocation(ctx, f)
	if err != nil {
		return Result{}, err
	}
	rows, _ := conn.Query(ctx, "SELECT path FROM history_files")
	paths, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return Result{}, err
	}
	done := make(map[string]bool, len(paths))
	for _, p := range paths {
		done[p] = true
	}
	if len(done) > 0 {
		var hasOutline bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM history_outline)").Scan(&hasOutline); err != nil {
			return Result{}, err
		}
		if !hasOutline {
			all := []any{
				pgtype.Timestamptz{InfinityModifier: pgtype.NegativeInfinity, Valid: true},
				pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true},
			}
			if _, err := conn.Exec(ctx, UpdateOutline, all...); err != nil {
				return Result{}, err
			}
		}
	}

	var res Result
	total := days(firstDay, yesterday) + 1
	for d := firstDay; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
		probe.Touch(probe.Alive)
		n, failed, err := ImportDay(ctx, conn, f, d, done, rx, s, firstDay)
		if err != nil {
			return res, err
		}
		if n > 0 {
			slog.Info("imported", "day", d.Format(time.DateOnly), "files", n,
				"progress", fmt.Sprintf("day %d of %d", days(firstDay, d)+1, total))
		}
		res.Imported += n
		res.Failed += failed
	}

	probe.Touch(probe.Alive)
	tag, err := conn.Exec(ctx, FillLiveGaps)
	if err != nil {
		return res, err
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("filled gaps in live positions with history positions", "rows", n)
	}

	// The aircraft db load is long and uninterruptible: start it with a fresh liveness window.
	probe.Touch(probe.Alive)
	// Types and operators are nice to have, never block history.
	if _, err := aircraftdb.Load(ctx, conn, f); err != nil {
		slog.Warn("aircraft database not loaded", "err", err)
	}
	var populated bool
	if err := conn.QueryRow(ctx, "SELECT ispopulated FROM pg_matviews WHERE matviewname = 'flights'").
		Scan(&populated); err != nil {
		return res, err
	}
	if res.Imported > 0 || !populated {
		started := time.Now()
		// The rebuild is the longest single step: the liveness window starts fresh here.
		probe.Touch(probe.Alive)
		if _, err := conn.Exec(ctx, RefreshFlights); err != nil {
			return res, err
		}
		slog.Info("flights rebuilt", "took", time.Since(started).Round(time.Second))
	}
	slog.Info("history run done", "imported", res.Imported, "to_retry", res.Failed)
	return res, nil
}

// sleep waits for d or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// SleepUntil sleeps until wake, touching Alive every minute so a liveness probe can tell
// sleeping from hung. A test's now must advance (from its fake sleep) or this never returns.
func SleepUntil(ctx context.Context, wake time.Time, now func() time.Time,
	nap func(context.Context, time.Duration) error) error {
	for left := wake.Sub(now()); left > 0; left = wake.Sub(now()) {
		probe.Touch(probe.Alive)
		if err := nap(ctx, min(left, time.Minute)); err != nil {
			return err
		}
	}
	return nil
}

// Run is `skyledger history`: a run now, then nightly, until ctx is done. A failed run is retried.
func Run(ctx context.Context, s config.Settings) error {
	f, err := feed.New(s.Tar1090URL, 30*time.Second)
	if err != nil {
		return err
	}
	for {
		err := func() error {
			conn, err := migrate.Connect(ctx, s.DatabaseURL, 10*time.Second)
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
			if _, err := RunOnce(ctx, conn, f, s, time.Now().UTC()); err != nil {
				return err
			}
			probe.Touch(probe.Ready)
			return nil
		}()
		if ctx.Err() != nil {
			return nil
		}
		wake := NextRun(time.Now(), s.HistoryAt, s.TZ)
		if err != nil {
			slog.Error("history run failed", "err", err)
			wake = time.Now().Add(RetryAfter)
		}
		slog.Info("next history run", "at", wake.Format(time.RFC3339))
		if SleepUntil(ctx, wake, time.Now, sleep) != nil {
			return nil
		}
	}
}
