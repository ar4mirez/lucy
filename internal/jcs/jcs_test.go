package jcs

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
)

// RFC 8785 section 3.2.4: the canonical form of the RFC's sample object.
func TestRFC8785Sample(t *testing.T) {
	in := `{
  "numbers": [333333333.33333329, 1E30, 4.50, 2e-3, 0.000000000000000000000000001],
  "string": "\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/",
  "literals": [null, true, false]
}`
	want := `{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`
	got, err := Transform([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// RFC 8785 section 3.2.3: members sort by UTF-16 code units, so U+1F600 (D83D DE00) sorts before
// U+FB33.
func TestSortUTF16(t *testing.T) {
	in := `{"\u20ac":"Euro Sign","\r":"Carriage Return","\ufb33":"Hebrew Letter Dalet With Dagesh","1":"One","\ud83d\ude00":"Emoji: Grinning Face","\u0080":"Control","\u00f6":"Latin Small Letter O With Diaeresis"}`
	want := "{\"\\r\":\"Carriage Return\",\"1\":\"One\",\"\u0080\":\"Control\",\"\u00f6\":\"Latin Small Letter O With Diaeresis\",\"\u20ac\":\"Euro Sign\",\"\U0001F600\":\"Emoji: Grinning Face\",\"\ufb33\":\"Hebrew Letter Dalet With Dagesh\"}"
	got, err := Transform([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestFormatNumber(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "0"},
		{1, "1"},
		{1.0, "1"},
		{-1.5, "-1.5"},
		{4.5, "4.5"},
		{0.002, "0.002"},
		{1e-6, "0.000001"},
		{1e-7, "1e-7"},
		{1.5e-7, "1.5e-7"},
		{123456789, "123456789"},
		{1e20, "100000000000000000000"},
		{1e21, "1e+21"},
		{1.5e21, "1.5e+21"},
		{1e30, "1e+30"},
		{333333333.33333329, "333333333.3333333"},
		{9007199254740992, "9007199254740992"},
		{295147905179352830000, "295147905179352830000"},
		{5e-324, "5e-324"},
		{1.7976931348623157e308, "1.7976931348623157e+308"},
		{-1e-27, "-1e-27"},
	}
	for _, c := range cases {
		got, err := FormatNumber(c.in)
		if err != nil {
			t.Fatalf("%v: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("FormatNumber(%v) = %s, want %s", c.in, got, c.want)
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := FormatNumber(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("FormatNumber(%v) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestStringEscapes(t *testing.T) {
	// No HTML escaping and no U+2028/U+2029 escaping; controls as lower-case \u00xx.
	got, err := Transform([]byte(`"<&>\u2028\u2029\u001f\u007f\t/"`))
	if err != nil {
		t.Fatal(err)
	}
	want := "\"<&>\u2028\u2029\\u001f\u007f\\t/\""
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRefusesNonIJSON(t *testing.T) {
	for _, in := range []string{
		``,
		`{"a":1,"a":2}`,
		`"\ud800"`,
		`"\udc00"`,
		`"\ud800\u0041"`,
		"\"\xff\"",
		"\"a\x01\"",
		`1e400`,
		`-1e400`,
		`01`,
		`1.`,
		`.5`,
		`+1`,
		`[1,]`,
		`{"a":1,}`,
		`{"a" 1}`,
		`[1] [2]`,
		`tru`,
		`NaN`,
		`"\x"`,
		`"\u12"`,
	} {
		if _, err := Transform([]byte(in)); !errors.Is(err, ErrInvalid) {
			t.Errorf("Transform(%q) = %v, want ErrInvalid", in, err)
		}
	}
}

func TestDepthLimit(t *testing.T) {
	deep := bytes.Repeat([]byte("["), MaxDepth) // exactly MaxDepth levels is fine
	deep = append(deep, bytes.Repeat([]byte("]"), MaxDepth)...)
	if _, err := Transform(deep); err != nil {
		t.Fatalf("depth %d: %v", MaxDepth, err)
	}
	tooDeep := append([]byte("["), deep...)
	tooDeep = append(tooDeep, ']')
	if _, err := Transform(tooDeep); !errors.Is(err, ErrInvalid) {
		t.Fatalf("depth %d: %v, want ErrInvalid", MaxDepth+1, err)
	}
}

func TestMarshal(t *testing.T) {
	got, err := Marshal(map[string]any{"b": []int{1, 2}, "a": "<x>", "c": 1.0})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"a":"<x>","b":[1,2],"c":1}`; string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// FuzzTransform checks that canonical output is valid JSON denoting the same value as the input,
// and that canonicalizing it again changes nothing.
func FuzzTransform(f *testing.F) {
	for _, s := range []string{`{"a":[1,2.5,null,true],"b":"x\ty"}`, `1e21`, `"\u2028"`, `{"\ud83d\ude00":0}`, `[]`, `{}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		out, err := Transform(in)
		if err != nil {
			return
		}
		again, err := Transform(out)
		if err != nil {
			t.Fatalf("canonical output %q does not parse: %v", out, err)
		}
		if !bytes.Equal(out, again) {
			t.Fatalf("not idempotent: %q then %q", out, again)
		}
		var a, b any
		if err := json.Unmarshal(in, &a); err != nil {
			t.Fatalf("accepted %q, which encoding/json refuses: %v", in, err)
		}
		if err := json.Unmarshal(out, &b); err != nil {
			t.Fatalf("output %q is not JSON: %v", out, err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("value changed: %q became %q", in, out)
		}
	})
}
