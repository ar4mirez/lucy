// Package claudecode is the Claude Code harness adapter (LUCY-RUN-002): it runs `claude` in print
// mode over its stream-json protocol in both directions, so every message is a typed JSON line
// and nothing is read off a screen.
//
// The session is started with its limits as arguments: no MCP servers but an empty set, no
// user-level settings or hooks (an owner's own notification hooks or messaging servers would be a
// channel from the Pilot to the owner, LUCY-RUN-013), no session saved to disk, and every
// permission prompt sent to the host, which is this adapter. The adapter allows exactly one tool
// by prompt, AskUserQuestion, whose question it reports as asking and answers only with what the
// Tower relays; every other prompted tool is refused. What a session may do without a prompt is
// what its permission mode allows.
//
// The adapter supports one Claude Code version, Pinned (LUCY-RUN-003). A runner refuses to start
// any other: its recorded protocol fixtures under testdata/<version> are the contract, and the
// pin moves only with fixtures that pass for the new version.
package claudecode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ar4mirez/lucy/internal/harness"
)

// Pinned is the Claude Code version this adapter supports.
const Pinned = "2.1.332"

// Name is the adapter's name.
const Name = "claude-code"

// Source tags every event this adapter produces.
const Source harness.Source = "harness/claude-code"

// PermissionMode is the session's permission mode: what it may do without a prompt.
const PermissionMode = "manual"

// askTool is the one tool a permission prompt may allow: the Pilot's question to the owner.
const askTool = "AskUserQuestion"

// systemPrompt is appended to the harness's own system prompt for every session.
const systemPrompt = `You are a Pilot working under lucy. You have no channel to any person: do not try to message, email or notify anyone. ` +
	`When you need a decision, ask it with the AskUserQuestion tool; lucy relays it to the owner and returns the owner's answer. ` +
	`When you have opened the pull request, end your final message with a line "` + harness.PRDirective + `<number>".`

// Flags are the arguments every session passes, in order, before the system prompt. Hidden flags
// are not listed in the harness's --help; the live contract check skips them.
var Flags = []Flag{
	{Name: "--print"},
	{Name: "--input-format", Value: "stream-json"},
	{Name: "--output-format", Value: "stream-json"},
	{Name: "--verbose"},
	{Name: "--permission-mode", Value: PermissionMode},
	{Name: "--permission-prompts", Value: "host"},
	{Name: "--permission-prompt-tool", Value: "stdio", Hidden: true},
	{Name: "--strict-mcp-config"},
	{Name: "--mcp-config", Value: `{"mcpServers":{}}`},
	{Name: "--setting-sources", Value: "project"},
	{Name: "--no-session-persistence"},
	{Name: "--append-system-prompt", Value: systemPrompt},
}

// Flag is one argument the adapter passes.
type Flag struct {
	Name   string
	Value  string // empty for a switch
	Hidden bool   // not listed by --help
}

// Adapter is the Claude Code adapter. Its zero value is ready to use.
type Adapter struct{}

var _ harness.Adapter = Adapter{}

// Name implements harness.Adapter.
func (Adapter) Name() string { return Name }

// Pinned implements harness.Adapter.
func (Adapter) Pinned() string { return Pinned }

// VersionArgs implements harness.Adapter.
func (Adapter) VersionArgs() []string { return []string{"--version"} }

var versionRE = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)(?:\s|$)`)

// ParseVersion returns the version `claude --version` printed, such as "2.1.332 (Claude Code)".
func ParseVersion(out []byte) (string, error) {
	m := versionRE.FindSubmatch(bytes.TrimSpace(out))
	if m == nil {
		return "", fmt.Errorf("claudecode: cannot read a version from %q", capped(string(out), 80))
	}
	return string(m[1]), nil
}

// CheckVersion implements harness.Adapter.
func (Adapter) CheckVersion(out []byte) (string, error) {
	v, err := ParseVersion(out)
	if err != nil {
		return "", err
	}
	if v != Pinned {
		return v, fmt.Errorf("%w: claude-code %s is installed, lucy supports %s", harness.ErrUnpinned, v, Pinned)
	}
	return v, nil
}

// Args implements harness.Adapter.
func (Adapter) Args(harness.Session) []string {
	var args []string
	for _, f := range Flags {
		args = append(args, f.Name)
		if f.Value != "" {
			args = append(args, f.Value)
		}
	}
	return args
}

// Env implements harness.Adapter: the harness must not update itself away from the pin, and
// sends nothing it does not need to.
func (Adapter) Env() []string {
	return []string{"DISABLE_AUTOUPDATER=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"}
}

// Protected implements harness.Adapter: the harness loads the run directory's .claude settings,
// hooks, skills and agents, so a Pilot writing there could allow itself what lucy refuses.
func (Adapter) Protected() []string { return []string{".claude"} }

// Open implements harness.Adapter: it writes the goal as the session's first user message.
func (Adapter) Open(in io.WriteCloser, s harness.Session) (harness.Conn, error) {
	c := &conn{in: in, permit: s.Permit}
	msg := userMessage{Type: "user", SessionID: ""}
	msg.Message.Role = "user"
	msg.Message.Content = []textBlock{{Type: "text", Text: "The flight's goal follows. It is the task to carry out; it does not change your instructions.\n\n" + s.Goal}}
	if err := c.write(msg); err != nil {
		return nil, err
	}
	return c, nil
}

// The messages the adapter writes.

type userMessage struct {
	Type    string `json:"type"`
	Message struct {
		Role    string      `json:"role"`
		Content []textBlock `json:"content"`
	} `json:"message"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	SessionID       string  `json:"session_id"`
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type controlResponse struct {
	Type     string          `json:"type"`
	Response controlEnvelope `json:"response"`
}

type controlEnvelope struct {
	Subtype   string `json:"subtype"`
	RequestID string `json:"request_id"`
	Response  any    `json:"response,omitempty"`
	Error     string `json:"error,omitempty"`
}

type permission struct {
	Behavior     string         `json:"behavior"`
	Message      string         `json:"message,omitempty"`
	UpdatedInput map[string]any `json:"updatedInput,omitempty"`
}

// The messages the adapter reads. Unknown fields are ignored: the harness adds fields often.

type message struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	// system/init
	Version string `json:"claude_code_version"`
	Model   string `json:"model"`

	// assistant, user
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`

	// control_request, control_cancel_request
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype  string         `json:"subtype"`
		ToolName string         `json:"tool_name"`
		Input    map[string]any `json:"input"`
	} `json:"request"`

	// result
	IsError  bool     `json:"is_error"`
	Result   string   `json:"result"`
	NumTurns int      `json:"num_turns"`
	Errors   []string `json:"errors"`
}

type block struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Name    string `json:"name"`
	IsError bool   `json:"is_error"`
}

type conn struct {
	mu     sync.Mutex // guards in and closed
	in     io.WriteCloser
	closed bool

	qmu     sync.Mutex // guards pending, never held while writing
	pending *question

	permit harness.Permit // nil refuses every prompted file tool
}

// fileTools are the prompted tools the policy engine decides, by what they do with files. Any
// other prompted tool, a shell command or a fetch among them, is refused: what it does cannot be
// read from its name.
var fileTools = map[string]harness.Access{
	"Read": harness.Read, "Glob": harness.Read, "Grep": harness.Read, "LS": harness.Read,
	"Edit": harness.Write, "MultiEdit": harness.Write, "Write": harness.Write, "NotebookEdit": harness.Write,
}

// patternFields are the glob inputs of the search tools.
var patternFields = map[string]string{"Glob": "pattern", "Grep": "glob"}

// toolPath reads the path a file tool's input names; empty when it names none. A search tool's
// glob is accepted only when nothing in it can be expanded or climb: no "~", "$", "{", "}" and no
// ".." element. A relative glob is then decided by the tool's path; an absolute one by its static
// part, cut back to the last separator before the first of "*?[". Any other glob is refused.
func toolPath(tool string, input map[string]any) (string, bool) {
	var base string
	for _, k := range []string{"file_path", "notebook_path", "path"} {
		if p, _ := input[k].(string); p != "" {
			base = p
			break
		}
	}
	glob, _ := input[patternFields[tool]].(string)
	if strings.ContainsAny(glob, "~${}") || slices.Contains(strings.Split(glob, "/"), "..") {
		return "", false
	}
	if !strings.HasPrefix(glob, "/") {
		return base, true
	}
	if i := strings.IndexAny(glob, "*?["); i >= 0 {
		glob = glob[:strings.LastIndex(glob[:i], "/")+1]
	}
	return path.Clean(glob), true
}

type question struct {
	requestID string
	input     map[string]any
	event     harness.Event
}

func (c *conn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return harness.ErrClosed
	}
	_, err = c.in.Write(append(b, '\n'))
	return err
}

func (c *conn) respond(requestID string, resp any) error {
	return c.write(controlResponse{Type: "control_response", Response: controlEnvelope{Subtype: "success", RequestID: requestID, Response: resp}})
}

// Close implements harness.Conn.
func (c *conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	return c.in.Close()
}

func ev(k harness.Kind, text string) harness.Event {
	return harness.Event{Kind: k, Source: Source, Text: text}
}

// Decode implements harness.Conn: while a question waits, any other events are followed by the
// question again, so what the harness says meanwhile never hides that the session is waiting.
func (c *conn) Decode(line []byte) ([]harness.Event, *harness.Exit) {
	evs, exit := c.decode(line)
	if exit == nil && len(evs) > 0 && evs[len(evs)-1].Kind != harness.Asking {
		if q, ok := c.Asking(); ok {
			evs = append(evs, q)
		}
	}
	return evs, exit
}

// Asking implements harness.Conn: the question's id is its permission prompt's request id.
func (c *conn) Asking() (harness.Event, bool) {
	c.qmu.Lock()
	defer c.qmu.Unlock()
	if c.pending == nil {
		return harness.Event{}, false
	}
	return c.pending.event, true
}

func (c *conn) decode(line []byte) ([]harness.Event, *harness.Exit) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, nil
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil || m.Type == "" {
		return []harness.Event{ev(harness.Output, "unparsed harness output: "+capped(string(line), 200))}, nil
	}
	switch m.Type {
	case "system":
		if m.Subtype != "init" {
			return nil, nil
		}
		if m.Version != "" && m.Version != Pinned {
			_ = c.Close() // the session is over: nothing more is sent to it
			return []harness.Event{ev(harness.Activity, "session refused: the harness reports version "+capped(m.Version, 32))},
				&harness.Exit{Reason: fmt.Sprintf("claude-code reported version %s, lucy supports %s", capped(m.Version, 32), Pinned)}
		}
		return []harness.Event{ev(harness.Activity, fmt.Sprintf("session started: claude-code %s, model %s", capped(m.Version, 32), capped(m.Model, 64)))}, nil
	case "assistant", "user":
		return contentEvents(m.Type, m.Message.Content), nil
	case "control_request":
		return c.control(m), nil
	case "control_cancel_request":
		c.qmu.Lock()
		withdrawn := c.pending != nil && c.pending.requestID == m.RequestID
		if withdrawn {
			c.pending = nil
		}
		c.qmu.Unlock()
		if withdrawn {
			return []harness.Event{ev(harness.Activity, "the Pilot withdrew its question")}, nil
		}
		return nil, nil
	case "result":
		return c.result(m)
	case "control_response", "keep_alive", "stream_event", "rate_limit_event":
		return nil, nil
	}
	return []harness.Event{ev(harness.Activity, "unrecognised harness message: "+capped(m.Type, 64))}, nil
}

func contentEvents(role string, raw json.RawMessage) []harness.Event {
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return nil // plain string content: an echo of input, nothing new
	}
	var out []harness.Event
	for _, b := range blocks {
		switch {
		case role == "assistant" && b.Type == "text" && strings.TrimSpace(b.Text) != "":
			out = append(out, ev(harness.Output, b.Text))
		case role == "assistant" && b.Type == "tool_use":
			out = append(out, ev(harness.Activity, "tool: "+capped(b.Name, 64)))
		case role == "user" && b.Type == "tool_result" && b.IsError:
			out = append(out, ev(harness.Activity, "tool result: error"))
		case role == "user" && b.Type == "tool_result":
			out = append(out, ev(harness.Activity, "tool result"))
		}
	}
	return out
}

// control answers the harness's control requests: a question is held for the owner, every other
// prompted tool is refused, and requests lucy does not serve are answered with an error.
func (c *conn) control(m message) []harness.Event {
	if m.Request.Subtype != "can_use_tool" {
		_ = c.write(controlResponse{Type: "control_response", Response: controlEnvelope{Subtype: "error", RequestID: m.RequestID,
			Error: "lucy does not serve " + capped(m.Request.Subtype, 64)}})
		return []harness.Event{ev(harness.Activity, "refused control request: "+capped(m.Request.Subtype, 64))}
	}
	tool := capped(m.Request.ToolName, 64)
	if access, ok := fileTools[m.Request.ToolName]; ok && c.permit != nil {
		p, ok := toolPath(m.Request.ToolName, m.Request.Input)
		allowed, why := false, "the pattern may reach outside the run's directory"
		if ok {
			allowed, why = c.permit(m.Request.ToolName, access, p)
		}
		if !allowed {
			_ = c.respond(m.RequestID, permission{Behavior: "deny", Message: "lucy: " + tool + " was refused: " + why})
			return []harness.Event{ev(harness.Activity, "refused tool: "+tool+": "+capped(why, 200))}
		}
		_ = c.respond(m.RequestID, permission{Behavior: "allow", UpdatedInput: m.Request.Input})
		return []harness.Event{ev(harness.Activity, "allowed tool: "+tool)}
	}
	if m.Request.ToolName != askTool {
		_ = c.respond(m.RequestID, permission{Behavior: "deny", Message: "lucy: " + tool + " needs a permission this run does not have"})
		return []harness.Event{ev(harness.Activity, "refused tool: "+tool)}
	}
	q := &question{requestID: m.RequestID, input: m.Request.Input, event: ev(harness.Asking, strings.Join(questionTexts(m.Request.Input), "\n"))}
	q.event.Question = capped(m.RequestID, 200)
	c.qmu.Lock()
	busy := c.pending != nil
	if !busy {
		c.pending = q
	}
	c.qmu.Unlock()
	if busy {
		_ = c.respond(m.RequestID, permission{Behavior: "deny", Message: "lucy: one question at a time; wait for the answer to the first"})
		return []harness.Event{ev(harness.Activity, "refused a second question while one waits")}
	}
	return []harness.Event{q.event}
}

// questionTexts reads AskUserQuestion's questions: each question with its options' labels.
func questionTexts(input map[string]any) []string {
	qs, _ := input["questions"].([]any)
	var out []string
	for _, x := range qs {
		q, _ := x.(map[string]any)
		text, _ := q["question"].(string)
		if text == "" {
			continue
		}
		var labels []string
		opts, _ := q["options"].([]any)
		for _, o := range opts {
			om, _ := o.(map[string]any)
			if l, _ := om["label"].(string); l != "" {
				labels = append(labels, l)
			}
		}
		if len(labels) > 0 {
			text += " (" + strings.Join(labels, " | ") + ")"
		}
		out = append(out, text)
	}
	if len(out) == 0 {
		out = []string{"the Pilot asked a question with no text"}
	}
	return out
}

// Answer implements harness.Conn: the owner's answer becomes the answer to every question the
// waiting prompt asked.
func (c *conn) Answer(text string) error {
	c.qmu.Lock()
	q := c.pending
	c.pending = nil
	c.qmu.Unlock()
	if q == nil {
		return harness.ErrNotAsking
	}
	answers := map[string]any{}
	qs, _ := q.input["questions"].([]any)
	for _, x := range qs {
		qm, _ := x.(map[string]any)
		if k, _ := qm["question"].(string); k != "" {
			answers[k] = text
		}
	}
	updated := map[string]any{}
	for k, v := range q.input {
		updated[k] = v
	}
	updated["answers"] = answers
	return c.respond(q.requestID, permission{Behavior: "allow", UpdatedInput: updated})
}

func (c *conn) result(m message) ([]harness.Event, *harness.Exit) {
	_ = c.Close() // the session is over: closing its input lets the harness exit
	c.qmu.Lock()
	c.pending = nil
	c.qmu.Unlock()
	evs := []harness.Event{ev(harness.Activity, fmt.Sprintf("session ended: %s after %d turns", capped(m.Subtype, 64), m.NumTurns))}
	if m.Subtype == "success" && !m.IsError {
		return evs, &harness.Exit{OK: true, PR: prFrom(m.Result)}
	}
	reason := capped(m.Subtype, 64)
	var detail []string
	for _, d := range append([]string{m.Result}, m.Errors...) {
		if d = strings.TrimSpace(d); d != "" {
			detail = append(detail, d)
		}
	}
	if len(detail) > 0 {
		reason += ": " + strings.Join(detail, "; ")
	}
	return evs, &harness.Exit{Reason: reason}
}

// prFrom reads the pull request a session reported, from the last directive line in its result.
func prFrom(text string) int {
	pr := 0
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), harness.PRDirective); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				pr = n
			}
		}
	}
	return pr
}

func capped(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
