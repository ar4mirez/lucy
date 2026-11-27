// Package harnesstest replays recorded harness protocol transcripts, for tests: a transcript plays
// the harness's side of a session, so an adapter and a runner are tested against the protocol
// without a real harness, a model, a login or the network.
//
// A transcript is JSON lines. The first is a header, {"version": "<v>", "answer": "<text>"}: the
// version the harness reports and, for a session that asks, the answer the test gives. Each later
// line is one of:
//
//	{"out": {...}}   a message the harness writes to its standard output
//	{"in": {...}}    a message the harness must read next on its input; every member given must
//	                 match, members not given are not checked
//	{"stderr": "x"}  a line the harness writes to its standard error
//
// After the last line the harness waits for its input to close and exits 0.
package harnesstest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// Header is a transcript's first line.
type Header struct {
	Version string `json:"version"`
	Answer  string `json:"answer,omitempty"`
	// Project, when set, is the directory of the project the session runs in (relative to the
	// transcript, for the caller to resolve). The harness then follows Claude Code's documented
	// settings rules on it, so a test sees what a project's committed settings would do: its
	// .claude/settings.json loads when --setting-sources is absent or names "project"; a tool its
	// permissions.allow lists runs without the transcript's permission prompt; and its hooks run,
	// each writing "project hook ran" to standard error.
	// When the loaded project settings set enableAllProjectMcpServers, every server in the
	// project's .mcp.json is made available, each writing "project MCP server loaded: <name>" to
	// standard error, unless the harness is started with --strict-mcp-config and an --mcp-config
	// whose mcpServers is empty.
	Project string `json:"project,omitempty"`
}

// projectRules is what a session's project settings would do under its arguments.
type projectRules struct {
	preallowed map[string]bool
	hooks      bool
	mcpServers []string
}

func rules(h Header, args []string) projectRules {
	r := projectRules{preallowed: map[string]bool{}}
	if h.Project == "" {
		return r
	}
	sources, sourcesSet, mcpConfig := "", false, ""
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--setting-sources":
			sources, sourcesSet = args[i+1], true
		case "--mcp-config":
			mcpConfig = args[i+1]
		}
	}
	if sourcesSet && !slices.Contains(strings.Split(sources, ","), "project") {
		return r
	}
	b, err := os.ReadFile(filepath.Join(h.Project, ".claude", "settings.json")) // #nosec G304 -- a test transcript's own fixture project
	if err != nil {
		return r
	}
	var project struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
		Hooks                      map[string]any `json:"hooks"`
		EnableAllProjectMcpServers bool           `json:"enableAllProjectMcpServers"`
	}
	if json.Unmarshal(b, &project) != nil {
		return r
	}
	for _, t := range project.Permissions.Allow {
		r.preallowed[t] = true
	}
	r.hooks = len(project.Hooks) > 0
	var strict struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if !project.EnableAllProjectMcpServers ||
		slices.Contains(args, "--strict-mcp-config") && json.Unmarshal([]byte(mcpConfig), &strict) == nil && len(strict.MCPServers) == 0 {
		return r
	}
	b, err = os.ReadFile(filepath.Join(h.Project, ".mcp.json")) // #nosec G304 -- a test transcript's own fixture project
	if err != nil {
		return r
	}
	var servers struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	if json.Unmarshal(b, &servers) != nil {
		return r
	}
	for name := range servers.MCPServers {
		r.mcpServers = append(r.mcpServers, name)
	}
	slices.Sort(r.mcpServers)
	return r
}

// promptedTool returns the tool a can_use_tool control request asks about, or "".
func promptedTool(out []byte) string {
	var m struct {
		Type    string `json:"type"`
		Request struct {
			Subtype  string `json:"subtype"`
			ToolName string `json:"tool_name"`
		} `json:"request"`
	}
	if json.Unmarshal(out, &m) != nil || m.Type != "control_request" || m.Request.Subtype != "can_use_tool" {
		return ""
	}
	return m.Request.ToolName
}

// Line is one later line of a transcript.
type Line struct {
	Out    json.RawMessage `json:"out,omitempty"`
	In     json.RawMessage `json:"in,omitempty"`
	Stderr string          `json:"stderr,omitempty"`
}

// Transcript is a parsed transcript.
type Transcript struct {
	Header Header
	Lines  []Line
}

// Parse reads a transcript.
func Parse(r io.Reader) (Transcript, error) {
	var t Transcript
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	n := 0
	for sc.Scan() {
		n++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var err error
		if n == 1 {
			err = dec.Decode(&t.Header)
		} else {
			var l Line
			if err = dec.Decode(&l); err == nil {
				t.Lines = append(t.Lines, l)
			}
		}
		if err != nil {
			return Transcript{}, fmt.Errorf("harnesstest: line %d: %w", n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return Transcript{}, err
	}
	if t.Header.Version == "" {
		return Transcript{}, fmt.Errorf("harnesstest: the first line must be a header with a version")
	}
	return t, nil
}

// Matches reports whether got has every member of want, recursively: objects match on want's
// members, arrays element by element, everything else by equality.
func Matches(want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !Matches(wv, gv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !Matches(w[i], g[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(want, got)
}

// MatchJSON reports whether the JSON got matches the JSON want, as Matches does.
func MatchJSON(want, got []byte) bool {
	var w, g any
	if json.Unmarshal(want, &w) != nil || json.Unmarshal(got, &g) != nil {
		return false
	}
	return Matches(w, g)
}

// Replay plays t as the harness, started with args: it prints the version for --version, and
// otherwise writes every out line and checks every in line against what it reads. It returns the
// process's exit status: 0 when the input matched and was closed, 3 when it did not match.
func Replay(t Transcript, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if slices.Contains(args, "--version") {
		fmt.Fprintf(stdout, "%s (Claude Code)\n", t.Header.Version)
		return 0
	}
	in := bufio.NewScanner(stdin)
	in.Buffer(make([]byte, 0, 64<<10), 4<<20)
	project := rules(t.Header, args)
	if project.hooks {
		fmt.Fprintln(stderr, "project hook ran")
	}
	for _, name := range project.mcpServers {
		fmt.Fprintln(stderr, "project MCP server loaded: "+name)
	}
	skipIn := false
	for i, l := range t.Lines {
		switch {
		case l.Out != nil && project.preallowed[promptedTool(l.Out)]:
			skipIn = true // the project pre-allowed the tool: it runs without asking the host
		case l.In != nil && skipIn:
			skipIn = false
		case l.Out != nil:
			if _, err := fmt.Fprintf(stdout, "%s\n", l.Out); err != nil {
				return 1
			}
		case l.In != nil:
			if !in.Scan() {
				fmt.Fprintf(stderr, "fake harness: line %d: input ended, want %s\n", i+2, l.In)
				return 3
			}
			if !MatchJSON(l.In, in.Bytes()) {
				fmt.Fprintf(stderr, "fake harness: line %d: read %s, want %s\n", i+2, in.Bytes(), l.In)
				return 3
			}
		case l.Stderr != "":
			fmt.Fprintln(stderr, l.Stderr)
		}
	}
	for in.Scan() {
		// the session is over; anything more is ignored until the input closes
	}
	return 0
}
