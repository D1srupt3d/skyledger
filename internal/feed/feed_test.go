package feed

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

func server(t *testing.T) string {
	files := map[string][]byte{
		"/tar1090/data/aircraft.json": []byte(`{"now": 1, "aircraft": []}`),
		"/tar1090/db-1/A.js":          gz([]byte(`{"children": []}`)), // gzipped body, no Content-Encoding
		"/bad.gz":                     {0x1f, 0x8b, 0},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boom" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func mustNew(t *testing.T, base string) *Feed {
	t.Helper()
	f, err := New(base, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestURLsKeepSubpath(t *testing.T) {
	f := mustNew(t, "http://r/tar1090")
	day := time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)
	for got, want := range map[string]string{
		f.Aircraft():                "http://r/tar1090/data/aircraft.json",
		f.Stats():                   "http://r/tar1090/data/stats.json",
		f.Receiver():                "http://r/tar1090/data/receiver.json",
		f.Index():                   "http://r/tar1090/",
		f.Heatmap(day, 7):           "http://r/tar1090/globe_history/2025/03/04/heatmap/07.bin.ttf",
		f.DB("db-3.14", "files.js"): "http://r/tar1090/db-3.14/files.js",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}

func TestRootURL(t *testing.T) {
	if got := mustNew(t, "http://ultrafeeder/").Aircraft(); got != "http://ultrafeeder/data/aircraft.json" {
		t.Error(got)
	}
}

func TestBadBaseURL(t *testing.T) {
	if _, err := New("http://r/%zz", time.Second); err == nil {
		t.Error("want an error")
	}
}

func TestPlainAndGzipped(t *testing.T) {
	f := mustNew(t, server(t)+"/tar1090/")
	var a struct {
		Now      int   `json:"now"`
		Aircraft []any `json:"aircraft"`
	}
	if err := f.GetJSON(context.Background(), f.Aircraft(), &a); err != nil || a.Now != 1 || len(a.Aircraft) != 0 {
		t.Errorf("%+v %v", a, err)
	}
	var shard map[string][]string
	if err := f.GetJSON(context.Background(), f.DB("db-1", "A.js"), &shard); err != nil || shard["children"] == nil {
		t.Errorf("%v %v", shard, err)
	}
}

func Test404IsNotFound(t *testing.T) {
	f := mustNew(t, server(t)+"/tar1090/")
	_, err := f.Get(context.Background(), f.Heatmap(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), 0))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v", err)
	}
	if err := f.GetJSON(context.Background(), f.Stats(), new(any)); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJSON: got %v", err)
	}
}

func TestOtherErrorsPropagate(t *testing.T) {
	base := server(t)
	f := mustNew(t, base)
	_, err := f.Get(context.Background(), base+"/boom")
	if err == nil || errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("got %v", err)
	}
	if _, err := f.Get(context.Background(), base+"/bad.gz"); err == nil {
		t.Error("corrupt gzip: want an error")
	}
	if _, err := f.Get(context.Background(), "http://127.0.0.1:1/"); err == nil {
		t.Error("connection refused: want an error")
	}
	if _, err := f.Get(context.Background(), "://bad"); err == nil {
		t.Error("bad url: want an error")
	}
}
