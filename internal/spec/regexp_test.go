package spec

import "testing"

// The \uXXXX escape of ECMA-262 patterns reads as the same code point in Go; other escapes, and
// an escaped backslash before a u, are kept.
func TestTranslateECMA(t *testing.T) {
	for in, want := range map[string]string{
		`^[^\r\n\u2028\u2029]+$`: `^[^\r\n\x{2028}\x{2029}]+$`,
		`^plain$`:                `^plain$`,
		`a\\u2028`:               `a\\u2028`,
		`\u20`:                   `\u20`,
		`\d+\u00e9`:              `\d+\x{00e9}`,
	} {
		if got := translateECMA(in); got != want {
			t.Errorf("translateECMA(%q) = %q, want %q", in, got, want)
		}
	}
	re, err := ecmaRegexp(`^[^\r\n\u2028\u2029]+$`)
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("one line") || re.MatchString("two lines") {
		t.Fatal("the translated pattern does not match as the ECMA one does")
	}
}
