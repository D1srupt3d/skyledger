// Command gendashboards writes skyledger's Grafana dashboards into
// charts/skyledger/grafana/dashboards/ (see internal/dashboards).
//
// Usage: go run ./tools/gendashboards [--check | --sql]
//
//	(no args)  write the dashboard files
//	--check    exit 1 if the committed files differ from what this generates (CI)
//	--sql      print every panel query as JSON (tests)
//
// It finds the repo root by walking up from the working directory to go.mod,
// so it works from any directory inside the repo.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/D1srupt3d/skyledger/internal/dashboards"
)

func main() { os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr)) }

// cli runs against the repo's chart directory and reports any error on stderr.
func cli(args []string, stdout, stderr io.Writer) int {
	out, err := outDir()
	code := 1
	if err == nil {
		code, err = run(args, out, stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
	}
	return code
}

// outDir is charts/skyledger/grafana/dashboards under the nearest directory,
// at or above the working directory, that holds go.mod.
func outDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "charts", "skyledger", "grafana", "dashboards"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("gendashboards: no go.mod at or above the working directory; run it inside the repo")
		}
		dir = parent
	}
}

func run(args []string, out string, stdout io.Writer) (int, error) {
	files, err := dashboards.Render()
	if err != nil {
		return 1, err
	}
	switch strings.Join(args, " ") {
	case "--sql":
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(dashboards.AllQueries()); err != nil {
			return 1, err
		}
		return 0, nil
	case "--check":
		var stale, extra []string
		for _, name := range slices.Sorted(maps.Keys(files)) {
			got, err := os.ReadFile(filepath.Join(out, name))
			if err != nil || !bytes.Equal(got, files[name]) {
				stale = append(stale, name)
			}
		}
		matches, _ := filepath.Glob(filepath.Join(out, "*.json")) // only errors on a bad pattern
		for _, m := range matches {
			if _, ok := files[filepath.Base(m)]; !ok {
				extra = append(extra, filepath.Base(m))
			}
		}
		if len(stale) > 0 || len(extra) > 0 {
			_, err := fmt.Fprintf(stdout, "dashboards out of date: %v %v. Run: go run ./tools/gendashboards\n", stale, extra)
			return 1, err
		}
		return 0, nil
	case "":
		if err := os.MkdirAll(out, 0o755); err != nil {
			return 1, err
		}
		for _, name := range slices.Sorted(maps.Keys(files)) {
			if err := os.WriteFile(filepath.Join(out, name), files[name], 0o644); err != nil {
				return 1, err
			}
			if _, err := fmt.Fprintf(stdout, "wrote %s/%s\n", filepath.Base(out), name); err != nil {
				return 1, err
			}
		}
		return 0, nil
	default:
		return 2, errors.New("usage: gendashboards [--check | --sql]")
	}
}
