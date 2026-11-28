package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openHome(t *testing.T) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return dir, r
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// Converge puts the account state back to what lucy relies on, in the org's home: onboarding done,
// the run's directory trusted with no tools pre-allowed and no MCP servers, no user-scope MCP
// servers, no hooks; it keeps every other key, and on state that already holds it changes nothing
// (LUCY-RUN-008).
func TestConverge(t *testing.T) {
	dir, home := openHome(t)
	drifted := `{"hasCompletedOnboarding": false, "oauthAccount": {"emailAddress": "someone@example.test"},
		"mcpServers": {"slack": {"command": "x"}},
		"projects": {"/work/run": {"allowedTools": ["Bash"], "hasTrustDialogAccepted": false}, "/other": {"allowedTools": ["Read"]}}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(drifted), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte(`{"hooks": {"Stop": []}, "theme": "dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := Adapter{}.Converge(home, "/work/run")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"onboarding marked complete", "the run's directory trusted", "user-scope MCP servers removed",
		"the run's directory's pre-allowed tools removed", "user settings' hooks removed", "hooks disabled"} {
		found := false
		for _, c := range changed {
			found = found || c == want
		}
		if !found {
			t.Errorf("changed %v lacks %q", changed, want)
		}
	}
	st := readJSONFile(t, filepath.Join(dir, ".claude.json"))
	proj := st["projects"].(map[string]any)["/work/run"].(map[string]any)
	if st["hasCompletedOnboarding"] != true || len(st["mcpServers"].(map[string]any)) != 0 || proj["hasTrustDialogAccepted"] != true ||
		len(proj["allowedTools"].([]any)) != 0 || len(proj["mcpServers"].(map[string]any)) != 0 {
		t.Errorf("state after converge: %v", st)
	}
	if st["oauthAccount"] == nil || st["projects"].(map[string]any)["/other"] == nil {
		t.Errorf("converge dropped keys it does not own: %v", st)
	}
	set := readJSONFile(t, filepath.Join(dir, ".claude", "settings.json"))
	if _, ok := set["hooks"]; ok || set["disableAllHooks"] != true || set["theme"] != "dark" {
		t.Errorf("settings after converge: %v", set)
	}
	if again, err := (Adapter{}).Converge(home, "/work/run"); err != nil || len(again) != 0 {
		t.Fatalf("a second converge changed %v, %v; want nothing", again, err)
	}
	fresh, freshHome := openHome(t)
	if changed, err := (Adapter{}).Converge(freshHome, "/work/run"); err != nil || len(changed) == 0 {
		t.Fatalf("an empty home: %v, %v", changed, err)
	}
	if fi, err := os.Stat(filepath.Join(fresh, ".claude.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the state file: %v, %v", fi, err)
	}
}

// Converge never writes through a link and never leaves the home: a state file, settings file or
// settings directory that is a symlink is refused, and what it points to is untouched.
func TestConvergeRefusesLinks(t *testing.T) {
	for _, link := range []string{".claude.json", ".claude", ".claude/settings.json"} {
		t.Run(link, func(t *testing.T) {
			dir, home := openHome(t)
			outside := t.TempDir()
			target := filepath.Join(outside, "target")
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if link != ".claude" {
				target = filepath.Join(outside, "target.json")
				if err := os.WriteFile(target, []byte(`{"kept": true}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(link, "/") {
				if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
				t.Fatal(err)
			}
			if _, err := (Adapter{}).Converge(home, "/work/run"); err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("a symlinked %s: %v, want a refusal", link, err)
			}
			if link != ".claude" {
				if data, _ := os.ReadFile(target); string(data) != `{"kept": true}` {
					t.Fatalf("the link's target changed: %s", data)
				}
			} else if entries, _ := os.ReadDir(target); len(entries) != 0 {
				t.Fatalf("converge wrote into the linked directory: %v", entries)
			}
		})
	}
}

// A state or settings file that is not a JSON object (not JSON, null, another JSON value, or an
// object followed by more data) is refused rather than overwritten.
func TestConvergeRefusesAnUnreadableState(t *testing.T) {
	for _, name := range []string{".claude.json", ".claude/settings.json"} {
		for _, content := range []string{"not json", "null", "[]", `"x"`, "1", `{} {}`, `{"a": 1} x`} {
			t.Run(name+"/"+content, func(t *testing.T) {
				dir, home := openHome(t)
				if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := (Adapter{}).Converge(home, "/work/run"); err == nil || !strings.Contains(err.Error(), "not a JSON object") {
					t.Fatalf("converge on %q: %v, want a refusal", content, err)
				}
				if data, _ := os.ReadFile(filepath.Join(dir, name)); string(data) != content {
					t.Fatalf("it was overwritten: %q", data)
				}
			})
		}
	}
}

// Numbers in keys Converge does not own keep their exact spelling when it rewrites a file.
func TestConvergeKeepsNumbers(t *testing.T) {
	dir, home := openHome(t)
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".claude.json", ".claude/settings.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"big": 9007199254740993, "one": 1.0}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if changed, err := (Adapter{}).Converge(home, "/work/run"); err != nil || len(changed) == 0 {
		t.Fatalf("converge: %v, %v", changed, err)
	}
	for _, name := range []string{".claude.json", ".claude/settings.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		if string(m["big"]) != "9007199254740993" || string(m["one"]) != "1.0" {
			t.Errorf("%s after converge: big %s, one %s", name, m["big"], m["one"])
		}
	}
}
