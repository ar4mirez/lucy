package spec

import (
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ecmaRegexp compiles a schema's pattern with Go's regexp engine, after rewriting the one
// ECMA-262 escape the vendored schemas use that Go does not read: \uXXXX, a code point by four hex
// digits, becomes \x{XXXX}. An escaped backslash (\\) is kept as it is, so \\u is not rewritten.
func ecmaRegexp(s string) (jsonschema.Regexp, error) {
	return regexp.Compile(translateECMA(s))
}

func translateECMA(s string) string {
	if !strings.Contains(s, `\u`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		if s[i+1] == 'u' && i+6 <= len(s) && isHex(s[i+2:i+6]) {
			b.WriteString(`\x{` + s[i+2:i+6] + `}`)
			i += 5
			continue
		}
		b.WriteString(s[i : i+2]) // any other escape, \\ included, as it is
		i++
	}
	return b.String()
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
