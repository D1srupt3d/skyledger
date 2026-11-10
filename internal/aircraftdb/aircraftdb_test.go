package aircraftdb

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFlagBits(t *testing.T) {
	for flags, bits := range map[string]int16{
		"00": 0, "10": 1, "01": 2, "0010": 4, "0001": 8, "11": 3, "1001": 9, "": 0} {
		if got := FlagBits(flags); got != bits {
			t.Errorf("%q: got %d, want %d", flags, got, bits)
		}
	}
}

func shard(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDBRows(t *testing.T) {
	rows, err := DBRows("AD", shard(t, `{"0001": ["N100XX", "B739", "00", "BOEING 737-900"],
		"0003": ["N100XY", null, "0001", null], "children": ["AD0", "AD1"]}`))
	want := [][]any{{"ad0001", "N100XX", "B739", "BOEING 737-900", int16(0)}, {"ad0003", "N100XY", nil, nil, int16(8)}}
	if err != nil || !reflect.DeepEqual(rows, want) {
		t.Errorf("got %v %v", rows, err)
	}
	rows, _ = DBRows("A", shard(t, `{"E0000": ["00-0001", "T38", "10", "NORTHROP T-38"]}`))
	if !reflect.DeepEqual(rows, [][]any{{"ae0000", "00-0001", "T38", "NORTHROP T-38", int16(1)}}) {
		t.Errorf("got %v", rows)
	}
}

func TestDBRowsShortAndBadRecords(t *testing.T) {
	rows, err := DBRows("A", shard(t, `{"B": ["N1"]}`)) // missing fields are NULL
	if err != nil || !reflect.DeepEqual(rows, [][]any{{"ab", "N1", nil, nil, int16(0)}}) {
		t.Errorf("got %v %v", rows, err)
	}
	if _, err := DBRows("A", shard(t, `{"B": {"not": "a list"}}`)); err == nil {
		t.Error("want an error")
	}
}

func TestAirlineRows(t *testing.T) {
	got := AirlineRows(map[string]Operator{"XXX": {}, "AAA": {N: "Example Air", C: "Nowhere", R: "EXAMPLE"}})
	want := [][]any{{"AAA", "Example Air", "Nowhere", "EXAMPLE"}, {"XXX", nil, nil, nil}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestDBVersion(t *testing.T) {
	if v, err := DBVersion(`<script>databaseFolder = "db-3.14.1718";</script>`); err != nil || v != "db-3.14.1718" {
		t.Errorf("%q %v", v, err)
	}
	if _, err := DBVersion("<html></html>"); err == nil {
		t.Error("want an error")
	}
}
