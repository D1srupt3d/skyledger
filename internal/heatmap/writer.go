package heatmap

import (
	"encoding/binary"
	"math"
)

// Writer side: builds heatmap files for tests and the demo feed; skyledger itself only reads them.

// Record is one raw 16-byte record.
func Record(hx, lat, lon int32, alt, gs int16) []byte {
	b := make([]byte, 16)
	le := binary.LittleEndian
	le.PutUint32(b[0:], uint32(hx))
	le.PutUint32(b[4:], uint32(lat))
	le.PutUint32(b[8:], uint32(lon))
	le.PutUint16(b[12:], uint16(alt))
	le.PutUint16(b[14:], uint16(gs))
	return b
}

// Marker starts a slice at ms (Unix milliseconds).
func Marker(ms int64, intervalMS int16) []byte {
	return Record(Magic, int32(uint32(uint64(ms)>>32)), int32(uint32(ms)), intervalMS, 0)
}

// Position is a position record. addr: 24-bit address, plus 1 << 24 for non-ICAO.
// source: readsb addrtype. A nil altitude or speed is "unknown".
func Position(addr int32, lat, lon float64, altFt, gsKt *float64, onGround bool, source int32) []byte {
	alt := int16(AltUnknown)
	if onGround {
		alt = AltGround
	} else if altFt != nil {
		alt = int16(math.RoundToEven(*altFt / 25)) // Python's round() rounds half to even
	}
	gs := int16(-1)
	if gsKt != nil {
		gs = int16(math.RoundToEven(*gsKt * 10))
	}
	return Record(addr|source<<27, int32(math.RoundToEven(lat*1e6)), int32(math.RoundToEven(lon*1e6)), alt, gs)
}

// Callsign is a callsign and squawk record; the name is NUL padded to 8 bytes.
func Callsign(addr int32, name string, squawk int32) []byte {
	b := Record(addr, 1<<30|squawk, 0, 0, 0)
	copy(b[8:16], name)
	return b
}

// Slice is one slice: its start in Unix milliseconds and its records.
type Slice struct {
	StartMS int64
	Records [][]byte
}

// Build returns file bytes: the index, then a marker and the records of each slice.
func Build(slices []Slice, intervalMS int16) []byte {
	var index, body []byte
	offset := int32(len(slices))
	for _, s := range slices {
		index = append(index, Record(offset, 0, 0, 0, 0)...)
		body = append(body, Marker(s.StartMS, intervalMS)...)
		for _, r := range s.Records {
			body = append(body, r...)
		}
		offset += 1 + int32(len(s.Records))
	}
	return append(index, body...)
}
