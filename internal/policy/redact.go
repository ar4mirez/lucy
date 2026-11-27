package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"unicode/utf8"
)

// MaxRecordedText bounds the redacted text of an exec or fetch resource in a decision record.
const MaxRecordedText = 512

// Redacted replaces a secret in recorded text.
const Redacted = "<redacted>"

// RecordedResource is a request's resource as a decision record holds it. A shell command, URL or
// search is untrusted text that can carry secrets, so an exec or fetch resource is recorded
// redacted, capped at MaxRecordedText bytes, and followed by the SHA-256 of the original text, so
// the same text can be matched across records without being stored. Every other kind's resource
// is a name or a path and is recorded as it is. The engine always decides on the original.
func RecordedResource(k Kind, resource string) string {
	if k != KindExec && k != KindFetch || resource == "" {
		return resource
	}
	sum := sha256.Sum256([]byte(resource))
	text := Redact(resource)
	if len(text) > MaxRecordedText {
		cut := MaxRecordedText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
	}
	return text + " sha256:" + hex.EncodeToString(sum[:])
}

var (
	// An Authorization or Cookie header's value, up to a quote or the end of the line.
	headerSecret = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization|(?:set-)?cookie)(\s*[:=]\s*)[^'"\r\n]*`)
	// The userinfo of a URL (user:pass@ or token@).
	urlUserinfo = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*://)[^/\s@'"?#]+@`)
	// The value of a query parameter named like a credential.
	querySecret = regexp.MustCompile(`(?i)([?&;][^=&\s'"#]*(?:token|key|secret|passw|sig|auth)[^=&\s'"#]*=)[^&\s'"#]*`)
	// NAME=value or export NAME=value where NAME names a credential.
	envSecret = regexp.MustCompile(`(?i)\b([a-z0-9_]*(?:token|key|secret|pass|auth)[a-z0-9_]*=)('[^']*'|"[^"]*"|[^\s;&|'"]*)`)
	// Tokens with a known issuer prefix.
	tokenPrefix = regexp.MustCompile(`\b(?:(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|sk-[A-Za-z0-9_\-]{16,}|xox[abp]-[A-Za-z0-9\-]{8,}|AKIA[0-9A-Z]{16}|glpat-[A-Za-z0-9_\-]{16,})`)
	// A long run of token characters, redacted when it looks random.
	longRun = regexp.MustCompile(`[A-Za-z0-9+/=_\-]{32,}`)
)

// Redact replaces the secrets text may carry with Redacted: Authorization and Cookie header
// values, URL credentials, credential-named query parameters and NAME=value assignments, tokens
// with a known prefix (GitHub, OpenAI, Slack, AWS, GitLab) and long high-entropy strings.
func Redact(text string) string {
	text = headerSecret.ReplaceAllString(text, "${1}${2}"+Redacted)
	text = urlUserinfo.ReplaceAllString(text, "${1}"+Redacted+"@")
	text = querySecret.ReplaceAllString(text, "${1}"+Redacted)
	text = envSecret.ReplaceAllString(text, "${1}"+Redacted)
	text = tokenPrefix.ReplaceAllString(text, Redacted)
	return longRun.ReplaceAllStringFunc(text, func(s string) string {
		if highEntropy(s) {
			return Redacted
		}
		return s
	})
}

// highEntropy reports whether s mixes letters and digits and has the Shannon entropy of random
// text rather than of a word, a path or an identifier.
func highEntropy(s string) bool {
	var letter, digit bool
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			letter = true
		}
	}
	if !letter || !digit {
		return false
	}
	var h float64
	n := float64(len(s))
	for _, c := range counts {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h >= 3.5
}
