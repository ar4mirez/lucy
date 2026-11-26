// Package config loads lucy's own settings file, config.toml, and renders what lucy says to the
// person in charge (GLOSSARY.md, the owner).
//
// Inside lucy the human role is always the neutral id "owner": in code, in the API, in policy and
// in every recorded event; recorded text that names them, such as a policy decision's reason,
// says {owner}. What a surface shows that person is their configured display title, which Render
// puts in place of {owner} only when text is rendered for them (CLI, TUI, dashboard, MCP chat
// replies, notifications, holding messages). The title defaults to "Owner"; `lucy preflight` will ask for
// it when it lands (LUCY-OPS-001), and until then it is set by editing config.toml:
//
//	[you]
//	title = "Commander" # how lucy refers to you; default "Owner"
//	name = "Ada"        # optional; shown with the title, as "Ada (Commander)"
//
// The file is a small TOML subset read without a TOML library, like the docs' front matter:
// `[section]` headers, `key = "basic string"` or `key = 'literal string'`, and `#` comments.
// Anything else, and any unknown section or key, is an error naming the line, so a typo is
// reported rather than silently ignored.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DefaultTitle is the owner's display title when none is configured.
const DefaultTitle = "Owner"

// FileName is the settings file's name inside lucy's config directory.
const FileName = "config.toml"

// MaxValueBytes bounds a title or name.
const MaxValueBytes = 64

// maxFileBytes bounds the settings file, so a wrong path cannot make lucy read a huge file.
const maxFileBytes = 64 << 10

// Config is lucy's settings file.
type Config struct {
	You You
}

// You is the [you] section: how lucy refers to its owner. The zero value is the
// default: title "Owner", no name.
type You struct {
	title string
	name  string
}

// Title is the owner's display title, for example "Owner" or "Commander".
func (y You) Title() string {
	if y.title == "" {
		return DefaultTitle
	}
	return y.title
}

// Name is the owner's name, or "" when none is set.
func (y You) Name() string { return y.name }

// Label is the owner as shown in a listing: "Commander", or "Ada (Commander)" when a name is set.
func (y You) Label() string {
	if y.name != "" {
		return y.name + " (" + y.Title() + ")"
	}
	return y.Title()
}

// Render resolves the {owner} placeholder in a message for the owner, such as a policy decision's
// reason, to the display title. Recorded data keeps the placeholder; only rendered text carries
// the title.
func (y You) Render(msg string) string {
	return strings.ReplaceAll(msg, "{owner}", y.Title())
}

// Holding renders the message for an action that is holding, waiting on the owner, with the
// decision's reason rendered too.
func (y You) Holding(reason string) string {
	msg := "holding: waiting on the {owner}"
	if reason != "" {
		msg += ": " + reason
	}
	return y.Render(msg)
}

// dir returns lucy's config directory: $XDG_CONFIG_HOME/lucy when XDG_CONFIG_HOME is set, else
// lucy inside the OS's user config dir (~/.config on Linux, ~/Library/Application Support on
// macOS).
func dir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		if !filepath.IsAbs(d) {
			return "", fmt.Errorf("config: XDG_CONFIG_HOME %q is not absolute", d)
		}
		return filepath.Join(d, "lucy"), nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	return filepath.Join(d, "lucy"), nil
}

// ResourcesDirName is the directory, inside lucy's config directory, that holds spec resources.
const ResourcesDirName = "resources"

// ResourcesDir returns the directory lucy loads spec resources from: resources inside lucy's
// config directory. Files there hold spec only; status lives in lucy's database.
func ResourcesDir() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ResourcesDirName), nil
}

// defaultPath returns the settings file's path inside dir.
func defaultPath() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, FileName), nil
}

// LoadDefault loads the settings file in lucy's config directory: $XDG_CONFIG_HOME/lucy when
// XDG_CONFIG_HOME is set, else lucy inside the OS's user config dir.
func LoadDefault() (Config, error) {
	p, err := defaultPath()
	if err != nil {
		return Config{}, err
	}
	return Load(p)
}

// Load reads the settings file at path. A missing file is the defaults, not an error.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if len(data) > maxFileBytes {
		return Config{}, fmt.Errorf("config: %s is larger than %d bytes", path, maxFileBytes)
	}
	c, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse reads a settings file's contents.
func Parse(data []byte) (Config, error) {
	if !utf8.Valid(data) {
		return Config{}, errors.New("config: not valid UTF-8")
	}
	var c Config
	section := ""
	seenSections := map[string]bool{}
	seenKeys := map[string]bool{}
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		fail := func(format string, a ...any) (Config, error) {
			return Config{}, fmt.Errorf("line %d: "+format, append([]any{i + 1}, a...)...)
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, rest, ok := strings.Cut(line[1:], "]")
			name = strings.TrimSpace(name)
			if !ok || !emptyOrComment(rest) || strings.HasPrefix(name, "[") {
				return fail("want a [section] header")
			}
			if name != "you" {
				return fail("unknown section [%s]; lucy knows [you]", name)
			}
			if seenSections[name] {
				return fail("section [%s] appears twice", name)
			}
			seenSections[name], section = true, name
			continue
		}
		key, rest, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fail("want key = \"value\"")
		}
		if section == "" {
			return fail("key %q is outside a section; put it under [you]", key)
		}
		if key != "title" && key != "name" {
			return fail("unknown key %q in [%s]; lucy knows title and name", key, section)
		}
		if seenKeys[section+"."+key] {
			return fail("key %q appears twice in [%s]", key, section)
		}
		seenKeys[section+"."+key] = true
		v, err := parseString(strings.TrimSpace(rest))
		if err != "" {
			return fail("%s: %s", key, err)
		}
		if err := checkValue(v); err != "" {
			return fail("%s %s", key, err)
		}
		if key == "title" {
			c.You.title = v
		} else {
			c.You.name = v
		}
	}
	return c, nil
}

// parseString reads one TOML basic ("...") or literal ('...') string followed by an optional
// comment.
func parseString(s string) (string, string) {
	if s == "" {
		return "", "want a quoted string"
	}
	switch s[0] {
	case '\'':
		v, rest, ok := strings.Cut(s[1:], "'")
		if !ok {
			return "", "unterminated string"
		}
		if !emptyOrComment(rest) {
			return "", "unexpected text after the string"
		}
		return v, ""
	case '"':
		end := -1
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			return "", "unterminated string"
		}
		if !emptyOrComment(s[end+1:]) {
			return "", "unexpected text after the string"
		}
		body := s[1:end]
		for i := 0; i < len(body); i++ {
			if body[i] != '\\' {
				continue
			}
			i++
			if i >= len(body) || !strings.ContainsRune(`btnfr"\uU`, rune(body[i])) {
				return "", "unsupported escape in the string"
			}
		}
		v, err := strconv.Unquote(s[:end+1])
		if err != nil {
			return "", "bad escape in the string"
		}
		return v, ""
	}
	return "", "want a quoted string"
}

func emptyOrComment(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || strings.HasPrefix(s, "#")
}

// checkValue returns "" or why v is not an acceptable title or name: not blank, at most
// MaxValueBytes, no control or invisible characters, no leading or trailing space.
func checkValue(v string) string {
	switch {
	case strings.TrimSpace(v) == "":
		return "is empty"
	case len(v) > MaxValueBytes:
		return fmt.Sprintf("is longer than %d bytes", MaxValueBytes)
	case !utf8.ValidString(v):
		return "is not valid UTF-8"
	case strings.TrimSpace(v) != v:
		return "has leading or trailing space"
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "contains a control or invisible character"
		}
	}
	return ""
}
