package dashboards

import (
	"bytes"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// obj is a JSON object that keeps keys in insertion order, like a Python
// dict. Setting an existing key keeps its position, as dict updates do.
type obj struct {
	keys []string
	vals map[string]any
}

// o builds an obj from alternating keys and values: o("a", 1, "b", 2).
func o(kv ...any) *obj {
	m := &obj{vals: make(map[string]any, len(kv)/2)}
	for i := 0; i < len(kv); i += 2 {
		m.set(kv[i].(string), kv[i+1])
	}
	return m
}

func (m *obj) set(k string, v any) {
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

func (m *obj) get(k string) any { return m.vals[k] }

func (m *obj) sub(k string) *obj { return m.vals[k].(*obj) }

func (m *obj) num(k string) int { return m.vals[k].(int) }

// clone is a shallow copy, like {**m}.
func (m *obj) clone() *obj {
	return &obj{keys: slices.Clone(m.keys), vals: maps.Clone(m.vals)}
}

// dumps encodes v like Python's json.dumps(v, indent=2): 2-space indent,
// ": " after keys, ASCII-only strings, no HTML escaping, floats as repr().
func dumps(v any) ([]byte, error) {
	var b bytes.Buffer
	err := writeValue(&b, v, "")
	return b.Bytes(), err
}

func writeValue(b *bytes.Buffer, v any, indent string) error {
	inner := indent + "  "
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case int:
		b.WriteString(strconv.Itoa(v))
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("dashboards: cannot encode %v", v)
		}
		b.WriteString(pyFloat(v))
	case string:
		writeString(b, v)
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, x := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString("\n" + inner)
			if err := writeValue(b, x, inner); err != nil {
				return err
			}
		}
		b.WriteString("\n" + indent + "]")
	case *obj:
		if len(v.keys) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteByte('{')
		for i, k := range v.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString("\n" + inner)
			writeString(b, k)
			b.WriteString(": ")
			if err := writeValue(b, v.vals[k], inner); err != nil {
				return err
			}
		}
		b.WriteString("\n" + indent + "}")
	default:
		return fmt.Errorf("dashboards: cannot encode %T", v)
	}
	return nil
}

// pyFloat formats f like Python's repr(float): shortest round-trip digits,
// scientific outside 1e-4 <= |f| < 1e16, and ".0" on whole numbers.
func pyFloat(f float64) string {
	if a := math.Abs(f); a != 0 && (a < 1e-4 || a >= 1e16) {
		return strconv.FormatFloat(f, 'e', -1, 64)
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// writeString quotes s like json.dumps with ensure_ascii=True: everything
// outside printable ASCII becomes \uXXXX (a surrogate pair above U+FFFF).
func writeString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r >= 0x20 && r <= 0x7e:
				b.WriteRune(r)
			case r > 0xffff:
				r1, r2 := utf16.EncodeRune(r)
				fmt.Fprintf(b, `\u%04x\u%04x`, r1, r2)
			default:
				fmt.Fprintf(b, `\u%04x`, r)
			}
		}
	}
	b.WriteByte('"')
}
