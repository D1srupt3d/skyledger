package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/D1srupt3d/skyledger/internal/version"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func call(args []string, e map[string]string) (int, string, string) {
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	code := run(ctx, args, &out, &errOut, env(e))
	return code, out.String(), errOut.String()
}

func TestVersionAndHelp(t *testing.T) {
	if code, out, _ := call([]string{"--version"}, nil); code != 0 || out != "skyledger "+version.Version+"\n" {
		t.Errorf("%d %q", code, out)
	}
	if code, out, _ := call([]string{"--help"}, nil); code != 0 || !strings.Contains(out, "demo-feed") {
		t.Errorf("%d %q", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	if code, _, errOut := call(nil, nil); code != 2 || !strings.Contains(errOut, "usage:") {
		t.Errorf("no command: %d %q", code, errOut)
	}
	if code, _, errOut := call([]string{"fly"}, nil); code != 2 || !strings.Contains(errOut, "unknown command 'fly'") {
		t.Errorf("unknown: %d %q", code, errOut)
	}
	if code, _, _ := call([]string{"demo-feed", "--nope"}, nil); code != 2 {
		t.Errorf("bad flag: %d", code)
	}
}

func TestConfigErrorExits2(t *testing.T) {
	code, _, errOut := call([]string{"ingest"}, map[string]string{})
	if code != 2 || !strings.Contains(errOut, "skyledger: TAR1090_URL is required") {
		t.Errorf("%d %q", code, errOut)
	}
}

func TestCommandFailureExits1(t *testing.T) {
	e := map[string]string{"TAR1090_URL": "http://r/", "DATABASE_URL": "postgresql://u:p@127.0.0.1:1/d"}
	if code, _, errOut := call([]string{"migrate"}, e); code != 1 || !strings.HasPrefix(errOut, "skyledger: ") {
		t.Errorf("%d %q", code, errOut)
	}
	if code, _, _ := call([]string{"demo-feed", "--port", "-1"}, nil); code != 1 {
		t.Errorf("demo-feed bad port: %d", code)
	}
}

// The long-running commands stop cleanly, exit 0, when the context ends (SIGTERM).
func TestLongRunningCommandsStopOnCancel(t *testing.T) {
	e := map[string]string{"TAR1090_URL": "http://127.0.0.1:1/", "DATABASE_URL": "postgresql://u:p@127.0.0.1:1/d",
		"POLL_SECONDS": "1"}
	for _, cmd := range []string{"ingest", "history"} {
		if code, _, errOut := call([]string{cmd}, e); code != 0 {
			t.Errorf("%s: %d %q", cmd, code, errOut)
		}
	}
}
