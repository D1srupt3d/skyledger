// Package config reads settings from environment variables, validated once at startup.
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	// The image has no zoneinfo files; this embeds the time zone database in the binary.
	_ "time/tzdata"
)

// Settings for every skyledger command. Dates are UTC midnights.
type Settings struct {
	Tar1090URL        string
	DatabaseURL       string
	TZ                *time.Location
	RetentionDays     int
	Poll              time.Duration
	FetchPause        time.Duration
	HistoryAt         time.Time // only the clock time is used
	HistoryStart      *time.Time
	GrafanaDBPassword string
}

// FromEnv builds Settings from a lookup such as os.LookupEnv. Errors name the variable.
func FromEnv(lookup func(string) (string, bool)) (Settings, error) {
	get := func(name, def string) string {
		v, _ := lookup(name)
		if v = strings.TrimSpace(v); v == "" {
			return def
		}
		return v
	}
	var s Settings

	u := get("TAR1090_URL", "")
	if u == "" {
		return s, fmt.Errorf("TAR1090_URL is required: the base URL of your tar1090, e.g. http://ultrafeeder/")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return s, fmt.Errorf("TAR1090_URL must start with http:// or https://, got '%s'", u)
	}
	// Everything else is joined onto it, so a missing slash would drop the last path segment.
	if !strings.HasSuffix(u, "/") {
		u += "/"
	}
	s.Tar1090URL = u

	s.DatabaseURL = get("DATABASE_URL", "")
	if s.DatabaseURL == "" {
		password := get("POSTGRES_PASSWORD", "")
		if password == "" {
			return s, fmt.Errorf("POSTGRES_PASSWORD (or DATABASE_URL) is required")
		}
		s.DatabaseURL = fmt.Sprintf("postgresql://%s:%s@%s:%s/%s",
			quote(get("POSTGRES_USER", "skyledger")), quote(password),
			get("POSTGRES_HOST", "timescaledb"), get("POSTGRES_PORT", "5432"),
			quote(get("POSTGRES_DB", "skyledger")))
	}

	tzName := get("TZ", "UTC")
	tz, err := time.LoadLocation(tzName)
	// "Local" would mean the container's own zone, which is never what the user meant.
	if err != nil || tzName == "Local" {
		return s, fmt.Errorf("TZ '%s' is not a known time zone (e.g. Europe/London)", tzName)
	}
	s.TZ = tz

	days, err := number(get, "RETENTION_DAYS", 90, 1, true)
	if err != nil {
		return s, err
	}
	s.RetentionDays = int(days)
	poll, err := number(get, "POLL_SECONDS", 10, 1, false)
	if err != nil {
		return s, err
	}
	s.Poll = seconds(poll)
	pause, err := number(get, "FETCH_PAUSE", 0.2, 0, false)
	if err != nil {
		return s, err
	}
	s.FetchPause = seconds(pause)

	at := get("HISTORY_AT", "01:15")
	if s.HistoryAt, err = parseClock(at); err != nil {
		return s, fmt.Errorf("HISTORY_AT must be HH:MM, got '%s'", at)
	}

	if start := get("HISTORY_START", ""); start != "" && !strings.EqualFold(start, "auto") {
		d, err := time.Parse(time.DateOnly, start)
		if err != nil {
			return s, fmt.Errorf("HISTORY_START must be YYYY-MM-DD or auto, got '%s'", start)
		}
		s.HistoryStart = &d
	}
	s.GrafanaDBPassword = get("GRAFANA_DB_PASSWORD", "")
	return s, nil
}

// quote percent-encodes everything but letters, digits and -_.~ (a "/" or "@" in a password must not end the field).
func quote(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// number parses a setting; whole rejects a fraction, as Python's int() did.
func number(get func(string, string) string, name string, def, minimum float64, whole bool) (float64, error) {
	raw := get(name, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if whole {
		var n int
		n, err = strconv.Atoi(raw)
		v = float64(n)
	}
	if err != nil {
		return 0, fmt.Errorf("%s must be a number, got '%s'", name, raw)
	}
	if v < minimum {
		return 0, fmt.Errorf("%s must be at least %v, got '%s'", name, minimum, raw)
	}
	return v, nil
}

func seconds(v float64) time.Duration { return time.Duration(v * float64(time.Second)) }

func parseClock(s string) (time.Time, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		t, err = time.Parse("15:04:05", s)
	}
	return t, err
}
