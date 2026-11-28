package claudecode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/ar4mirez/lucy/internal/harness"
)

// The files Claude Code keeps its account state in, inside its home: the state file, and the
// user settings file. Converge reads and writes them through the home's Root only.
const (
	stateFile    = ".claude.json"
	settingsDir  = ".claude"
	settingsFile = ".claude/settings.json"
)

// maxStateBytes bounds the state file Converge reads.
const maxStateBytes = 8 << 20

// Converge implements harness.Converger (LUCY-RUN-008). In the org's synthetic home it marks
// onboarding complete and turns auto-updates off, marks the run's directory trusted and onboarded
// with no pre-allowed tools and no MCP servers, removes user-scope MCP servers, and removes the user
// settings' hooks and disables them. Every other key, the login above all, is kept as it is. A
// state or settings file that is a symlink, or a settings directory that is one, is refused: lucy
// never writes through a link. A session loads no user settings file anyway (--setting-sources is
// empty); the settings are re-asserted so nothing in the home can add a hook.
func (Adapter) Converge(home *os.Root, runDir string) ([]string, error) {
	var changed []string
	state, err := readJSON(home, stateFile)
	if err != nil {
		return nil, err
	}
	set := func(m map[string]any, key string, v any, what string) {
		if !sameJSON(m[key], v) {
			m[key] = v
			changed = append(changed, what)
		}
	}
	set(state, "hasCompletedOnboarding", true, "onboarding marked complete")
	set(state, "autoUpdates", false, "auto-updates turned off")
	set(state, "mcpServers", map[string]any{}, "user-scope MCP servers removed")
	projects, _ := state["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
		state["projects"] = projects
	}
	project, _ := projects[runDir].(map[string]any)
	if project == nil {
		project = map[string]any{}
		projects[runDir] = project
	}
	set(project, "hasTrustDialogAccepted", true, "the run's directory trusted")
	set(project, "hasCompletedProjectOnboarding", true, "the run's directory onboarded")
	set(project, "allowedTools", []any{}, "the run's directory's pre-allowed tools removed")
	set(project, "mcpServers", map[string]any{}, "the run's directory's MCP servers removed")
	set(project, "enableAllProjectMcpServers", false, "the run's directory's project MCP servers disabled")
	if len(changed) > 0 {
		if err := writeJSON(home, stateFile, state); err != nil {
			return nil, err
		}
	}
	n := len(changed)
	if err := noLink(home, settingsDir); err != nil {
		return nil, err
	}
	if err := home.MkdirAll(settingsDir, 0o700); err != nil {
		return nil, fmt.Errorf("claudecode: converge: %w", err)
	}
	settings, err := readJSON(home, settingsFile)
	if err != nil {
		return nil, err
	}
	if _, ok := settings["hooks"]; ok {
		delete(settings, "hooks")
		changed = append(changed, "user settings' hooks removed")
	}
	set(settings, "disableAllHooks", true, "hooks disabled")
	if len(changed) > n {
		if err := writeJSON(home, settingsFile, settings); err != nil {
			return nil, err
		}
	}
	return changed, nil
}

// noLink refuses name when it is a symlink.
func noLink(home *os.Root, name string) error {
	fi, err := home.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("claudecode: converge: %w", err)
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("claudecode: converge: %s in the org's home is a symlink; lucy never writes through one", name)
	}
	return nil
}

// readJSON reads name, a JSON object, from home; a file that is missing or empty is an empty object.
func readJSON(home *os.Root, name string) (map[string]any, error) {
	if err := noLink(home, name); err != nil {
		return nil, err
	}
	fi, err := home.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claudecode: converge: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxStateBytes {
		return nil, fmt.Errorf("claudecode: converge: %s in the org's home is not a regular file of at most %d bytes", name, maxStateBytes)
	}
	data, err := home.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("claudecode: converge: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	err = dec.Decode(&v)
	if err == nil && dec.Decode(new(any)) != io.EOF {
		err = errors.New("trailing data after the object")
	}
	m, ok := v.(map[string]any)
	if err == nil && !ok {
		err = errors.New("not an object")
	}
	if err != nil {
		return nil, fmt.Errorf("claudecode: converge: %s in the org's home is not a JSON object: %w", name, err)
	}
	return m, nil
}

// writeJSON writes m to name in home, through a temporary file renamed over it, 0600.
func writeJSON(home *os.Root, name string, m map[string]any) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := name + ".lucy-tmp"
	if err := home.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("claudecode: converge: %w", err)
	}
	if err := home.Rename(tmp, name); err != nil {
		_ = home.Remove(tmp) // the rename error is what matters
		return fmt.Errorf("claudecode: converge: %w", err)
	}
	return nil
}

// sameJSON reports whether a and b encode to the same JSON.
func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// Needs implements harness.Prerequisite: a session needs a login in the org's home, which Claude
// Code keeps as .claude/.credentials.json, or, on macOS, in the keychain, with the account named in
// .claude.json's oauthAccount. Needs checks only that one of them is there: it never opens the
// credentials file, and of .claude.json it looks at no more than whether oauthAccount is set.
func (Adapter) Needs(home *os.Root) string {
	if fi, err := home.Lstat(".claude/.credentials.json"); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
		return ""
	}
	if state, err := readJSON(home, stateFile); err == nil {
		if acct, ok := state["oauthAccount"].(map[string]any); ok && len(acct) > 0 {
			return ""
		}
	}
	return harness.NeedLogin
}

// SkillsDir implements harness.SkillHost: Claude Code reads user skills from .claude/skills.
// TODO(LUCY-RUN-009): a session still runs with skills off (--disable-slash-commands), since a
// skill's allowed-tools would skip the permission prompt and turning skills on may load a
// repository's own; lucy's skills are installed and verified, and switched on only once a live
// contract check shows neither can happen.
func (Adapter) SkillsDir() string { return ".claude/skills" }
