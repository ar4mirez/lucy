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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/pathguard"
)

// ProcessExecutor runs one program per leased run, directly on this machine: the local runner's
// executor until the structured harness adapter lands (TODO(LUCY-RUN-002), m1 step 2). Each run
// gets a fresh scratch directory under Root as its working directory and TMPDIR, and an
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
}

// Bounds on what one process run buffers between polls.
const (
	maxBufferedLines = 4096
	maxLineBytes     = 64 << 10 // a longer line is cut into pieces of this size
)

// prDirective is the line a program writes to report its pull request.
const prDirective = "lucy:pr="

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
	dir, err := os.MkdirTemp(p.Root, "run-")
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
	cmd := exec.Command(p.Command[0], p.Command[1:]...) // #nosec G204 -- the runner's own configured program, never text from a task
	cmd.Dir = dir
	cmd.Env = []string{
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
	cmd.Stdout = pe
	cmd.Stderr = pe
	ownProcessGroup(cmd)
	cmd.WaitDelay = p.StopGrace
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = 2 * time.Second
	}
	if err := cmd.Start(); err != nil {
		pe.cleanup()
		return nil, err
	}
	pe.cmd = cmd
	go pe.wait()
	return pe, nil
}

type processExecution struct {
	guard *pathguard.Guard
	dir   string
	log   *slog.Logger
	cmd   *exec.Cmd

	mu      sync.Mutex
	partial []byte
	lines   []Event
	dropped int
	pr      int
	stopped bool
	result  *Result
}

// Write implements io.Writer for the program's stdout and stderr.
func (pe *processExecution) Write(b []byte) (int, error) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
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
	if len(pe.lines) >= maxBufferedLines {
		pe.dropped++
		return
	}
	pe.lines = append(pe.lines, Event{Kind: KindOutput, Text: capText(s)})
}

func (pe *processExecution) wait() {
	err := pe.cmd.Wait()
	if kerr := killProcessGroup(pe.cmd); kerr != nil {
		pe.log.Warn("runner could not kill a run's leftover processes", "dir", pe.dir, "err", kerr)
	}
	if errors.Is(err, exec.ErrWaitDelay) && pe.cmd.ProcessState.Success() {
		err = nil
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
