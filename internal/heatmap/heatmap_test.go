package heatmap

import (
	"bytes"
	"compress/gzip"
	"math"
	"reflect"
	"testing"
	"time"
)

var rx = &Receiver{40.0, -70.0}

const (
	t0 = int64(1790000040000) // a whole UTC minute, in ms
	a  = int32(0xABC123)
	b  = int32(1<<24 | 0x00BEEF) // a non-ICAO address
)

func f(v float64) *float64 { return &v }

func sample() []byte {
	return Build([]Slice{
		{t0, [][]byte{
			Position(a, 41.0, -70.0, f(35000), f(450), false, 0), // before A's callsign record
			Callsign(a, "TEST1", 1200),
			Position(b, -33.9, 151.2, nil, f(200), false, 5), // mlat, southern latitude, alt unknown
			Callsign(b, "", 7700),                            // squawk only
		}},
		{t0 + 15000, [][]byte{Position(a, 41.01, -70.0, f(35000), f(450), false, 0)}}, // same minute: dropped
		{t0 + 60000, [][]byte{Position(a, 41.02, -70.0, nil, nil, true, 0)}},          // ground, speed unknown
	}, 15000)
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func TestDecodeRules(t *testing.T) {
	rows, err := Decode(sample(), rx)
	if err != nil || len(rows) != 3 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	a0, bb, a1 := rows[0], rows[1], rows[2]
	ts := time.UnixMilli(t0).UTC()
	if !a0.Time.Equal(ts) || a0.Hex != "abc123" || a0.Source != "adsb_icao" {
		t.Errorf("a0 %+v", a0)
	}
	if str(a0.Flight) != "TEST1" || str(a0.Squawk) != "1200" { // earliest-record fallback
		t.Errorf("a0 ident %s %s", str(a0.Flight), str(a0.Squawk))
	}
	if *a0.AltBaro != 35000 || a0.OnGround || *a0.GS != 450.0 {
		t.Errorf("a0 %+v", a0)
	}
	if math.Abs(*a0.DistanceNM-60.0) >= 0.2 || math.Abs(*a0.Bearing) >= 0.01 { // 1 deg due north
		t.Errorf("a0 range %v %v", *a0.DistanceNM, *a0.Bearing)
	}
	if bb.Hex != "~00beef" || bb.Source != "mlat" || bb.Lat != -33.9 || bb.AltBaro != nil || bb.OnGround {
		t.Errorf("b %+v", bb)
	}
	if bb.Flight != nil || str(bb.Squawk) != "7700" {
		t.Errorf("b ident %s %s", str(bb.Flight), str(bb.Squawk))
	}
	if !a1.Time.Equal(ts.Add(time.Minute)) || !a1.OnGround || a1.AltBaro != nil || a1.GS != nil {
		t.Errorf("a1 %+v", a1)
	}
	if str(a1.Flight) != "TEST1" {
		t.Errorf("a1 flight %s", str(a1.Flight))
	}
	if len(a0.Values()) != len(Columns) {
		t.Errorf("Values has %d fields, Columns %d", len(a0.Values()), len(Columns))
	}
}

func TestGzipAccepted(t *testing.T) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(sample())
	_ = w.Close()
	got, err1 := Decode(buf.Bytes(), rx)
	want, err2 := Decode(sample(), rx)
	if err1 != nil || err2 != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("gzip decode differs: %v %v", err1, err2)
	}
	if _, err := Decode([]byte{0x1f, 0x8b, 1}, rx); err == nil {
		t.Error("corrupt gzip: want an error")
	}
}

func TestBadLengthRejected(t *testing.T) {
	if _, err := Decode(append(sample(), 'x'), rx); err == nil {
		t.Error("want an error")
	}
}

func TestEmptyFile(t *testing.T) {
	if rows, err := Decode(nil, rx); err != nil || len(rows) != 0 {
		t.Errorf("%v %v", rows, err)
	}
}

func TestRangeAndBearing(t *testing.T) {
	d, brg := RangeAndBearing(40.0, -70.0, 40.0, -69.0)
	if d <= 45 || d >= 46.5 || brg <= 89 || brg >= 90.5 { // 1 deg of longitude at 40N, due east
		t.Errorf("%v %v", d, brg)
	}
	if _, brg := RangeAndBearing(40.0, -70.0, 40.0, -71.0); brg <= 269 || brg >= 271 { // west: not negative
		t.Errorf("west bearing %v", brg)
	}
}

func TestNoReceiverLocation(t *testing.T) {
	rows, _ := Decode(sample(), nil)
	for _, r := range rows {
		if r.DistanceNM != nil || r.Bearing != nil {
			t.Errorf("%+v", r)
		}
	}
	if len(rows) != 3 {
		t.Errorf("%d rows", len(rows))
	}
}

func TestOddCallsignAndUnknownSource(t *testing.T) {
	raw := Build([]Slice{{t0, [][]byte{
		Callsign(a, "AB\x80", 1),
		Position(a, 1, 1, nil, nil, false, 31), // addrtype past the known list
	}}}, 15000)
	rows, _ := Decode(raw, nil)
	if len(rows) != 1 || str(rows[0].Flight) != "AB�" || str(rows[0].Squawk) != "0001" || rows[0].Source != "unknown" {
		t.Errorf("%+v %s", rows, str(rows[0].Flight))
	}
}

func TestMarkerKeepsHighBits(t *testing.T) {
	// A time past 2^32 ms must survive the split into two 32-bit words.
	rows, _ := Decode(Build([]Slice{{t0, [][]byte{Position(a, 1, 1, nil, nil, false, 0)}}}, 15000), nil)
	if rows[0].Time.UnixMilli() != t0 {
		t.Errorf("got %d", rows[0].Time.UnixMilli())
	}
}
