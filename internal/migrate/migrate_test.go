package migrate

import (
	"regexp"
	"strings"
	"testing"
)

func TestShippedMigrationsAreWellFormed(t *testing.T) {
	found, err := Discover()
	if err != nil || len(found) == 0 || found[0] != (Migration{1, "0001_init.sql"}) {
		t.Fatalf("%v %v", found, err)
	}
	for i, m := range found {
		if m.Version != i+1 {
			t.Errorf("versions must be contiguous: %v", found)
		}
		if sql, err := Read(m.Name); err != nil || strings.TrimSpace(sql) == "" {
			t.Errorf("%s: empty or unreadable: %v", m.Name, err)
		}
	}
}

func TestOrderIsNumeric(t *testing.T) {
	got, err := Discover("0010_b.sql", "0002_a.sql")
	if err != nil || len(got) != 2 || got[0] != (Migration{2, "0002_a.sql"}) || got[1] != (Migration{10, "0010_b.sql"}) {
		t.Errorf("%v %v", got, err)
	}
}

func TestRejects(t *testing.T) {
	for _, c := range []struct {
		names   []string
		message string
	}{
		{[]string{"1_init.sql"}, "bad migration file name"},
		{[]string{"0001_Init.sql"}, "bad migration file name"},
		{[]string{"0001_a.sql", "0001_b.sql"}, "two migrations with version 1"},
	} {
		if _, err := Discover(c.names...); err == nil || !strings.Contains(err.Error(), c.message) {
			t.Errorf("%v: got %v", c.names, err)
		}
	}
}

func TestReadMissing(t *testing.T) {
	if _, err := Read("9999_nope.sql"); err == nil {
		t.Error("want an error")
	}
}

func mustRead(t *testing.T, n string) string {
	t.Helper()
	s, err := Read(n)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInitUsesCurrentColumnstoreAPI(t *testing.T) {
	init := mustRead(t, "0001_init.sql")
	// add_compression_policy / timescaledb.compress are deprecated since TimescaleDB 2.18.
	if strings.Contains(init, "add_compression_policy") || strings.Contains(init, "timescaledb.compress ") {
		t.Error("deprecated compression API")
	}
	if n := strings.Count(init, "add_columnstore_policy"); n != 3 {
		t.Errorf("%d columnstore policies, want 3", n)
	}
}

func TestNoIndexCollidesWithCreateHypertablesDefault(t *testing.T) {
	// create_hypertable() makes "<table>_time_idx" unless told not to; a CREATE INDEX with that
	// name then fails the whole migration (it did, in the first CI run).
	sql := mustRead(t, "0001_init.sql")
	hyper := regexp.MustCompile(`create_hypertable\('(\w+)',([^;]*)\);`)
	matches := hyper.FindAllStringSubmatch(sql, -1)
	if len(matches) != 3 {
		t.Fatalf("%d hypertables, want 3", len(matches))
	}
	for _, m := range matches {
		if !strings.Contains(m[2], "create_default_indexes => false") && strings.Contains(sql, "INDEX "+m[1]+"_time_idx ") {
			t.Error(m[1])
		}
	}
}
