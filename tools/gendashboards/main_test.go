package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/D1srupt3d/skyledger/internal/dashboards"
)

// Tests run with the package directory as the working directory.
var chartDir, _ = filepath.Abs(filepath.Join("..", "..", "charts", "skyledger", "grafana", "dashboards"))

// errWriter fails every write, like a closed stdout.
type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestCommittedFilesAreCurrent(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := cli([]string{"--check"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--check = %d: %s%s", code, stdout.String(), stderr.String())
	}
}

func TestDashboardsLiveInTheChart(t *testing.T) {
	got, err := outDir()
	if err != nil || got != chartDir {
		t.Errorf("outDir() = %q, %v; want %q", got, err, chartDir)
	}
}

func TestOutsideTheRepoFails(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := outDir(); err == nil {
		t.Error("outDir() outside a module succeeded")
	}
	var stdout, stderr bytes.Buffer
	if code := cli(nil, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "go.mod") {
		t.Errorf("cli outside the repo = %d, stderr %q", code, stderr.String())
	}
}

func TestWriteThenCheck(t *testing.T) {
	out := filepath.Join(t.TempDir(), "dashboards") // does not exist yet
	var stdout bytes.Buffer
	if code, err := run(nil, out, &stdout); code != 0 || err != nil {
		t.Fatalf("write = %d, %v", code, err)
	}
	files, err := dashboards.Render()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s not written as rendered: %v", name, err)
		}
		if !strings.Contains(stdout.String(), "wrote dashboards/"+name+"\n") {
			t.Errorf("stdout does not report %s: %q", name, stdout.String())
		}
	}
	check := func() (int, string) {
		var stdout bytes.Buffer
		code, err := run([]string{"--check"}, out, &stdout)
		if err != nil {
			t.Fatal(err)
		}
		return code, stdout.String()
	}
	if code, msg := check(); code != 0 {
		t.Fatalf("--check after write = %d: %s", code, msg)
	}

	stale := filepath.Join(out, "skyledger-history.json")
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, msg := check(); code != 1 || !strings.Contains(msg, "[skyledger-history.json] []") {
		t.Errorf("--check with an edited file = %d, %q", code, msg)
	}

	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "old.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, msg := check(); code != 1 || !strings.Contains(msg, "[skyledger-history.json] [old.json]") {
		t.Errorf("--check with a missing and an extra file = %d, %q", code, msg)
	}
}

func TestWriteFailsWhenOutIsAFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(out, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if code, err := run(nil, out, &stdout); code != 1 || err == nil {
		t.Errorf("write into a file = %d, %v", code, err)
	}
}

func TestStdoutFailuresAreErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"--sql"}} {
		if code, err := run(args, t.TempDir(), errWriter{}); code != 1 || err == nil {
			t.Errorf("%v with a broken stdout = %d, %v", args, code, err)
		}
	}
}

func TestSQLPrintsAllQueries(t *testing.T) {
	var stdout bytes.Buffer
	if code, err := run([]string{"--sql"}, t.TempDir(), &stdout); code != 0 || err != nil {
		t.Fatalf("--sql = %d, %v", code, err)
	}
	var got map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, dashboards.AllQueries()) {
		t.Error("--sql output differs from AllQueries()")
	}
	// Readable SQL: "<" is printed as is, not HTML-escaped by encoding/json.
	if !strings.Contains(stdout.String(), "nic < 7") {
		t.Error("--sql output HTML-escapes the SQL")
	}
}

func TestUnknownArgsWriteNothing(t *testing.T) {
	out := t.TempDir()
	var stdout bytes.Buffer
	if code, err := run([]string{"--chek"}, out, &stdout); code != 2 || err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("--chek = %d, %v", code, err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("--chek wrote %d files", len(entries))
	}
	var stderr bytes.Buffer
	if code := cli([]string{"--check", "--sql"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage") {
		t.Errorf("cli --check --sql = %d, stderr %q", code, stderr.String())
	}
}
