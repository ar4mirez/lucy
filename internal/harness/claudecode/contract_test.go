package claudecode

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/harness"
	"github.com/ar4mirez/lucy/internal/harness/harnesstest"
)

var update = flag.Bool("update", false, "rewrite the golden files from the transcripts")

// capture is the harness's input, as the adapter writes it.
type capture struct {
	buf    bytes.Buffer
	closed bool
}

func (c *capture) Write(b []byte) (int, error) {
	if c.closed {
		return 0, errors.New("write after close")
	}
	return c.buf.Write(b)
}

func (c *capture) Close() error { c.closed = true; return nil }

func (c *capture) lines() [][]byte {
	return bytes.Split(bytes.TrimSuffix(c.buf.Bytes(), []byte("\n")), []byte("\n"))
}

// fakePermit stands in for the policy engine: it allows file tools inside the run's directory, as
// the transcripts record it, and refuses the rest.
func fakePermit(tool string, access harness.Access, path string) (bool, string) {
	if path == "" || strings.HasPrefix(path, "/work/run-1/") {
		return true, ""
	}
	return false, "the path is outside the run's directory"
}

// required are the transcripts every pinned version must have: the contract.
var required = []string{"ask", "done", "drift", "error", "permitted", "refused"}

// TestContract replays every recorded transcript of the pinned version through the adapter: what
// the adapter writes must match each recorded input, and what it reports must match the golden
// file. Moving the pin needs this directory for the new version, so an unverified harness is never
// supported (LUCY-RUN-003).
func TestContract(t *testing.T) {
	dir := filepath.Join("testdata", Pinned)
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no transcripts for the pinned version %s in %s: %v", Pinned, dir, err)
	}
	var names []string
	for _, p := range paths {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".jsonl"))
	}
	for _, r := range required {
		if !slices.Contains(names, r) {
			t.Errorf("the pinned version has no %s transcript", r)
		}
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			tr, err := harnesstest.Parse(f)
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if tr.Header.Version != Pinned {
				t.Fatalf("transcript records version %s, the pin is %s", tr.Header.Version, Pinned)
			}
			got := replay(t, tr)
			golden := strings.TrimSuffix(p, ".jsonl") + ".golden"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if got != string(want) {
				t.Errorf("events differ from %s:\n--- got\n%s--- want\n%s", golden, got, want)
			}
		})
	}
}

// replay plays the harness's side of tr against the adapter and returns what the adapter reported.
func replay(t *testing.T, tr harnesstest.Transcript) string {
	t.Helper()
	in := &capture{}
	conn, err := Adapter{}.Open(in, harness.Session{Goal: "make the failing test pass", Permit: fakePermit})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	read, ended := 0, false
	for i, l := range tr.Lines {
		switch {
		case l.Out != nil:
			evs, exit := conn.Decode(l.Out)
			for _, e := range evs {
				fmt.Fprintf(&b, "%s %s %q\n", e.Source, e.Kind, e.Text)
				if e.Kind == harness.Asking {
					if err := conn.Answer(tr.Header.Answer); err != nil {
						t.Fatalf("line %d: answer: %v", i+2, err)
					}
					b.WriteString("answered\n")
				}
			}
			if exit != nil {
				ended = true
				fmt.Fprintf(&b, "exit ok=%v pr=%d reason=%q\n", exit.OK, exit.PR, exit.Reason)
			}
		case l.In != nil:
			lines := in.lines()
			if read >= len(lines) || len(lines[read]) == 0 {
				t.Fatalf("line %d: the adapter wrote nothing, want %s", i+2, l.In)
			}
			if !harnesstest.MatchJSON(l.In, lines[read]) {
				t.Fatalf("line %d: the adapter wrote %s, want %s", i+2, lines[read], l.In)
			}
			read++
		}
	}
	if lines := in.lines(); read != len(lines) {
		t.Errorf("the adapter wrote %d messages the transcript does not expect: %s", len(lines)-read, bytes.Join(lines[read:], []byte("\n")))
	}
	if ended != in.closed {
		t.Errorf("session ended %v, input closed %v: the adapter closes the input exactly when the session ends", ended, in.closed)
	}
	return b.String()
}

func TestCheckVersion(t *testing.T) {
	for _, c := range []struct {
		out     string
		version string
		err     error
	}{
		{Pinned + " (Claude Code)\n", Pinned, nil},
		{"v" + Pinned + "\n", Pinned, nil},
		{"2.1.400 (Claude Code)", "2.1.400", harness.ErrUnpinned},
		{"claude: command not found", "", nil},
		{"", "", nil},
	} {
		v, err := Adapter{}.CheckVersion([]byte(c.out))
		switch {
		case v != c.version:
			t.Errorf("CheckVersion(%q) = %q, want %q", c.out, v, c.version)
		case c.version == "" && err == nil:
			t.Errorf("CheckVersion(%q) accepted no version", c.out)
		case c.err != nil && !errors.Is(err, c.err):
			t.Errorf("CheckVersion(%q) = %v, want %v", c.out, err, c.err)
		case c.version == Pinned && err != nil:
			t.Errorf("CheckVersion(%q) = %v", c.out, err)
		}
	}
}

// TestArgs pins the session's limits: the structured protocol both ways, prompts to the host, an
// empty MCP set, no user settings, nothing saved, and no flag that skips permissions.
func TestArgs(t *testing.T) {
	args := strings.Join(Adapter{}.Args(harness.Session{Goal: "g"}), " ")
	for _, want := range []string{"--print", "--input-format stream-json", "--output-format stream-json", "--permission-prompts host",
		"--strict-mcp-config", `--mcp-config {"mcpServers":{}}`, "--setting-sources project", "--no-session-persistence",
		"--permission-mode " + PermissionMode} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	for _, bad := range []string{"dangerously", "bypassPermissions", "--resume", "--continue", "user"} {
		if strings.Contains(args, bad) {
			t.Errorf("args contain %q: %s", bad, args)
		}
	}
	if env := strings.Join(Adapter{}.Env(), " "); !strings.Contains(env, "DISABLE_AUTOUPDATER=1") {
		t.Errorf("env %q lets the harness update itself away from the pin", env)
	}
}

func TestAnswerWithoutQuestion(t *testing.T) {
	in := &capture{}
	conn, err := Adapter{}.Open(in, harness.Session{Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Answer("yes"); !errors.Is(err, harness.ErrNotAsking) {
		t.Fatalf("Answer with no question = %v, want ErrNotAsking", err)
	}
	if err := conn.Close(); err != nil || !in.closed {
		t.Fatalf("Close = %v, closed %v", err, in.closed)
	}
	if err := conn.Answer("yes"); !errors.Is(err, harness.ErrNotAsking) {
		t.Fatalf("Answer after close = %v", err)
	}
}

// TestQuestionStaysOpen: while a question waits on the owner, whatever else the harness says,
// such as a second question it is refused or a line lucy cannot parse, is followed by the open
// question again, so the session never looks as if it stopped asking; once answered, it is not.
func TestQuestionStaysOpen(t *testing.T) {
	in := &capture{}
	conn, err := Adapter{}.Open(in, harness.Session{Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	ask := func(id, q string) []byte {
		return []byte(`{"type":"control_request","request_id":"` + id + `","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"` + q + `"}]}}}`)
	}
	const open = "Which account?"
	kinds := func(line []byte) string {
		evs, exit := conn.Decode(line)
		if exit != nil {
			t.Fatalf("Decode(%s) ended the session", line)
		}
		var b strings.Builder
		for _, e := range evs {
			fmt.Fprintf(&b, "%s %q", e.Kind, e.Text)
			if e.Question != "" {
				fmt.Fprintf(&b, " id=%s", e.Question)
			}
			b.WriteString("|")
		}
		return b.String()
	}
	for _, c := range []struct {
		line []byte
		want string
	}{
		{ask("req_1", open), `asking "Which account?" id=req_1|`},
		{ask("req_2", "Which region?"), `activity "refused a second question while one waits"|asking "Which account?" id=req_1|`},
		{[]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","is_error":true}]}}`), `activity "tool result: error"|asking "Which account?" id=req_1|`},
		{[]byte(`not json`), `output "unparsed harness output: not json"|asking "Which account?" id=req_1|`},
		{[]byte(`{"type":"keep_alive"}`), ``},
	} {
		if got := kinds(c.line); got != c.want {
			t.Errorf("Decode(%s) = %s, want %s", c.line, got, c.want)
		}
	}
	if q, ok := conn.Asking(); !ok || q.Text != open || q.Question != "req_1" {
		t.Fatalf("Asking = %+v, %v", q, ok)
	}
	if err := conn.Answer("staging"); err != nil {
		t.Fatal(err)
	}
	if _, ok := conn.Asking(); ok {
		t.Fatal("still asking after the answer")
	}
	if got := kinds([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result"}]}}`)); got != `activity "tool result"|` {
		t.Errorf("after the answer, Decode = %s", got)
	}
}

// TestLiveHarness checks an installed harness against the adapter's contract without a model, a
// login or the network: it reads the version and checks that --help still lists every flag the
// adapter passes. It runs only when LUCY_HARNESS_BIN names the program, as the scheduled harness
// check does; it never runs a session.
func TestLiveHarness(t *testing.T) {
	bin := os.Getenv("LUCY_HARNESS_BIN")
	if bin == "" {
		t.Skip("LUCY_HARNESS_BIN is not set")
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", bin, err)
	}
	v, err := ParseVersion(out)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("harness version %s, pinned %s", v, Pinned)
	help, err := exec.Command(bin, "--help").Output()
	if err != nil {
		t.Fatalf("%s --help: %v", bin, err)
	}
	for _, f := range Flags {
		if !f.Hidden && !bytes.Contains(help, []byte(f.Name)) {
			t.Errorf("claude-code %s no longer lists %s", v, f.Name)
		}
	}
	if !bytes.Contains(help, []byte(`"`+PermissionMode+`"`)) {
		t.Errorf("claude-code %s no longer offers the permission mode %q", v, PermissionMode)
	}
}

// TestSearchToolPath: a Glob or Grep glob that could expand or climb is refused without asking;
// a relative glob is decided by the tool's path, empty when it names none, and an absolute one by
// its static part.
func TestSearchToolPath(t *testing.T) {
	const refused, varRef = "\x00may reach outside", "\x00variable reference"
	for _, c := range []struct {
		tool  string
		input string
		want  string
	}{
		{"Glob", `{"pattern":"/home/someone/.ssh/*"}`, "/home/someone/.ssh"},
		{"Glob", `{"pattern":"/*"}`, "/"},
		{"Glob", `{"pattern":"/work/run-1/src/*.go"}`, "/work/run-1/src"},
		{"Glob", `{"pattern":"/work/run-1/src/main.go"}`, "/work/run-1/src/main.go"},
		{"Glob", `{"pattern":"src/**/*.go"}`, ""},
		{"Glob", `{"pattern":"src/*.go","path":"/work/run-1"}`, "/work/run-1"},
		{"Glob", `{"pattern":"/etc/*","path":"/work/run-1"}`, "/etc"},
		{"Grep", `{"pattern":"/etc/*","path":"/work/run-1/src"}`, "/work/run-1/src"},
		{"Read", `{"file_path":"/work/run-1/a","pattern":"~/x"}`, "/work/run-1/a"},
		{"Glob", `{"pattern":"~/.ssh/*"}`, refused},
		{"Glob", `{"pattern":"{/etc,src}/*"}`, refused},
		{"Glob", `{"pattern":"{..,x}/*"}`, refused},
		{"Glob", `{"pattern":"**/../../x","path":"/work/run-1"}`, refused},
		{"Glob", `{"pattern":"/work/run-1/../etc/*"}`, refused},
		{"Grep", `{"pattern":"secret","glob":"../../*"}`, refused},
		{"Grep", `{"pattern":"secret","glob":"$HOME/*"}`, varRef},
		{"Glob", `{"pattern":"src/${X}/*"}`, varRef},
		{"Glob", `{"pattern":"$(pwd)/*"}`, varRef},
		{"Glob", `{"pattern":"routes/*$*.tsx"}`, ""},
		{"Glob", `{"pattern":"app/routes/posts.$slug.tsx"}`, ""},
		{"Glob", `{"pattern":"app/routes/$postId.tsx"}`, ""},
		{"Grep", `{"pattern":"secret","glob":"$TMPDIR/*"}`, varRef},
		{"Glob", `{"pattern":"$HOME.bak/*"}`, varRef},
		{"Grep", `{"pattern":"secret","glob":"$TMPDIR-1/*"}`, varRef},
		{"Glob", `{"pattern":"routes/$_index.tsx"}`, ""},
	} {
		var asked []string
		permit := func(tool string, _ harness.Access, path string) (bool, string) {
			asked = append(asked, path)
			return false, "no"
		}
		in := &capture{}
		conn, err := Adapter{}.Open(in, harness.Session{Permit: permit})
		if err != nil {
			t.Fatal(err)
		}
		evs, _ := conn.Decode([]byte(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"` +
			c.tool + `","input":` + c.input + `}}`))
		if reason, ok := strings.CutPrefix(c.want, "\x00"); ok {
			if len(asked) != 0 || len(evs) != 1 || !strings.Contains(evs[0].Text, reason) || !strings.Contains(in.buf.String(), `"deny"`) {
				t.Errorf("%s %s: asked %q, %+v, wrote %s", c.tool, c.input, asked, evs, in.buf.String())
			}
			continue
		}
		if len(asked) != 1 || asked[0] != c.want {
			t.Errorf("%s %s: asked about %q, want %q", c.tool, c.input, asked, c.want)
		}
	}
}
