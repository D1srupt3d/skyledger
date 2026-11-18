package web

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestVendoredAssetsAreServed(t *testing.T) {
	h := Handler(&fakeDB{}, time.UTC)
	cases := map[string]string{
		"/vendor/uplot/uPlot.iife.min.js": "text/javascript; charset=utf-8",
		"/vendor/uplot/uPlot.min.css":     "text/css; charset=utf-8",
	}
	for _, f := range []string{"barlow-400", "barlow-500", "barlow-600", "barlow-condensed-500", "barlow-condensed-600", "barlow-condensed-700"} {
		cases["/fonts/"+f+".woff2"] = "font/woff2"
	}
	for p, want := range cases {
		rec := get(t, h, "GET", p)
		if rec.Code != 200 {
			t.Errorf("%s: status %d", p, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != want {
			t.Errorf("%s: Content-Type %q, want %q", p, ct, want)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s: empty body", p)
		}
	}
}

func TestLicensesShipWithVendoredCode(t *testing.T) {
	for p, want := range map[string]string{
		"ui/vendor/uplot/LICENSE":           "MIT",
		"ui/fonts/OFL.txt":                  "SIL Open Font License",
		"ui/fonts/OFL-barlow-condensed.txt": "SIL Open Font License",
	} {
		b, err := embedded.ReadFile(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
		} else if !strings.Contains(string(b), want) {
			t.Errorf("%s does not mention %q", p, want)
		}
	}
}

// The UI must work offline: our own html, css and js may not point anywhere but the SVG
// namespace. vendor/ is skipped because uPlot's header comment names its source URL.
func TestNoExternalURLs(t *testing.T) {
	url := regexp.MustCompile(`https?://[^\s"'<>)]*`)
	checked := 0
	err := fs.WalkDir(embedded, "ui", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == "ui/vendor" {
				return fs.SkipDir
			}
			return nil
		}
		switch path.Ext(p) {
		case ".html", ".css", ".js":
		default:
			return nil
		}
		checked++
		b, err := embedded.ReadFile(p)
		if err != nil {
			return err
		}
		for _, u := range url.FindAllString(string(b), -1) {
			if u != "http://www.w3.org/2000/svg" {
				t.Errorf("%s references %s", p, u)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Error("no files checked")
	}
}
