// Package jcs implements the JSON Canonicalization Scheme of RFC 8785: one byte sequence for any
// JSON value, independent of the encoder that produced it. lucy hashes audit entries over it
// (LUCY-SPEC-009), so the hash an entry carries is the hash any conforming implementation
// computes.
//
// Canonical form: object members sorted by their names' UTF-16 code units, no insignificant
// whitespace, strings with only the escapes JSON requires (no HTML or U+2028 escaping), and numbers
// in the shortest form that round-trips an IEEE 754 double, as ECMAScript prints them (1.0 is 1,
// 1e21 is 1e+21).
//
// Input must be I-JSON (RFC 7493): valid UTF-8, no lone surrogates, no duplicate member names,
// numbers within the double range. Anything else is an error, never silently repaired.
package jcs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// MaxDepth bounds how deeply arrays and objects may nest.
const MaxDepth = 128

// ErrInvalid wraps every input error.
var ErrInvalid = errors.New("jcs: invalid input")

// Transform parses a JSON text and returns its canonical form.
func Transform(data []byte) ([]byte, error) {
	v, err := Parse(data)
	if err != nil {
		return nil, err
	}
	return Encode(v)
}

// Encode returns the canonical form of a value as Parse returns it.
func Encode(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := write(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Marshal encodes v with encoding/json and returns its canonical form.
func Marshal(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("jcs: %w", err)
	}
	return Transform(data)
}

// Number is a JSON number, held as the double it denotes.
type Number float64

// Parse parses one JSON text strictly. Objects become map[string]any, arrays []any, strings string,
// numbers Number, and true, false and null bool and nil.
func Parse(data []byte) (any, error) {
	p := &parser{data: data}
	p.space()
	v, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.data) {
		return nil, p.fail("trailing data")
	}
	return v, nil
}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) fail(format string, args ...any) error {
	return fmt.Errorf("%w: offset %d: %s", ErrInvalid, p.pos, fmt.Sprintf(format, args...))
}

func (p *parser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value(depth int) (any, error) {
	if p.pos >= len(p.data) {
		return nil, p.fail("unexpected end of input")
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object(depth + 1)
	case c == '[':
		return p.array(depth + 1)
	case c == '"':
		return p.string()
	case c == '-' || c >= '0' && c <= '9':
		return p.number()
	case p.literal("true"):
		return true, nil
	case p.literal("false"):
		return false, nil
	case p.literal("null"):
		return nil, nil
	default:
		return nil, p.fail("unexpected character %q", c)
	}
}

func (p *parser) literal(s string) bool {
	if bytes.HasPrefix(p.data[p.pos:], []byte(s)) {
		p.pos += len(s)
		return true
	}
	return false
}

func (p *parser) object(depth int) (any, error) {
	if depth > MaxDepth {
		return nil, p.fail("nesting deeper than %d", MaxDepth)
	}
	p.pos++ // {
	m := map[string]any{}
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return m, nil
	}
	for {
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return nil, p.fail("expected a member name")
		}
		k, err := p.string()
		if err != nil {
			return nil, err
		}
		if _, dup := m[k]; dup {
			return nil, p.fail("duplicate member name %q", k)
		}
		p.space()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return nil, p.fail("expected ':'")
		}
		p.pos++
		p.space()
		v, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		m[k] = v
		p.space()
		if p.pos >= len(p.data) {
			return nil, p.fail("unterminated object")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return m, nil
		default:
			return nil, p.fail("expected ',' or '}'")
		}
	}
}

func (p *parser) array(depth int) (any, error) {
	if depth > MaxDepth {
		return nil, p.fail("nesting deeper than %d", MaxDepth)
	}
	p.pos++ // [
	a := []any{}
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return a, nil
	}
	for {
		p.space()
		v, err := p.value(depth)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		p.space()
		if p.pos >= len(p.data) {
			return nil, p.fail("unterminated array")
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return a, nil
		default:
			return nil, p.fail("expected ',' or ']'")
		}
	}
}

func (p *parser) string() (string, error) {
	p.pos++ // opening quote
	var b strings.Builder
	for {
		if p.pos >= len(p.data) {
			return "", p.fail("unterminated string")
		}
		c := p.data[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String(), nil
		case c == '\\':
			r, err := p.escape()
			if err != nil {
				return "", err
			}
			b.WriteRune(r)
		case c < 0x20:
			return "", p.fail("control character in string")
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			p.pos++
		default:
			r, n := utf8.DecodeRune(p.data[p.pos:])
			if r == utf8.RuneError && n <= 1 {
				return "", p.fail("invalid UTF-8")
			}
			b.WriteRune(r)
			p.pos += n
		}
	}
}

// escape decodes one escape sequence, pairing surrogates; a lone surrogate is an error.
func (p *parser) escape() (rune, error) {
	if p.pos+1 >= len(p.data) {
		return 0, p.fail("unterminated escape")
	}
	c := p.data[p.pos+1]
	p.pos += 2
	switch c {
	case '"', '\\', '/':
		return rune(c), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		r, err := p.hex4()
		if err != nil {
			return 0, err
		}
		switch {
		case r >= 0xd800 && r < 0xdc00:
			if !bytes.HasPrefix(p.data[p.pos:], []byte(`\u`)) {
				return 0, p.fail("lone surrogate")
			}
			p.pos += 2
			lo, err := p.hex4()
			if err != nil {
				return 0, err
			}
			if lo < 0xdc00 || lo >= 0xe000 {
				return 0, p.fail("lone surrogate")
			}
			return utf16.DecodeRune(r, lo), nil
		case r >= 0xdc00 && r < 0xe000:
			return 0, p.fail("lone surrogate")
		}
		return r, nil
	default:
		return 0, p.fail("invalid escape \\%c", c)
	}
}

func (p *parser) hex4() (rune, error) {
	if p.pos+4 > len(p.data) {
		return 0, p.fail("short \\u escape")
	}
	n, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 16)
	if err != nil {
		return 0, p.fail("invalid \\u escape")
	}
	p.pos += 4
	return rune(n), nil
}

func (p *parser) number() (any, error) {
	start := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
	}
	switch {
	case p.pos < len(p.data) && p.data[p.pos] == '0':
		p.pos++
	case p.pos < len(p.data) && p.data[p.pos] >= '1' && p.data[p.pos] <= '9':
		p.digits()
	default:
		return nil, p.fail("invalid number")
	}
	if p.pos < len(p.data) && p.data[p.pos] == '.' {
		p.pos++
		if p.digits() == 0 {
			return nil, p.fail("invalid number")
		}
	}
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
		}
		if p.digits() == 0 {
			return nil, p.fail("invalid number")
		}
	}
	f, err := strconv.ParseFloat(string(p.data[start:p.pos]), 64)
	if err != nil || math.IsInf(f, 0) {
		return nil, p.fail("number %s is outside the IEEE 754 double range", p.data[start:p.pos])
	}
	return Number(f), nil
}

func (p *parser) digits() int {
	n := 0
	for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
		p.pos++
		n++
	}
	return n
}

func write(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case Number:
		s, err := FormatNumber(float64(x))
		if err != nil {
			return err
		}
		b.WriteString(s)
	case string:
		writeString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := write(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, compareUTF16)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			if err := write(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("%w: unsupported value %T", ErrInvalid, v)
	}
	return nil
}

// compareUTF16 orders strings by their UTF-16 code units, as RFC 8785 section 3.2.3 requires.
func compareUTF16(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	return slices.Compare(ua, ub)
}

// writeString writes s with only the escapes RFC 8785 section 3.2.2.2 allows.
func writeString(b *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[r>>4])
				b.WriteByte(hex[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// FormatNumber prints a double as ECMAScript's Number.prototype.toString does (RFC 8785 section
// 3.2.2.3): the shortest digits that round-trip, in plain notation from 1e-6 up to 1e21 and in
// exponent notation outside it. NaN and the infinities are not JSON and are refused.
func FormatNumber(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("%w: %v is not a JSON number", ErrInvalid, f)
	}
	if f == 0 {
		return "0", nil // also -0
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// Shortest round-trip digits: "d.ddde±x".
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, err := strconv.Atoi(expStr)
	if err != nil {
		return "", fmt.Errorf("jcs: format %v: %w", f, err)
	}
	k, n := len(digits), exp+1 // digits, and the decimal point's position after the first n digits
	var s string
	switch {
	case k <= n && n <= 21:
		s = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		s = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		s = "0." + strings.Repeat("0", -n) + digits
	default:
		es := "+"
		if n-1 < 0 {
			es = "-"
		}
		abs := n - 1
		if abs < 0 {
			abs = -abs
		}
		s = digits[:1]
		if k > 1 {
			s += "." + digits[1:]
		}
		s += "e" + es + strconv.Itoa(abs)
	}
	return sign + s, nil
}
