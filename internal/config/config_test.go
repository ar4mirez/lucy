package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing", FileName))
	if err != nil {
		t.Fatalf("a missing file is the defaults, got %v", err)
	}
	y := c.You
	if y.Title() != "Owner" || y.Name() != "" || y.Address() != "Owner" || y.Label() != "Owner" {
		t.Errorf("defaults: title %q, name %q, address %q, label %q", y.Title(), y.Name(), y.Address(), y.Label())
	}
	if got := y.Render("waiting on the {owner}; thanks, {you}"); got != "waiting on the Owner; thanks, Owner" {
		t.Errorf("Render = %q", got)
	}
	if got := y.Holding("the change touches the fixed list (security)"); got != "holding: waiting on the Owner: the change touches the fixed list (security)" {
		t.Errorf("Holding = %q", got)
	}
	if Default() != (Config{}) {
		t.Error("Default is not the zero Config")
	}
}

func TestCustomTitle(t *testing.T) {
	c := mustParse(t, "# lucy settings\n[you]\ntitle = \"Commander\" # how lucy refers to me\n")
	y := c.You
	if y.Title() != "Commander" || y.Name() != "" || y.Address() != "Commander" || y.Label() != "Commander" {
		t.Errorf("title %q, name %q, address %q, label %q", y.Title(), y.Name(), y.Address(), y.Label())
	}
	if got := y.Holding(""); got != "holding: waiting on the Commander" {
		t.Errorf("Holding = %q", got)
	}
	if got := y.Render("{you}, the merge is waiting on the {owner}"); got != "Commander, the merge is waiting on the Commander" {
		t.Errorf("Render = %q", got)
	}
}

func TestCustomName(t *testing.T) {
	c := mustParse(t, "[you]\nname = 'Ada'\n")
	y := c.You
	if y.Title() != "Owner" || y.Name() != "Ada" || y.Address() != "Ada" || y.Label() != "Ada (Owner)" {
		t.Errorf("title %q, name %q, address %q, label %q", y.Title(), y.Name(), y.Address(), y.Label())
	}
	c = mustParse(t, "\r\n  [ you ]  # me\r\n title=\"Capitán \\u00e9\"\r\n name = \"Ada \\\"A\\\" L.\"\r\n")
	y = c.You
	if y.Title() != "Capitán é" || y.Name() != `Ada "A" L.` || y.Label() != `Ada "A" L. (Capitán é)` {
		t.Errorf("title %q, name %q, label %q", y.Title(), y.Name(), y.Label())
	}
	if got := y.Render("Good morning, {you}."); got != `Good morning, Ada "A" L..` {
		t.Errorf("Render = %q", got)
	}
}

func TestNewYou(t *testing.T) {
	y, err := NewYou("Commander", "Ada")
	if err != nil || y.Label() != "Ada (Commander)" {
		t.Fatalf("NewYou = %q, %v", y.Label(), err)
	}
	if y, err := NewYou("", ""); err != nil || y != (You{}) {
		t.Errorf("empty NewYou = %+v, %v", y, err)
	}
	if _, err := NewYou("Com\x00mander", ""); err == nil || !strings.Contains(err.Error(), "title") {
		t.Errorf("a control character in the title: %v", err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	if err := os.WriteFile(p, []byte("[you]\ntitle = \"Chief\"\nname = \"Ada\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || c.You.Label() != "Ada (Chief)" {
		t.Fatalf("Load = %q, %v", c.You.Label(), err)
	}
	if err := os.WriteFile(p, []byte("[you]\ntitel = \"Chief\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), p+": line 2: unknown key \"titel\"") {
		t.Errorf("a typo: %v", err)
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("#", maxFileBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a huge file: %v", err)
	}
	if _, err := Load(dir); err == nil {
		t.Error("loading a directory succeeded")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	if p, err := DefaultPath(); err != nil || p != filepath.Join("/x/config", "lucy", FileName) {
		t.Errorf("DefaultPath = %q, %v", p, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := DefaultPath(); err == nil {
		t.Error("a relative XDG_CONFIG_HOME was accepted")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "lucy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lucy", FileName), []byte("[you]\ntitle = \"Skipper\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadDefault(); err != nil || c.You.Title() != "Skipper" {
		t.Errorf("LoadDefault = %q, %v", c.You.Title(), err)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"title = \"x\"\n":                                       "line 1: key \"title\" is outside a section",
		"[api]\n":                                               "line 1: unknown section [api]",
		"[you]\n[you]\n":                                        "line 2: section [you] appears twice",
		"[you\n":                                                "line 1: want a [section] header",
		"[[you]]\n":                                             "line 1: want a [section] header",
		"[you] x\n":                                             "line 1: want a [section] header",
		"[you]\nemail = \"a@b\"\n":                              "line 2: unknown key \"email\" in [you]",
		"[you]\ntitle = \"a\"\ntitle = \"b\"\n":                 "line 3: key \"title\" appears twice",
		"[you]\ntitle\n":                                        "line 2: want key = \"value\"",
		"[you]\n= \"x\"\n":                                      "line 2: want key = \"value\"",
		"[you]\ntitle = Commander\n":                            "line 2: title: want a quoted string",
		"[you]\ntitle =\n":                                      "line 2: title: want a quoted string",
		"[you]\ntitle = \"Commander\n":                          "line 2: title: unterminated string",
		"[you]\ntitle = 'Commander\n":                           "line 2: title: unterminated string",
		"[you]\ntitle = \"a\" \"b\"\n":                          "line 2: title: unexpected text after the string",
		"[you]\ntitle = 'a' b\n":                                "line 2: title: unexpected text after the string",
		"[you]\ntitle = \"a\\x41\"\n":                           "line 2: title: unsupported escape",
		"[you]\ntitle = \"a\\u00\"\n":                           "line 2: title: bad escape",
		"[you]\ntitle = \"\"\n":                                 "line 2: title is empty",
		"[you]\nname = '   '\n":                                 "line 2: name is empty",
		"[you]\ntitle = \" Chief\"\n":                           "line 2: title has leading or trailing space",
		"[you]\ntitle = \"a\\tb\"\n":                            "line 2: title contains a control or invisible character",
		"[you]\nname = \"Ada\u200b\"\n":                         "line 2: name contains a control or invisible character",
		"[you]\ntitle = \"" + long() + "\"\n":                   "line 2: title is longer than 64 bytes",
		"[you]\ntitle = \"\xff\"\n":                             "not valid UTF-8",
		"[you]\ntitle = \"x\"\nname = \"\\U0010ffff\\u0000\"\n": "line 3: name contains a control or invisible character",
	}
	for in, want := range cases {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want an error containing %q", in, err, want)
		}
	}
}

func long() string { return strings.Repeat("a", MaxValueBytes+1) }

func mustParse(t *testing.T, in string) Config {
	t.Helper()
	c, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse(%q): %v", in, err)
	}
	return c
}
