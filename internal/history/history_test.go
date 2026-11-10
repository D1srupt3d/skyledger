package history

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/D1srupt3d/skyledger/internal/config"
	"github.com/D1srupt3d/skyledger/internal/feed"
	"github.com/D1srupt3d/skyledger/internal/probe"
)

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func at(h, m int) time.Time { return time.Date(0, 1, 1, h, m, 0, 0, time.UTC) }

func london(t *testing.T) *time.Location {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestNextRunLaterTodayAndTomorrow(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 30, 0, 0, time.UTC)
	if got := NextRun(now, at(1, 15), time.UTC); !got.Equal(time.Date(2026, 1, 10, 1, 15, 0, 0, time.UTC)) {
		t.Error(got)
	}
	now = time.Date(2026, 1, 10, 1, 15, 0, 0, time.UTC) // exactly at: next day
	if got := NextRun(now, at(1, 15), time.UTC); !got.Equal(time.Date(2026, 1, 11, 1, 15, 0, 0, time.UTC)) {
		t.Error(got)
	}
}

func TestNextRunUsesLocalTime(t *testing.T) {
	// 23:30 UTC on 2026-06-30 is 00:30 on 2026-07-01 in London (BST): next 01:15 is 00:15 UTC.
	got := NextRun(time.Date(2026, 6, 30, 23, 30, 0, 0, time.UTC), at(1, 15), london(t))
	if !got.Equal(time.Date(2026, 7, 1, 0, 15, 0, 0, time.UTC)) {
		t.Error(got.UTC())
	}
}

func TestNextRunAcrossDSTChange(t *testing.T) {
	// Clocks go forward in London on 2026-03-29; 01:15 local that morning doesn't exist.
	// The run still lands on that day, once.
	got := NextRun(time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC), at(1, 15), london(t))
	if y, m, d := got.Date(); y != 2026 || m != 3 || d != 29 {
		t.Error(got)
	}
}

func existsBetween(first, last time.Time, gaps map[time.Time]bool) func(time.Time) (bool, error) {
	return func(d time.Time) (bool, error) { return !d.Before(first) && !d.After(last) && !gaps[d], nil }
}

func mustFind(t *testing.T, exists func(time.Time) (bool, error), y time.Time, horizon int) *time.Time {
	t.Helper()
	got, err := FindFirstDay(exists, y, horizon)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestFindFirstDay(t *testing.T) {
	y := date(2026, 9, 24)
	if got := mustFind(t, existsBetween(date(2024, 12, 12), y, nil), y, 3650); got == nil || !got.Equal(date(2024, 12, 12)) {
		t.Error(got)
	}
}

func TestFindFirstDayStepsOverGaps(t *testing.T) {
	y, first := date(2026, 9, 24), date(2025, 3, 1)
	gaps := map[time.Time]bool{}
	for k := 2; k < 9; k++ { // receiver off for a week, early on
		gaps[first.AddDate(0, 0, k)] = true
	}
	if got := mustFind(t, existsBetween(first, y, gaps), y, 3650); got == nil || !got.Equal(first) {
		t.Error(got)
	}
}

func TestFindFirstDayNone(t *testing.T) {
	if got := mustFind(t, func(time.Time) (bool, error) { return false, nil }, date(2026, 9, 24), 3650); got != nil {
		t.Error(got)
	}
}

func TestFindFirstDayOlderThanHorizon(t *testing.T) {
	y := date(2026, 9, 24)
	if got := mustFind(t, func(time.Time) (bool, error) { return true, nil }, y, 100); !got.Equal(y.AddDate(0, 0, -100)) {
		t.Error(got)
	}
}

func TestFindFirstDayErrorsPropagate(t *testing.T) {
	boom := errors.New("boom")
	y := date(2026, 9, 24)
	// Fail at the n-th probe, to reach each place FindFirstDay asks.
	for n := 1; n <= 30; n++ {
		calls := 0
		exists := func(d time.Time) (bool, error) {
			if calls++; calls == n {
				return false, boom
			}
			return !d.Before(date(2026, 1, 1)), nil
		}
		if _, err := FindFirstDay(exists, y, 3650); !errors.Is(err, boom) && calls >= n {
			t.Errorf("failure at probe %d not returned: %v", n, err)
		}
	}
}

func notFoundFeed(t *testing.T) *feed.Feed {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	f, err := feed.New(srv.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func settings(t *testing.T) config.Settings {
	s, err := config.FromEnv(func(k string) (string, bool) {
		v, ok := map[string]string{"TAR1090_URL": "http://r/", "POSTGRES_PASSWORD": "x", "FETCH_PAUSE": "0"}[k]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The conn is nil: ImportDay must not touch the database when it records nothing (a nil conn panics).
func TestWholeDayOf404IsRetriedNotRecorded(t *testing.T) {
	d := date(2026, 1, 5)
	n, failed, err := ImportDay(context.Background(), nil, notFoundFeed(t), d, map[string]bool{}, nil, settings(t), d)
	if n != 0 || failed != 48 || err != nil {
		t.Errorf("%d %d %v", n, failed, err)
	}
}

func TestAlreadyImportedFilesAreSkipped(t *testing.T) {
	d := date(2026, 1, 5)
	done := map[string]bool{}
	for h := range 48 {
		done[Path(d, h)] = true
	}
	n, failed, err := ImportDay(context.Background(), nil, notFoundFeed(t), d, done, nil, settings(t), d)
	if n != 0 || failed != 0 || err != nil {
		t.Errorf("%d %d %v", n, failed, err)
	}
}

func TestDayExistsAndErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/globe_history/2026/01/05/heatmap/24.bin.ttf":
			_, _ = w.Write(nil)
		case "/globe_history/2026/01/06/heatmap/00.bin.ttf":
			http.Error(w, "boom", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f, _ := feed.New(srv.URL, 5*time.Second)
	ctx := context.Background()
	if ok, err := DayExists(ctx, f, date(2026, 1, 5)); !ok || err != nil {
		t.Errorf("found day: %v %v", ok, err)
	}
	if ok, err := DayExists(ctx, f, date(2026, 1, 4)); ok || err != nil {
		t.Errorf("missing day: %v %v", ok, err)
	}
	if _, err := DayExists(ctx, f, date(2026, 1, 6)); err == nil {
		t.Error("server error: want an error")
	}
}

func TestImportDayStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := date(2026, 1, 5)
	if _, _, err := ImportDay(ctx, nil, notFoundFeed(t), d, map[string]bool{}, nil, settings(t), d); err == nil {
		t.Error("want the context error")
	}
}

func redirectProbes(t *testing.T) string {
	dir := t.TempDir()
	probe.Alive, probe.Ready = filepath.Join(dir, "alive"), filepath.Join(dir, "ready")
	t.Cleanup(func() { probe.Alive, probe.Ready = "/tmp/alive", "/tmp/ready" })
	return dir
}

func TestSleepUntilTouchesAliveEveryMinute(t *testing.T) {
	redirectProbes(t)
	clock := time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC)
	var naps []time.Duration
	nap := func(_ context.Context, d time.Duration) error {
		naps = append(naps, d)
		clock = clock.Add(d)
		_ = os.Remove(probe.Alive) // each nap must be preceded by a fresh touch
		return nil
	}
	touches := 0
	now := func() time.Time {
		if _, err := os.Stat(probe.Alive); err == nil {
			touches++
		}
		return clock
	}
	if err := SleepUntil(context.Background(), clock.Add(150*time.Second), now, nap); err != nil {
		t.Fatal(err)
	}
	if len(naps) != 3 || naps[0] != time.Minute || naps[1] != time.Minute || naps[2] != 30*time.Second {
		t.Errorf("naps %v", naps)
	}
	if touches != 0 { // touched before each nap, removed by it: never seen between
		t.Errorf("touches seen %d", touches)
	}
}

func TestSleepUntilInThePastReturnsAtOnce(t *testing.T) {
	redirectProbes(t)
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	nap := func(context.Context, time.Duration) error { t.Fatal("slept"); return nil }
	if err := SleepUntil(context.Background(), now.Add(-5*time.Second), func() time.Time { return now }, nap); err != nil {
		t.Fatal(err)
	}
}

func TestSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	if err := sleep(ctx, time.Millisecond); err != nil {
		t.Error(err)
	}
	if err := sleep(ctx, 0); err != nil {
		t.Error(err)
	}
	cancel()
	if err := sleep(ctx, time.Hour); err == nil {
		t.Error("want the context error")
	}
	if err := SleepUntil(ctx, time.Now().Add(time.Hour), time.Now, sleep); err == nil {
		t.Error("SleepUntil: want the context error")
	}
}

// A run that can't reach the database is retried, not fatal; Run returns when ctx ends.
func TestRunRetriesFailedRuns(t *testing.T) {
	redirectProbes(t)
	s := settings(t)
	s.DatabaseURL = "postgresql://u:p@127.0.0.1:1/d"
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(probe.Alive); err != nil {
		t.Error("alive not touched while waiting to retry")
	}
	s.Tar1090URL = "http://r/%zz"
	if err := Run(ctx, s); err == nil {
		t.Error("bad URL: want an error")
	}
}
