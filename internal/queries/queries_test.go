package queries

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	from = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to   = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
)

func TestGroupedQueriesFillEmptyBucketsWithNull(t *testing.T) {
	// Without a fill, an outage returns no rows and Grafana draws a straight line across it.
	grouped := 0
	for name, sql := range Panel {
		if !strings.Contains(sql, "$__timeGroupAlias") {
			continue
		}
		grouped++
		if !strings.Contains(sql, "$__timeGroupAlias(time, $__interval, NULL)") {
			t.Errorf("%s does not fill with NULL", name)
		}
	}
	if grouped != 6 {
		t.Errorf("%d grouped queries, want 6", grouped)
	}
}

func TestExpandFilterAndGroup(t *testing.T) {
	sql, args, err := Expand("SELECT $__timeGroupAlias(time, $__interval, NULL), count(*) FROM p WHERE $__timeFilter(p.time)",
		from, to, "5 minutes")
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT time_bucket($3::interval, time) AS time, count(*) FROM p WHERE p.time BETWEEN $1 AND $2"
	if sql != want {
		t.Errorf("sql\n got %s\nwant %s", sql, want)
	}
	if !slices.Equal(args, []any{from, to, "5 minutes"}) {
		t.Errorf("args %v", args)
	}
}

func TestExpandGroupOnly(t *testing.T) {
	sql, args, err := Expand("SELECT $__timeGroupAlias(time, $__interval) FROM p", from, to, "1 hour")
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT time_bucket($1::interval, time) AS time FROM p" {
		t.Errorf("sql %s", sql)
	}
	if !slices.Equal(args, []any{"1 hour"}) {
		t.Errorf("args %v", args)
	}
}

func TestExpandNoMacros(t *testing.T) {
	sql, args, err := Expand(Panel["rx_gain"], from, to, "5 minutes")
	if err != nil {
		t.Fatal(err)
	}
	if sql != Panel["rx_gain"] || len(args) != 0 {
		t.Errorf("sql %s args %v", sql, args)
	}
}

func TestExpandRejectsUnknownMacro(t *testing.T) {
	_, _, err := Expand("SELECT 1 WHERE now() > $__timeFrom()", from, to, "5 minutes")
	if err == nil || !strings.Contains(err.Error(), "$__timeFrom") {
		t.Errorf("err %v", err)
	}
}

func TestExpandEveryPanel(t *testing.T) {
	param := regexp.MustCompile(`\$(\d+)`)
	for name, raw := range Panel {
		sql, args, err := Expand(raw, from, to, "5 minutes")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if strings.Contains(sql, "$__") {
			t.Errorf("%s: macro left in %s", name, sql)
		}
		highest := 0
		for _, m := range param.FindAllStringSubmatch(sql, -1) {
			n, _ := strconv.Atoi(m[1])
			highest = max(highest, n)
		}
		if highest != len(args) {
			t.Errorf("%s: highest parameter $%d, %d args", name, highest, len(args))
		}
	}
}
