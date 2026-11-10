package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func with(extra map[string]string) map[string]string {
	m := map[string]string{"TAR1090_URL": "http://receiver/tar1090", "POSTGRES_PASSWORD": "pw"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func mustLoad(t *testing.T, m map[string]string) Settings {
	t.Helper()
	s, err := FromEnv(env(m), true)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDefaultsAndDerivedValues(t *testing.T) {
	s := mustLoad(t, with(nil))
	if s.Tar1090URL != "http://receiver/tar1090/" { // trailing slash added
		t.Errorf("url %q", s.Tar1090URL)
	}
	if s.DatabaseURL != "postgresql://skyledger:pw@timescaledb:5432/skyledger" {
		t.Errorf("database url %q", s.DatabaseURL)
	}
	if s.TZ.String() != "UTC" || s.RetentionDays != 90 || s.Poll != 10*time.Second ||
		s.FetchPause != 200*time.Millisecond {
		t.Errorf("defaults %+v", s)
	}
	if s.HistoryAt.Hour() != 1 || s.HistoryAt.Minute() != 15 || s.HistoryStart != nil {
		t.Errorf("history %v %v", s.HistoryAt, s.HistoryStart)
	}
}

func TestPasswordIsURLQuoted(t *testing.T) {
	s := mustLoad(t, with(map[string]string{"POSTGRES_PASSWORD": "p@ss/word x"}))
	if !strings.Contains(s.DatabaseURL, ":p%40ss%2Fword%20x@") {
		t.Error(s.DatabaseURL)
	}
}

func TestDatabaseURLWins(t *testing.T) {
	s := mustLoad(t, map[string]string{"TAR1090_URL": "http://r/", "DATABASE_URL": "postgresql://u:p@h/d"})
	if s.DatabaseURL != "postgresql://u:p@h/d" {
		t.Error(s.DatabaseURL)
	}
}

func TestOverrides(t *testing.T) {
	s := mustLoad(t, with(map[string]string{"TZ": "Europe/London", "RETENTION_DAYS": "30",
		"HISTORY_AT": "03:30", "HISTORY_START": "2025-01-02", "POLL_SECONDS": "5", "FETCH_PAUSE": "0",
		"GRAFANA_DB_PASSWORD": "g"}))
	if s.TZ.String() != "Europe/London" || s.RetentionDays != 30 || s.Poll != 5*time.Second || s.FetchPause != 0 {
		t.Errorf("%+v", s)
	}
	if s.HistoryAt.Hour() != 3 || s.HistoryAt.Minute() != 30 {
		t.Error(s.HistoryAt)
	}
	if s.HistoryStart == nil || !s.HistoryStart.Equal(time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Error(s.HistoryStart)
	}
	if s.ReadOnlyDBPassword != "g" {
		t.Error("read-only password not read from GRAFANA_DB_PASSWORD")
	}
}

func TestHistoryAtWithSeconds(t *testing.T) {
	s := mustLoad(t, with(map[string]string{"HISTORY_AT": "02:03:04"}))
	if s.HistoryAt.Hour() != 2 || s.HistoryAt.Minute() != 3 || s.HistoryAt.Second() != 4 {
		t.Error(s.HistoryAt)
	}
}

func TestHistoryStartAuto(t *testing.T) {
	if s := mustLoad(t, with(map[string]string{"HISTORY_START": "AUTO"})); s.HistoryStart != nil {
		t.Error(s.HistoryStart)
	}
}

func TestInvalid(t *testing.T) {
	cases := []struct {
		env     map[string]string
		message string
	}{
		{map[string]string{"POSTGRES_PASSWORD": "pw"}, "TAR1090_URL is required"},
		{with(map[string]string{"TAR1090_URL": "receiver/"}), "must start with http"},
		{map[string]string{"TAR1090_URL": "http://r/"}, "POSTGRES_PASSWORD"},
		{with(map[string]string{"TZ": "Mars/Olympus"}), "TZ"},
		{with(map[string]string{"TZ": "Local"}), "TZ"},
		{with(map[string]string{"HISTORY_AT": "25:99"}), "HISTORY_AT"},
		{with(map[string]string{"RETENTION_DAYS": "ninety"}), "RETENTION_DAYS must be a number"},
		{with(map[string]string{"RETENTION_DAYS": "1.5"}), "RETENTION_DAYS must be a number"},
		{with(map[string]string{"RETENTION_DAYS": "0"}), "RETENTION_DAYS must be at least 1"},
		{with(map[string]string{"POLL_SECONDS": "0.5"}), "POLL_SECONDS must be at least 1"},
		{with(map[string]string{"FETCH_PAUSE": "soon"}), "FETCH_PAUSE must be a number"},
		{with(map[string]string{"FETCH_PAUSE": "-1"}), "FETCH_PAUSE must be at least 0"},
		{with(map[string]string{"HISTORY_START": "last year"}), "HISTORY_START"},
	}
	for _, c := range cases {
		_, err := FromEnv(env(c.env), true)
		if err == nil || !strings.Contains(err.Error(), c.message) {
			t.Errorf("%v: got %v, want %q", c.env, err, c.message)
		}
	}
}

func TestBlankValuesMeanDefault(t *testing.T) {
	s := mustLoad(t, with(map[string]string{"TZ": "  ", "RETENTION_DAYS": ""}))
	if s.RetentionDays != 90 || s.TZ.String() != "UTC" {
		t.Errorf("%+v", s)
	}
}

func TestReadOnlyPasswordPrimaryName(t *testing.T) {
	if s := mustLoad(t, with(map[string]string{"READONLY_DB_PASSWORD": "a"})); s.ReadOnlyDBPassword != "a" {
		t.Errorf("got %q", s.ReadOnlyDBPassword)
	}
}

func TestReadOnlyPasswordLegacyAlias(t *testing.T) {
	if s := mustLoad(t, with(map[string]string{"GRAFANA_DB_PASSWORD": "b"})); s.ReadOnlyDBPassword != "b" {
		t.Errorf("got %q", s.ReadOnlyDBPassword)
	}
}

func TestReadOnlyPasswordBothSame(t *testing.T) {
	s := mustLoad(t, with(map[string]string{"READONLY_DB_PASSWORD": "c", "GRAFANA_DB_PASSWORD": "c"}))
	if s.ReadOnlyDBPassword != "c" {
		t.Errorf("got %q", s.ReadOnlyDBPassword)
	}
}

func TestReadOnlyPasswordConflict(t *testing.T) {
	_, err := FromEnv(env(with(map[string]string{
		"READONLY_DB_PASSWORD": "s3cret-one", "GRAFANA_DB_PASSWORD": "s3cret-two"})), true)
	if err == nil {
		t.Fatal("no error for two different passwords")
	}
	msg := err.Error()
	if !strings.Contains(msg, "READONLY_DB_PASSWORD") || !strings.Contains(msg, "GRAFANA_DB_PASSWORD") {
		t.Errorf("error should name both variables: %s", msg)
	}
	if strings.Contains(msg, "s3cret") {
		t.Errorf("error leaks a password: %s", msg)
	}
}

func TestTar1090OptionalWithoutFeed(t *testing.T) {
	s, err := FromEnv(env(map[string]string{"POSTGRES_PASSWORD": "pw"}), false)
	if err != nil || s.Tar1090URL != "" {
		t.Errorf("got %q, %v", s.Tar1090URL, err)
	}
	if _, err := FromEnv(env(map[string]string{"POSTGRES_PASSWORD": "pw", "TAR1090_URL": "receiver/"}), false); err == nil {
		t.Error("a set TAR1090_URL should still be validated")
	}
	if _, err := FromEnv(env(map[string]string{"POSTGRES_PASSWORD": "pw"}), true); err == nil ||
		!strings.Contains(err.Error(), "TAR1090_URL is required") {
		t.Errorf("needFeed: %v", err)
	}
}
