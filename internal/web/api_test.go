package web

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStatusLevel(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		age  *float64
		want string
	}{
		{nil, "silent"}, {f(0), "live"}, {f(119.9), "live"},
		{f(120), "stale"}, {f(899.9), "stale"}, {f(900), "silent"},
	} {
		if got := statusLevel(c.age); got != c.want {
			t.Errorf("%v: got %q want %q", c.age, got, c.want)
		}
	}
}

func TestLocalMidnightAcrossDST(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skip("no tzdata")
	}
	for _, c := range []struct{ now, want string }{
		{"2026-03-29T12:00:00Z", "2026-03-29T00:00:00Z"},
		{"2026-03-30T12:00:00Z", "2026-03-29T23:00:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, c.now)
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := localMidnight(now, london); !got.Equal(want) {
			t.Errorf("%s: got %s want %s", c.now, got.UTC(), want)
		}
	}
}

func TestBucketsFillGapsWithNull(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0) // a multiple of 300
	want := []int64{t0.Unix(), t0.Unix() + 300, t0.Unix() + 600, t0.Unix() + 900}
	for _, from := range []time.Time{t0, t0.Add(100 * time.Second)} {
		got := bucketTimes(from, t0.Add(15*time.Minute), 5*time.Minute)
		if len(got) != len(want) {
			t.Fatalf("from %v: got %v want %v", from, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("from %v: got %v want %v", from, got, want)
			}
		}
	}
}

func TestCacheReusesAndExpires(t *testing.T) {
	c := &cache{ttl: 50 * time.Millisecond}
	n := 0
	build := func(context.Context) (any, error) { n++; return map[string]int{"n": n}, nil }
	ctx := context.Background()
	a, _ := c.get(ctx, build)
	b, _ := c.get(ctx, build)
	if n != 1 || string(a) != string(b) {
		t.Fatalf("builds %d, %s vs %s", n, a, b)
	}
	time.Sleep(60 * time.Millisecond)
	if _, _ = c.get(ctx, build); n != 2 {
		t.Errorf("expired entry not rebuilt: %d builds", n)
	}

	e := &cache{ttl: time.Hour}
	fails := 0
	bad := func(context.Context) (any, error) { fails++; return nil, errors.New("boom") }
	for range 2 {
		if _, err := e.get(ctx, bad); err == nil {
			t.Error("want error")
		}
	}
	if fails != 2 {
		t.Errorf("error was cached: %d builds", fails)
	}
}

func TestAPIErrorsHideDetails(t *testing.T) {
	db := &fakeDB{err: errors.New("dial tcp 10.0.0.5:5432: password authentication failed for user skyledger_grafana")}
	rec := get(t, Handler(db, time.UTC), "GET", "/api/live")
	if rec.Code != 500 || rec.Header().Get("Content-Type") != "application/json" ||
		rec.Body.String() != "{\"error\":\"database query failed\"}\n" {
		t.Errorf("%d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := Handler(&fakeDB{}, time.UTC)
	for _, path := range []string{"/", "/healthz", "/nope"} {
		rec := get(t, h, "GET", path)
		for k, v := range map[string]string{
			"Content-Security-Policy": "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:",
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "no-referrer",
		} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s %s: %q", path, k, got)
			}
		}
	}
}
