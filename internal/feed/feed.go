// Package feed is everything skyledger reads from the receiver, derived from one base URL.
package feed

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound means the receiver answered 404: the file doesn't exist (as opposed to an outage).
var ErrNotFound = errors.New("not found")

type Feed struct {
	base   *url.URL
	client *http.Client
}

func New(base string, timeout time.Duration) (*Feed, error) {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	// No transparent gzip: tar1090 gzips some files whatever the client asks, so Get unwraps by magic instead.
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{DisableCompression: true}}
	return &Feed{base: u, client: client}, nil
}

func (f *Feed) URL(path string) string {
	return f.base.ResolveReference(&url.URL{Path: path}).String()
}

func (f *Feed) Aircraft() string { return f.URL("data/aircraft.json") }
func (f *Feed) Stats() string    { return f.URL("data/stats.json") }
func (f *Feed) Receiver() string { return f.URL("data/receiver.json") }
func (f *Feed) Index() string    { return f.base.String() }

// Heatmap is tar1090's replay file for one UTC half hour (0-47) of day.
func (f *Feed) Heatmap(day time.Time, half int) string {
	return f.URL(fmt.Sprintf("globe_history/%s/heatmap/%02d.bin.ttf", day.Format("2006/01/02"), half))
}

// DB is a file in tar1090's aircraft database folder, e.g. DB("db-3.14.1718", "files.js").
func (f *Feed) DB(version, name string) string { return f.URL(version + "/" + name) }

// Get fetches a URL. A 404 wraps ErrNotFound; any other non-2xx status is an error.
func (f *Feed) Get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s: %w", u, ErrNotFound)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s: HTTP %d", u, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return Gunzip(raw)
}

func (f *Feed) GetJSON(ctx context.Context, u string, v any) error {
	raw, err := f.Get(ctx, u)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// Gunzip returns raw decompressed if it starts with the gzip magic bytes, else unchanged.
func Gunzip(raw []byte) ([]byte, error) {
	if !bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) {
		return raw, nil
	}
	r, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}
