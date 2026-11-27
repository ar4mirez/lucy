package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/harness"
	"github.com/ar4mirez/lucy/internal/harness/claudecode"
	"github.com/ar4mirez/lucy/internal/harness/harnesstest"
	"github.com/ar4mirez/lucy/internal/pathguard"
)

// TestMain doubles as the program the process executor runs: the executor passes LUCY_RUN, and
// LUCY_RUN_GOAL picks what the program does.
func TestMain(m *testing.M) {
	if os.Getenv("LUCY_RUN") != "" {
		os.Exit(helper(os.Getenv("LUCY_RUN_GOAL")))
	}
	os.Exit(m.Run())
}

func helper(goal string) int {
	if rest, ok := strings.CutPrefix(goal, "harness "); ok {
		return fakeHarness(rest)
	}
	if rest, ok := strings.CutPrefix(goal, "harness-on "); ok {
		// a harness that carries on after its input closes, as Claude Code finishes a queued turn
		code := fakeHarness(rest)
		if !slices.Contains(os.Args[1:], "--version") {
			time.Sleep(time.Minute)
		}
		return code
	}
	switch goal {
	case "work":
		wd, _ := os.Getwd()
		fmt.Println("hello from", os.Getenv("LUCY_RUN"), "generation", os.Getenv("LUCY_RUN_GENERATION"))
		fmt.Println("tmpdir-is-cwd", os.Getenv("TMPDIR") == wd)
		fmt.Println("leaked", os.Getenv("LUCY_TEST_TOKEN") != "")
		fmt.Fprintln(os.Stderr, "a warning")
		fmt.Println("lucy:pr=12")
		fmt.Print("no newline")
		return 0
	case "env":
		for _, kv := range os.Environ() {
			fmt.Println("env", kv)
		}
		return 0
	case "fail":
		fmt.Println("giving up")
		return 3
	case "hang":
		time.Sleep(time.Minute)
		return 0
	case "spawn", "detach":
		exe, err := os.Executable()
		if err != nil {
			return 8
		}
		gc := exec.Command(exe)
		gc.Env = append(os.Environ(), "LUCY_RUN_GOAL=hang")
		gc.Stdout = os.Stdout
		if err := gc.Start(); err != nil {
			return 8
		}
		fmt.Println("grandchild", gc.Process.Pid)
		if goal == "detach" {
			fmt.Println("lucy:pr=5")
			return 0
		}
		time.Sleep(time.Minute)
		return 0
	}
	return 9
}

// fakeHarness plays a recorded harness transcript: the goal is "harness <version> <path>", where
// a version of "-" keeps the transcript's own.
func fakeHarness(spec string) int {
	version, path, _ := strings.Cut(spec, " ")
	f, err := os.Open(path)
	if err != nil {
		return 8
	}
	tr, err := harnesstest.Parse(f)
	_ = f.Close()
	if err != nil {
		return 8
	}
	if version != "-" {
		tr.Header.Version = version
	}
	if tr.Header.Project != "" && !filepath.IsAbs(tr.Header.Project) {
		tr.Header.Project = filepath.Join(filepath.Dir(path), tr.Header.Project)
	}
	return harnesstest.Replay(tr, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}

// transcript returns the absolute path of one of the Claude Code adapter's recorded transcripts.
func transcript(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "harness", "claudecode", "testdata", claudecode.Pinned, name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type procFixture struct {
	root, home string
	ex         *ProcessExecutor
}

func newProc(t *testing.T) procFixture {
	t.Helper()
	base := t.TempDir()
	pf := procFixture{root: filepath.Join(base, "runner"), home: filepath.Join(base, "home")}
	if err := os.Mkdir(pf.home, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pf.ex = &ProcessExecutor{Root: pf.root, Home: pf.home, Command: []string{exe}, StopGrace: time.Second}
	return pf
}

func procLease(goal string) Lease {
	return Lease{Task: Task{Run: "f1#2", Org: "acme", Project: "web", Flight: "f1", Generation: 2, Goal: goal, Base: "main"},
		Runner: "r1", Fence: 7}
}

// drain polls an execution until it reports a result.
func drain(t *testing.T, x Execution) ([]string, Result) {
	t.Helper()
	var lines []string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		u := x.Poll()
		for _, e := range u.Events {
			lines = append(lines, e.Text)
		}
		if u.Result != nil {
			return lines, *u.Result
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the program did not finish")
	return nil, Result{}
}

func TestProcessExecutorRunsAndCleansUp(t *testing.T) {
	t.Setenv("LUCY_TEST_TOKEN", "do-not-pass-me")
	pf := newProc(t)
	x, err := pf.ex.Start(context.Background(), procLease("work"))
	if err != nil {
		t.Fatal(err)
	}
	lines, res := drain(t, x)
	if res.Outcome != OutcomeDone || res.PR != 12 {
		t.Fatalf("result %+v", res)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"hello from f1#2 generation 2", "tmpdir-is-cwd true", "leaked false", "a warning", "no newline"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "lucy:pr=") {
		t.Error("the pull request directive was streamed as output")
	}
	entries, err := os.ReadDir(pf.root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the run's scratch directory was kept: %v, %v", entries, err)
	}
	if st, err := os.Stat(pf.root); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("root: %v, %v", st, err)
	}
}

func TestProcessExecutorFailsAndStops(t *testing.T) {
	pf := newProc(t)
	x, err := pf.ex.Start(context.Background(), procLease("fail"))
	if err != nil {
		t.Fatal(err)
	}
	if _, res := drain(t, x); res.Outcome != OutcomeFailed || !strings.Contains(res.Reason, "exit status 3") {
		t.Fatalf("result %+v", res)
	}

	x, err = pf.ex.Start(context.Background(), procLease("hang"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	x.Stop()
	if _, res := drain(t, x); res.Outcome != OutcomeStopped {
		t.Fatalf("result %+v", res)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("stop took too long")
	}
}

func TestProcessExecutorRefusesBadRoots(t *testing.T) {
	pf := newProc(t)
	for name, root := range map[string]string{
		"filesystem root": "/",
		"home":            pf.home,
		"home's parent":   filepath.Dir(pf.home),
		"relative":        "runner",
		"unclean":         pf.root + "/../runner",
	} {
		ex := *pf.ex
		ex.Root = root
		if _, err := ex.Start(context.Background(), procLease("work")); err == nil {
			t.Errorf("%s: started under root %q", name, root)
		}
	}
	ex := *pf.ex
	ex.Command = nil
	if _, err := ex.Start(context.Background(), procLease("work")); err == nil {
		t.Error("started with no command")
	}
}

// removeScratch deletes only inside its root: never the root, the home directory, "/" or a
// path that leaves the root through "..", a symlink or another directory.
func TestRemoveScratchStaysInsideRoot(t *testing.T) {
	pf := newProc(t)
	if err := os.MkdirAll(pf.root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(pf.root), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(pf.root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	guard, err := pathguard.New(pf.home, pathguard.Root{Kind: pathguard.Scratch, Path: pf.root})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", pf.home, pf.root, outside, link, pf.root + "/../outside", "relative"} {
		if err := removeScratch(guard, p); !errors.Is(err, pathguard.ErrRefused) {
			t.Errorf("removeScratch(%q) = %v, want refused", p, err)
		}
	}
	for _, p := range []string{pf.home, pf.root, outside} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted: %v", p, err)
		}
	}
	inside := filepath.Join(guard.Roots()[0].Path, "run-1") // the resolved root, as Start uses
	if err := os.MkdirAll(filepath.Join(inside, "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeScratch(guard, inside); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inside); !os.IsNotExist(err) {
		t.Fatalf("inside the root was not deleted: %v", err)
	}
}

// TestHarnessExecutorSpeaksTheProtocol runs recorded Claude Code sessions through the executor:
// the protocol on the program's output becomes events and the session's result, not lines, and
// the run's directory is still deleted afterwards.
func TestHarnessExecutorSpeaksTheProtocol(t *testing.T) {
	for _, c := range []struct {
		name   string
		goal   string // the fake harness's mode: "harness" plays the transcript, "harness-on" then carries on
		want   Result
		events []string
	}{
		{"done", "harness", Result{Outcome: OutcomeDone, PR: 42}, []string{"session started: claude-code " + claudecode.Pinned + ", model claude-opus-5-5",
			"I will fix the failing test.", "tool: Bash", "tool result", "Opened the pull request.\nlucy:pr=42", "session ended: success after 3 turns"}},
		{"refused", "harness", Result{Outcome: OutcomeDone}, nil},
		{"error", "harness", Result{Outcome: OutcomeFailed, Reason: "error_max_turns: Reached maximum number of turns (50)"}, nil},
		// the drifted session is killed, not left to carry out the goal: drain gives up long
		// before the harness would stop on its own
		{"drift", "harness-on", Result{Outcome: OutcomeFailed, Reason: "claude-code reported version 2.1.400, lucy supports " + claudecode.Pinned}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			pf := newProc(t)
			pf.ex.Harness = claudecode.Adapter{}
			x, err := pf.ex.Start(context.Background(), procLease(c.goal+" - "+transcript(t, c.name)))
			if err != nil {
				t.Fatal(err)
			}
			lines, res := drain(t, x)
			if res != c.want {
				t.Fatalf("result %+v, want %+v; events %q", res, c.want, lines)
			}
			if c.events != nil && strings.Join(lines, "|") != strings.Join(c.events, "|") {
				t.Errorf("events %q, want %q", lines, c.events)
			}
			if left, _ := os.ReadDir(pf.root); len(left) != 0 {
				t.Errorf("scratch left behind: %v", left)
			}
		})
	}
}

// TestHarnessExecutorHoldsAnUnpinnedHarness: a harness that reports any version but the pinned one
// never starts a session (LUCY-RUN-003).
func TestHarnessExecutorHoldsAnUnpinnedHarness(t *testing.T) {
	pf := newProc(t)
	pf.ex.Harness = claudecode.Adapter{}
	_, err := pf.ex.Start(context.Background(), procLease("harness 2.1.400 "+transcript(t, "done")))
	if !errors.Is(err, harness.ErrUnpinned) {
		t.Fatalf("Start = %v, want ErrUnpinned", err)
	}
	if left, _ := os.ReadDir(pf.root); len(left) != 0 {
		t.Errorf("scratch left behind: %v", left)
	}
}

// TestPlainProcessTakesNoAnswer: a plain program has no input channel at all.
func TestPlainProcessTakesNoAnswer(t *testing.T) {
	pf := newProc(t)
	x, err := pf.ex.Start(context.Background(), procLease("work"))
	if err != nil {
		t.Fatal(err)
	}
	if err := x.(Answerer).Answer("yes"); !errors.Is(err, ErrNoInput) {
		t.Fatalf("Answer = %v, want ErrNoInput", err)
	}
	drain(t, x)
}

// TestHarnessExecutorAnswers: the session's question is streamed as asking, and the answer given
// to the execution goes back to the harness on its input.
func TestHarnessExecutorAnswers(t *testing.T) {
	pf := newProc(t)
	pf.ex.Harness = claudecode.Adapter{}
	x, err := pf.ex.Start(context.Background(), procLease("harness - "+transcript(t, "ask")))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for asked := false; !asked; {
		if time.Now().After(deadline) {
			t.Fatal("the session never asked")
		}
		for _, e := range x.Poll().Events {
			asked = asked || e.Kind == KindAsking
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := x.(Answerer).Answer("use the staging account"); err != nil {
		t.Fatal(err)
	}
	if _, res := drain(t, x); res != (Result{Outcome: OutcomeDone, PR: 7}) {
		t.Fatalf("result %+v", res)
	}
}

// TestHarnessExecutorKeepsTheQuestionOpen: while the session's question waits, a second question
// the adapter refuses and a line on the harness's standard error are streamed, and the last event
// is still the open question, so the Tower keeps the flight asking and takes the owner's answer.
func TestHarnessExecutorKeepsTheQuestionOpen(t *testing.T) {
	const stderr = "warning: the network is slow"
	path := askWhileWaiting(t,
		`{"out":{"type":"control_request","request_id":"req_2","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"Which region?"}]}}}}`,
		`{"in":{"type":"control_response","response":{"subtype":"success","request_id":"req_2","response":{"behavior":"deny"}}}}`,
		`{"stderr":"`+stderr+`"}`)
	pf := newProc(t)
	pf.ex.Harness = claudecode.Adapter{}
	x, err := pf.ex.Start(context.Background(), procLease("harness - "+path))
	if err != nil {
		t.Fatal(err)
	}
	const question = "Which account should the migration run against? (staging | production)"
	var last Event
	refused, warned := false, false
	deadline := time.Now().Add(20 * time.Second)
	for !refused || !warned {
		if time.Now().After(deadline) {
			t.Fatalf("refused %v, warned %v", refused, warned)
		}
		for _, e := range x.Poll().Events {
			refused = refused || e.Text == "refused a second question while one waits"
			warned = warned || e.Text == stderr
			last = e
		}
		time.Sleep(5 * time.Millisecond)
	}
	if last.Kind != KindAsking || last.Text != question {
		t.Fatalf("the last event is %+v, want the open question", last)
	}
	if err := x.(Answerer).Answer("use the staging account"); err != nil {
		t.Fatal(err)
	}
	if _, res := drain(t, x); res != (Result{Outcome: OutcomeDone, PR: 7}) {
		t.Fatalf("result %+v", res)
	}
}

// askWhileWaiting writes the ask transcript with extra lines played while its question waits.
func askWhileWaiting(t *testing.T, extra ...string) string {
	t.Helper()
	b, err := os.ReadFile(transcript(t, "ask"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		lines = append(lines, l)
		if strings.Contains(l, `"can_use_tool"`) { // after the question's permission prompt
			lines = append(lines, extra...)
		}
	}
	path := filepath.Join(t.TempDir(), "ask-busy.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestHarnessExecutorTagsEveryRepeat: while a question waits, a stderr line and a protocol message
// the adapter does not know are each followed by the question again, and every repeat carries
// the question's id, so the broker knows it as the question the owner answers once.
func TestHarnessExecutorTagsEveryRepeat(t *testing.T) {
	const stderr = "warning: the network is slow"
	const unknown = "unrecognised harness message: mystery"
	path := askWhileWaiting(t, `{"stderr":"`+stderr+`"}`, `{"out":{"type":"mystery"}}`)
	pf := newProc(t)
	pf.ex.Harness = claudecode.Adapter{}
	x, err := pf.ex.Start(context.Background(), procLease("harness - "+path))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	asks := 0
	deadline := time.Now().Add(20 * time.Second)
	for !slices.Contains(texts, stderr) || !slices.Contains(texts, unknown) {
		if time.Now().After(deadline) {
			t.Fatalf("events %q", texts)
		}
		for _, e := range x.Poll().Events {
			texts = append(texts, e.Text)
			if e.Kind == KindAsking {
				asks++
				if e.Question != "req_1" {
					t.Errorf("the question is streamed as %q, want id req_1", e.Question)
				}
			} else if e.Question != "" {
				t.Errorf("%s event %q carries a question id", e.Kind, e.Text)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if asks < 2 {
		t.Errorf("the question was streamed %d times, want it repeated after the other events: %q", asks, texts)
	}
	if err := x.(Answerer).Answer("use the staging account"); err != nil {
		t.Fatal(err)
	}
	if _, res := drain(t, x); res != (Result{Outcome: OutcomeDone, PR: 7}) {
		t.Fatalf("result %+v", res)
	}
}

// projectSettings is the Claude Code adapter with project settings loaded, no lucy-owned settings
// and no strict MCP config. It is the control that shows the test below can see a
// project's settings take effect.
type projectSettings struct{ claudecode.Adapter }

func (a projectSettings) Args(s harness.Session) []string {
	var out []string
	args := a.Adapter.Args(s)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--setting-sources":
			out = append(out, args[i], "project")
			i++
		case "--settings", "--mcp-config":
			i++
		case "--strict-mcp-config":
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// TestHarnessIgnoresProjectSettings runs a session in a fixture repository whose committed
// .claude/settings.json pre-allows Bash, adds a hook and enables its .mcp.json servers. Under the
// adapter's arguments none takes effect: Bash is still prompted, so lucy refuses it, the hook never
// runs and no MCP server loads. The control, with project settings loaded and no strict MCP config,
// shows all three taking effect.
func TestHarnessIgnoresProjectSettings(t *testing.T) {
	run := func(a harness.Adapter) []string {
		pf := newProc(t)
		pf.ex.Harness = a
		x, err := pf.ex.Start(context.Background(), procLease("harness - "+transcript(t, "project")))
		if err != nil {
			t.Fatal(err)
		}
		lines, res := drain(t, x)
		if res.Outcome != OutcomeDone {
			t.Fatalf("result %+v, events %q", res, lines)
		}
		return lines
	}
	got := strings.Join(run(claudecode.Adapter{}), "|")
	if !strings.Contains(got, "refused tool: Bash") || strings.Contains(got, "project hook ran") ||
		strings.Contains(got, "project MCP server loaded") {
		t.Errorf("the repository's settings took effect: %s", got)
	}
	control := strings.Join(run(projectSettings{}), "|")
	if strings.Contains(control, "refused tool: Bash") || !strings.Contains(control, "project hook ran") ||
		!strings.Contains(control, "project MCP server loaded: chat") {
		t.Errorf("the control did not load the repository's settings, so the test proves nothing: %s", control)
	}
}

// recordArgs is the Claude Code adapter, keeping the launch arguments it gave.
type recordArgs struct {
	claudecode.Adapter
	got *[]string
}

func (a recordArgs) Args(s harness.Session) []string {
	*a.got = a.Adapter.Args(s)
	return *a.got
}

// TestHarnessGetsRepositoryInstructions seeds the run's directory with a CLAUDE.md that imports
// AGENTS.md and checks the session is launched with the AGENTS.md text in its system prompt.
func TestHarnessGetsRepositoryInstructions(t *testing.T) {
	pf := newProc(t)
	var argv []string
	pf.ex.Harness = recordArgs{got: &argv}
	pf.ex.prepare = func(dir string) error {
		if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("@AGENTS.md\n"), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("run-dir-agents-9c1e\n"), 0o600)
	}
	x, err := pf.ex.Start(context.Background(), procLease("harness - "+transcript(t, "done")))
	if err != nil {
		t.Fatal(err)
	}
	if lines, res := drain(t, x); res.Outcome != OutcomeDone {
		t.Fatalf("result %+v, events %q", res, lines)
	}
	i := slices.Index(argv, "--append-system-prompt")
	var framed string
	if i >= 0 && i+1 < len(argv) {
		_, rest, _ := strings.Cut(argv[i+1], "<repository-instructions>\n")
		framed, _, _ = strings.Cut(rest, "\n</repository-instructions>")
	}
	if !strings.Contains(framed, "run-dir-agents-9c1e") {
		t.Errorf("the launch arguments lack the run directory's AGENTS.md: %q", argv)
	}
}

// TestHarnessExecutorStreamsHooks: the harness's hook callbacks arrive as typed hook events,
// each naming its hook, and the session still ends with its result.
func TestHarnessExecutorStreamsHooks(t *testing.T) {
	pf := newProc(t)
	pf.ex.Harness = claudecode.Adapter{}
	x, err := pf.ex.Start(context.Background(), procLease("harness - "+transcript(t, "hooks")))
	if err != nil {
		t.Fatal(err)
	}
	var hooks []string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		u := x.Poll()
		for _, e := range u.Events {
			if e.Kind == KindHook {
				hooks = append(hooks, e.Hook)
			}
		}
		if u.Result != nil {
			if *u.Result != (Result{Outcome: OutcomeDone, PR: 3}) {
				t.Fatalf("result %+v", *u.Result)
			}
			if strings.Join(hooks, " ") != "SessionStart PreToolUse Notification Stop" {
				t.Fatalf("hooks %v", hooks)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the session did not finish")
}

// TestHarnessExecutorKeepsTheQuestionAfterAHook: a Notification hook that fires while the
// session's question waits is streamed as a hook event, the last event is still the open question
// with its id, and the owner's answer then reaches the harness.
func TestHarnessExecutorKeepsTheQuestionAfterAHook(t *testing.T) {
	path := askWhileWaiting(t,
		`{"out":{"type":"control_request","request_id":"req_2","request":{"subtype":"hook_callback","callback_id":"lucy_hook_Notification","input":{"session_id":"5f0c7d1e-0000-4000-8000-000000000001","hook_event_name":"Notification","message":"Claude needs your input"}}}}`,
		`{"in":{"type":"control_response","response":{"subtype":"success","request_id":"req_2","response":{}}}}`)
	pf := newProc(t)
	pf.ex.Harness = claudecode.Adapter{}
	x, err := pf.ex.Start(context.Background(), procLease("harness - "+path))
	if err != nil {
		t.Fatal(err)
	}
	var last Event
	hooked := false
	deadline := time.Now().Add(20 * time.Second)
	for !hooked {
		if time.Now().After(deadline) {
			t.Fatal("the Notification hook was never streamed")
		}
		for _, e := range x.Poll().Events {
			hooked = hooked || e.Kind == KindHook && e.Hook == "Notification"
			last = e
		}
		time.Sleep(5 * time.Millisecond)
	}
	if last.Kind != KindAsking || last.Question != "req_1" {
		t.Fatalf("the last event is %+v, want the open question req_1", last)
	}
	if err := x.(Answerer).Answer("use the staging account"); err != nil {
		t.Fatal(err)
	}
	if _, res := drain(t, x); res != (Result{Outcome: OutcomeDone, PR: 7}) {
		t.Fatalf("result %+v", res)
	}
}

// TestNoCredentialReachesThePilot checks LUCY-CRED-005 at the runner: whatever credentials the
// runner's own environment holds, a Pilot's environment is exactly PATH, HOME, TMPDIR and the
// run's LUCY_RUN_* variables, so none of them reaches the work. The values are made up by the test.
func TestNoCredentialReachesThePilot(t *testing.T) {
	secrets := map[string]string{}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN",
		"OPENAI_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS",
		"NPM_TOKEN", "SSH_AUTH_SOCK", "GIT_ASKPASS", "GIT_CONFIG_GLOBAL", "DOCKER_AUTH_CONFIG", "LUCY_TOKEN", "HTTPS_PROXY"} {
		v := "made-up-" + strings.ToLower(name) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		secrets[name] = v
		t.Setenv(name, v)
	}
	pf := newProc(t)
	x, err := pf.ex.Start(context.Background(), procLease("env"))
	if err != nil {
		t.Fatal(err)
	}
	lines, res := drain(t, x)
	if res.Outcome != OutcomeDone {
		t.Fatalf("result %+v", res)
	}
	allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true}
	var names []string
	for _, l := range lines {
		kv, ok := strings.CutPrefix(l, "env ")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
		if !allowed[name] && !strings.HasPrefix(name, "LUCY_RUN") {
			t.Errorf("the Pilot's environment holds %s", name)
		}
		if strings.HasPrefix(name, "LUCY_RUN") && name != "LUCY_RUN" && !strings.HasPrefix(name, "LUCY_RUN_") {
			t.Errorf("unexpected variable %s", name)
		}
	}
	if len(names) == 0 {
		t.Fatal("the helper printed no environment")
	}
	out := strings.Join(lines, "\n")
	for name, v := range secrets {
		if strings.Contains(out, v) {
			t.Errorf("%s's value reached the Pilot", name)
		}
	}
}
