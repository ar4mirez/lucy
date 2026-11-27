package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/harness"
	"github.com/ar4mirez/lucy/internal/pathguard"
	"github.com/ar4mirez/lucy/internal/policy"
)

// ProcessExecutor runs one program per leased run, directly on this machine. With a Harness it
// drives an agent harness over the harness's structured protocol (LUCY-RUN-002); without one it
// runs a plain program and streams its output. Each run gets a fresh scratch directory under Root as its working directory and TMPDIR, and an
// environment of only PATH, HOME, TMPDIR and the run's LUCY_RUN_* variables, so nothing else in
// the runner's environment, such as a token, reaches the work. Every line the program writes to
// stdout or stderr is streamed as an output event; a line "lucy:pr=<n>" reports the pull request
// instead. Exit status 0 is done; any other is failed.
//
// When the program has exited, its scratch directory is deleted, but only after the path guard
// has resolved it to a path strictly inside Root: never "/", the home directory or anything
// outside Root.
type ProcessExecutor struct {
	Root    string   // absolute; one scratch directory per run is made inside it; created 0700
	Command []string // the program and its arguments; required
	Home    string   // the user's home directory, which no delete may touch; os.UserHomeDir when empty
	Path    string   // the work's PATH; the runner's own when empty
	Log     *slog.Logger
	// StopGrace is how long a stopped program's output may stay open after it is killed;
	// 2 s when zero.
	StopGrace time.Duration
	// Harness, when set, is the structured protocol the program speaks. Command is then the
	// harness program; the adapter's arguments follow Command's own and its variables join the
	// environment. Before each run the program must report the adapter's pinned version, or the
	// run fails without starting (LUCY-RUN-003). Its standard output is decoded as the protocol,
	// not streamed as lines; its standard error is streamed as output. The session's end is what
	// the protocol reports, not the exit status, and the work takes the owner's answers, relayed
	// by the Tower, on its input (LUCY-RUN-013).
	Harness harness.Adapter
	// Permitter, with a Harness, decides the session's prompted file tools through the policy
	// engine; without one every prompted tool is refused. A path outside the run's directory is
	// refused before the engine is asked.
	Permitter Permitter

	prepare func(dir string) error // tests: seeds a run's directory before the harness reads it
}

// Bounds on what one process run buffers between polls.
const (
	maxBufferedLines = 4096
	maxLineBytes     = 64 << 10 // a longer line is cut into pieces of this size
	maxProtocolLine  = 4 << 20  // a longer protocol message is dropped whole
	versionTimeout   = 10 * time.Second
)

// prDirective is the line a program writes to report its pull request.
const prDirective = harness.PRDirective

// Start implements Executor.
func (p *ProcessExecutor) Start(_ context.Context, l Lease) (Execution, error) {
	if len(p.Command) == 0 || p.Command[0] == "" {
		return nil, errors.New("runner: the process executor has no command")
	}
	if !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root {
		return nil, fmt.Errorf("runner: the process executor's root %q is not absolute and clean", p.Root)
	}
	home := p.Home
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return nil, err
	}
	guard, err := pathguard.New(home, pathguard.Root{Kind: pathguard.Scratch, Path: p.Root})
	if err != nil {
		return nil, err
	}
	// Make the scratch directory under the guard's symlink-resolved root (on macOS /var is a
	// symlink to /private/var), so the path the program sees as its cwd and TMPDIR and the path
	// the guard checks before the delete are the same spelling.
	dir, err := os.MkdirTemp(guard.Roots()[0].Path, "run-")
	if err != nil {
		return nil, err
	}
	path := p.Path
	if path == "" {
		path = os.Getenv("PATH")
	}
	log := p.Log
	if log == nil {
		log = slog.Default()
	}
	pe := &processExecution{guard: guard, dir: dir, log: log}
	if p.prepare != nil {
		if err := p.prepare(dir); err != nil {
			pe.cleanup()
			return nil, err
		}
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + home, // TODO(LUCY-RUN-006): a synthetic HOME per org (m1 step 6)
		"TMPDIR=" + dir,
		"LUCY_RUN=" + l.Run,
		"LUCY_RUN_ORG=" + l.Org,
		"LUCY_RUN_PROJECT=" + l.Project,
		"LUCY_RUN_FLIGHT=" + l.Flight,
		"LUCY_RUN_GENERATION=" + strconv.Itoa(l.Generation),
		"LUCY_RUN_GOAL=" + l.Goal,
		"LUCY_RUN_BASE=" + l.Base,
	}
	args := slices.Clip(p.Command[1:])
	session := harness.Session{Goal: l.Goal}
	if p.Permitter != nil {
		var protected []string
		if p.Harness != nil {
			protected = p.Harness.Protected()
		}
		session.Permit = permitFor(dir, protected, l, p.Permitter, log)
	}
	if p.Harness != nil {
		env = append(env, p.Harness.Env()...)
		if err := checkHarness(p.Harness, p.Command, dir, env); err != nil {
			pe.cleanup()
			return nil, err
		}
		// TODO(LUCY-RUN-006): the run's directory is an empty scratch dir until slice 6 checks the
		// repository out into it; slice 6 must point the reader at that checkout.
		if session.Instructions, err = harness.Instructions(dir); err != nil {
			pe.cleanup()
			return nil, err
		}
		args = append(args, p.Harness.Args(session)...)
	}
	cmd := exec.Command(p.Command[0], args...) // #nosec G204 -- the runner's own configured program, never text from a task
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = pe
	cmd.Stderr = pe
	if p.Harness != nil {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			pe.cleanup()
			return nil, err
		}
		if pe.conn, err = p.Harness.Open(stdin, session); err != nil {
			_ = stdin.Close()
			pe.cleanup()
			return nil, err
		}
		pe.proto = &protocolLines{pe: pe}
		cmd.Stdout = pe.proto
	}
	ownProcessGroup(cmd)
	cmd.WaitDelay = p.StopGrace
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = 2 * time.Second
	}
	pe.cmd = cmd // before Start: the protocol's reader may stop the program as soon as it runs
	if err := cmd.Start(); err != nil {
		pe.cleanup()
		return nil, err
	}
	go pe.wait()
	return pe, nil
}

type processExecution struct {
	guard *pathguard.Guard
	dir   string
	log   *slog.Logger
	cmd   *exec.Cmd
	conn  harness.Conn   // with a harness: the session's protocol
	proto *protocolLines // with a harness: its standard output, split into messages

	mu      sync.Mutex
	partial []byte
	lines   []Event
	dropped int
	pr      int
	exit    *harness.Exit // with a harness: the session's end, as the protocol reported it
	asked   bool          // the last event buffered was the open question
	stopped bool
	result  *Result
}

// checkHarness runs the harness program for its version and refuses any but the pinned one.
func checkHarness(a harness.Adapter, command []string, dir string, env []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], append(slices.Clip(command[1:]), a.VersionArgs()...)...) // #nosec G204 -- as in Start
	cmd.Dir, cmd.Env = dir, env
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("runner: %s did not report its version: %w", a.Name(), err)
	}
	_, err = a.CheckVersion(out)
	return err
}

// protocolLines splits a harness's standard output into protocol messages, one per line. It is
// written from one goroutine, and flushed once the program has exited.
type protocolLines struct {
	pe       *processExecution
	partial  []byte
	skipping bool // the rest of an overlong message is being dropped
}

func (w *protocolLines) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if !w.skipping {
				w.partial = append(w.partial, b...)
				if len(w.partial) > maxProtocolLine {
					w.partial, w.skipping = w.partial[:0], true
					w.pe.message(nil, fmt.Sprintf("lucy: dropped a harness message over %d MiB", maxProtocolLine>>20))
				}
			}
			return n, nil
		}
		if !w.skipping {
			w.partial = append(w.partial, b[:i]...)
			w.pe.harnessLine(w.partial)
		}
		w.partial, w.skipping = w.partial[:0], false
		b = b[i+1:]
	}
	return n, nil
}

func (w *protocolLines) flush() {
	if len(w.partial) > 0 && !w.skipping {
		w.pe.harnessLine(w.partial)
	}
	w.partial = nil
}

// harnessLine decodes one protocol message. Decoding may answer the harness on its input, so it
// runs without the lock. A session the protocol ends as failed, such as one that reports a
// version other than the pin, is killed with everything it started, so it carries out nothing
// more of the goal.
func (pe *processExecution) harnessLine(b []byte) {
	evs, exit := pe.conn.Decode(b)
	pe.message(exit, "")
	if exit != nil && !exit.OK {
		if err := killProcessGroup(pe.cmd); err != nil {
			pe.log.Warn("runner could not kill a failed harness session", "dir", pe.dir, "err", err)
		}
	}
	pe.mu.Lock()
	defer pe.mu.Unlock()
	for _, e := range evs {
		kind := EventKind(e.Kind)
		if !kind.Valid() {
			kind = KindActivity
		}
		pe.push(harnessEvent(kind, e))
	}
}

// message records a session's end, and a note from lucy itself as an activity event.
func (pe *processExecution) message(exit *harness.Exit, note string) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	if exit != nil && pe.exit == nil {
		pe.exit = exit
	}
	if note != "" {
		pe.push(Event{Kind: KindActivity, Text: note})
		pe.keepAsking()
	}
}

// push buffers one event; pe.mu is held.
func (pe *processExecution) push(e Event) {
	if len(pe.lines) >= maxBufferedLines {
		pe.dropped++
		return
	}
	pe.lines = append(pe.lines, e)
	pe.asked = e.Kind == KindAsking
}

// keepAsking buffers the session's open question again after events of lucy's own or the
// harness's standard error, so the last event streamed stays the question; pe.mu is held.
func (pe *processExecution) keepAsking() {
	if pe.conn == nil || pe.asked {
		return
	}
	if q, ok := pe.conn.Asking(); ok {
		pe.push(harnessEvent(KindAsking, q))
	}
}

// harnessEvent is one of the harness's events as kind, capped.
func harnessEvent(kind EventKind, e harness.Event) Event {
	return Event{Kind: kind, Text: capText(e.Text), Question: e.Question, Hook: e.Hook}
}

// Answer implements Answerer: it gives the harness the owner's answer to its question.
func (pe *processExecution) Answer(text string) error {
	if pe.conn == nil {
		return ErrNoInput
	}
	return pe.conn.Answer(text)
}

// Write implements io.Writer for the program's stdout and stderr.
func (pe *processExecution) Write(b []byte) (int, error) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	defer pe.keepAsking()
	pe.partial = append(pe.partial, b...)
	for {
		i := bytes.IndexByte(pe.partial, '\n')
		if i < 0 {
			if len(pe.partial) >= maxLineBytes {
				pe.line(string(pe.partial[:maxLineBytes]))
				pe.partial = pe.partial[maxLineBytes:]
				continue
			}
			return len(b), nil
		}
		pe.line(string(pe.partial[:i]))
		pe.partial = pe.partial[i+1:]
	}
}

// line takes one line of output; pe.mu is held.
func (pe *processExecution) line(s string) {
	s = strings.TrimSuffix(s, "\r")
	if v, ok := strings.CutPrefix(s, prDirective); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			pe.pr = n
			return
		}
	}
	pe.push(Event{Kind: KindOutput, Text: capText(s)})
}

func (pe *processExecution) wait() {
	err := pe.cmd.Wait()
	if kerr := killProcessGroup(pe.cmd); kerr != nil {
		pe.log.Warn("runner could not kill a run's leftover processes", "dir", pe.dir, "err", kerr)
	}
	if errors.Is(err, exec.ErrWaitDelay) && pe.cmd.ProcessState.Success() {
		err = nil
	}
	if pe.proto != nil {
		pe.proto.flush()
	}
	pe.mu.Lock()
	if len(pe.partial) > 0 {
		pe.line(string(pe.partial))
		pe.partial = nil
	}
	res := &Result{Outcome: OutcomeDone, PR: pe.pr}
	switch {
	case pe.stopped:
		res = &Result{Outcome: OutcomeStopped, Reason: "stopped"}
	case pe.conn != nil && pe.exit != nil && pe.exit.OK:
		res = &Result{Outcome: OutcomeDone, PR: pe.exit.PR}
	case pe.conn != nil && pe.exit != nil:
		res = &Result{Outcome: OutcomeFailed, Reason: capText(pe.exit.Reason)}
	case pe.conn != nil:
		reason := "the harness ended without reporting a result"
		if err != nil {
			reason += ": " + err.Error()
		}
		res = &Result{Outcome: OutcomeFailed, Reason: capText(reason)}
	case err != nil:
		res = &Result{Outcome: OutcomeFailed, Reason: capText(err.Error())}
	}
	pe.mu.Unlock()
	pe.cleanup()
	pe.mu.Lock()
	pe.result = res
	pe.mu.Unlock()
}

// cleanup deletes the run's scratch directory through the path guard.
func (pe *processExecution) cleanup() {
	if err := removeScratch(pe.guard, pe.dir); err != nil {
		pe.log.Warn("runner kept a run's scratch directory", "dir", pe.dir, "err", err)
	}
}

// removeScratch deletes dir, which must resolve to a path strictly inside one of guard's roots.
// It deletes the resolved path and does not follow symlinks inside it.
func removeScratch(guard *pathguard.Guard, dir string) error {
	resolved, _, err := guard.Resolve(dir)
	if err != nil {
		return err
	}
	return os.RemoveAll(resolved)
}

// Poll implements Execution.
func (pe *processExecution) Poll() Update {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	u := Update{Events: pe.lines}
	pe.lines = nil
	if pe.dropped > 0 && len(u.Events) < maxBufferedLines {
		u.Events = append(u.Events, Event{Kind: KindOutput, Text: fmt.Sprintf("lucy: %d lines of output dropped", pe.dropped)})
		pe.dropped = 0
		pe.asked = false
		if pe.conn != nil {
			if q, ok := pe.conn.Asking(); ok {
				u.Events = append(u.Events, harnessEvent(KindAsking, q))
				pe.asked = true
			}
		}
	}
	if pe.result != nil && len(pe.lines) == 0 && pe.dropped == 0 {
		u.Result = pe.result
	}
	return u
}

// Stop implements Execution: it kills the program and every process it started.
func (pe *processExecution) Stop() {
	pe.mu.Lock()
	if pe.result != nil || pe.stopped {
		pe.mu.Unlock()
		return
	}
	pe.stopped = true
	pe.mu.Unlock()
	if pe.cmd != nil && pe.cmd.Process != nil {
		_ = killProcessGroup(pe.cmd) // an already exited program is fine
	}
}

// permitFor returns the session's Permit: the path the tool names, or dir itself when it names
// none, must resolve inside dir, the run's directory, a write must not reach a protected element
// inside it, and the policy engine must clear the tool under the run's lease with that path.
// Anything that goes wrong refuses the tool.
func permitFor(dir string, protected []string, l Lease, pm Permitter, log *slog.Logger) harness.Permit {
	return func(tool string, access harness.Access, path string) (bool, string) {
		if access == harness.Exec || access == harness.Fetch {
			// A command or a URL is the policy engine's to decide: no path rule applies to it.
			if path == "" || len(path) > MaxPermitResource {
				return false, fmt.Sprintf("the %s names nothing to decide, or more than %d bytes", access, MaxPermitResource)
			}
			return ask(pm, l, log, tool, access, path)
		}
		if harness.VarRef(path) {
			return false, "the path contains a variable reference"
		}
		if path == "" {
			path = dir
		}
		path, ok := insideDir(dir, path)
		if !ok {
			return false, "the path is outside the run's directory"
		}
		if access == harness.Write && holdsAny(strings.TrimPrefix(path, dir), protected) {
			return false, "the path is the harness's own configuration"
		}
		return ask(pm, l, log, tool, access, path)
	}
}

// ask has the policy engine decide one prompted tool under the run's lease. Only a recorded
// cleared decision lets it run.
func ask(pm Permitter, l Lease, log *slog.Logger, tool string, access harness.Access, resource string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	perm, err := pm.Permit(ctx, l.Runner, l.Run, l.Fence, PermitRequest{Tool: tool, Kind: policy.Kind(access), Resource: resource})
	if err != nil {
		log.Warn("runner could not ask the policy engine about a tool", "run", l.Run, "tool", tool, "err", err)
		return false, "lucy could not ask its policy engine"
	}
	if perm.Outcome != policy.Cleared {
		return false, perm.Reason
	}
	return true, ""
}

// holdsAny reports whether an element of path equals one of names, ignoring case.
func holdsAny(path string, names []string) bool {
	for _, e := range strings.Split(path, string(filepath.Separator)) {
		for _, n := range names {
			if strings.EqualFold(e, n) {
				return true
			}
		}
	}
	return false
}

// insideDir resolves path, relative to dir when it is not absolute, through any symlinks in its
// existing part, and reports whether it is dir or inside it. A path that starts with "~" may be
// expanded to the home directory, so it is refused.
func insideDir(dir, path string) (string, bool) {
	if strings.HasPrefix(path, "~") {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	path = filepath.Clean(path)
	existing, rest := path, ""
	for {
		if r, err := filepath.EvalSymlinks(existing); err == nil {
			path = filepath.Join(r, rest)
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", false
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	if path != dir && !strings.HasPrefix(path, dir+string(filepath.Separator)) {
		return "", false
	}
	return path, true
}
