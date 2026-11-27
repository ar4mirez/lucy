package server

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/spec"
	"github.com/ar4mirez/lucy/internal/worktree"
)

// gitIn runs git for a test in dir, with no user or system configuration, and returns its output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@localhost", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@localhost"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remoteRepo makes a bare repository with one commit on main, and returns its path.
func remoteRepo(t *testing.T, dir string) string {
	t.Helper()
	remote, src := filepath.Join(dir, "remote.git"), filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	gitIn(t, src, "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, src, "add", "README.md")
	gitIn(t, src, "commit", "--quiet", "-m", "start")
	gitIn(t, src, "push", "--quiet", remote, "HEAD:main")
	return remote
}

// withWorktrees gives the server a broker and a worktree pool over a local remote, for acme's
// project web; git, when not empty, is the program the pool runs.
func withWorktrees(t *testing.T, src **worktree.Source, git string) func(*Options) {
	dir := t.TempDir()
	remote := remoteRepo(t, dir)
	return func(o *Options) {
		withBroker(o)
		s := &worktree.Source{Pool: &worktree.Pool{Store: o.Store, Root: filepath.Join(dir, "pool"), Git: git},
			Remote: func(string) string { return remote }, Context: ctx, Log: slog.New(slog.DiscardHandler)}
		s.SetRunways([]spec.Resource{{APIVersion: spec.LucyAPIVersion, Kind: "Runway",
			Metadata: spec.Metadata{Name: "web", Namespace: "acme"}, Spec: map[string]any{"repository": "acme/web"}}})
		t.Cleanup(s.Wait)
		o.Worktrees, *src = s, s
	}
}

// startedExec is an Executor whose work ends done at its first poll, recording each lease started.
type startedExec struct {
	mu     sync.Mutex
	leases []runner.Lease
}

func (e *startedExec) Start(_ context.Context, l runner.Lease) (runner.Execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.leases = append(e.leases, l)
	return doneExec{}, nil
}

func (e *startedExec) started() []runner.Lease {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]runner.Lease(nil), e.leases...)
}

type doneExec struct{}

func (doneExec) Poll() runner.Update {
	return runner.Update{Result: &runner.Result{Outcome: runner.OutcomeDone}}
}
func (doneExec) Stop() {}

// stepUntil steps r until done reports true, or fails the test after 20 s.
func stepUntil(t *testing.T, r *runner.Runner, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		if err := r.Step(ctx); err != nil {
			t.Fatalf("step: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A first fetch slower than an API call's deadline still ends with the slot ready and the run
// started in it: runner.lease reserves the slot and returns at once, the daemon prepares it in the
// background, and the runner holds the lease, heartbeating, until a heartbeat reports it ready. A
// preparation that fails reports why, and the runner fails the run without starting it.
func TestSlowFirstFetchStillStartsTheRun(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "git-slow-fetch")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\ncase \"$*\" in *fetch*) sleep 1;; esac\nexec git \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var src *worktree.Source
	f := start(t, os.Geteuid(), 0, func(o *Options) {
		withWorktrees(t, &src, slow)(o)
		o.CallTimeout = 300 * time.Millisecond
	})
	b := f.srv.opts.Runs
	if err := b.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	ex := &startedExec{}
	r, err := runner.New(runner.Config{ID: "acme-host-1", Org: "acme", Protocol: runner.Remote{Client: f.client}, Executor: ex, TTL: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stepUntil(t, r, func() bool { return len(ex.started()) == 1 })
	l := ex.started()[0]
	if l.WorktreeState != runner.WorktreeReady || !strings.HasPrefix(l.Branch, "lucy/f1-1-") {
		t.Fatalf("the started lease %+v", l)
	}
	if b, err := os.ReadFile(filepath.Join(l.Worktree, "README.md")); err != nil || string(b) != "hello\n" {
		t.Fatalf("the run's checkout: %q, %v", b, err)
	}
	stepUntil(t, r, func() bool { return len(r.Held()) == 0 })
	src.Wait()
	slots, err := src.List(ctx, "acme")
	if err != nil || len(slots) != 1 || slots[0].State != worktree.StateFree {
		t.Fatalf("after the run: %+v, %v", slots, err)
	}

	// A base the remote lacks: the preparation fails, and so does the run, never started.
	if err := b.Submit(ctx, runner.Task{Run: "f2#1", Org: "acme", Project: "web", Flight: "f2", Generation: 1, Goal: "g", Base: "nope"}); err != nil {
		t.Fatal(err)
	}
	stepUntil(t, r, func() bool {
		ri, err := b.Run(ctx, "f2#1")
		return err == nil && ri.State.Final()
	})
	ri, _ := b.Run(ctx, "f2#1")
	if ri.State != runner.StateFailed || !strings.Contains(ri.Reason, "not a branch of the remote") || len(ex.started()) != 1 {
		t.Fatalf("a failed preparation's run: %+v, started %d", ri, len(ex.started()))
	}
}

// fakeWorktrees records the runs it is asked to place.
type fakeWorktrees struct {
	mu       sync.Mutex
	acquired []string
}

func (w *fakeWorktrees) Acquire(_ context.Context, _, _, run, _ string) (worktree.Lease, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.acquired = append(w.acquired, run)
	return worktree.Lease{Slot: 1, Path: "/pool/acme/web/slot-1", Branch: "lucy/" + run}, true, nil
}
func (w *fakeWorktrees) State(context.Context, string, string, string) (string, string, error) {
	return worktree.PrepPreparing, "", nil
}
func (w *fakeWorktrees) Release(context.Context, string, string, string) error { return nil }
func (w *fakeWorktrees) List(context.Context, string) ([]worktree.SlotInfo, error) {
	return nil, nil
}
func (w *fakeWorktrees) Discard(context.Context, string, string, int, string) (worktree.SlotInfo, error) {
	return worktree.SlotInfo{}, errors.New("not here")
}

// A run placed in its org's container gets no slot of the worktree pool, whose path would be the
// host's: it works in its scratch directory there. A host run of the same project gets one.
func TestContainerLeaseGetsNoWorktree(t *testing.T) {
	wt := &fakeWorktrees{}
	f := start(t, os.Geteuid(), 0, func(o *Options) { withProvenPlacement(o); o.Worktrees = wt })
	for _, run := range []string{"f1#1", "f2#1"} {
		if err := f.srv.opts.Runs.Submit(ctx, runner.Task{Run: run, Org: "acme", Project: "web", Flight: strings.TrimSuffix(run, "#1"), Generation: 1, Goal: "g"}); err != nil {
			t.Fatal(err)
		}
	}
	remote := runner.Remote{Client: f.client}
	cl, ok, err := remote.Lease(ctx, "box", runner.PlacementContainer, "acme", "test-proof", time.Minute)
	if err != nil || !ok || cl.Worktree != "" || cl.Branch != "" || cl.WorktreeState != "" || cl.WorktreeError != "" {
		t.Fatalf("the container lease: %+v, %v, %v", cl, ok, err)
	}
	beat, err := remote.Heartbeat(ctx, "box", cl.Run, cl.Fence, time.Minute, nil, "")
	if err != nil || beat.WorktreeState != "" {
		t.Fatalf("the container run's heartbeat: %+v, %v", beat, err)
	}
	hl, ok, err := remote.Lease(ctx, "acme-host-1", runner.PlacementHost, "acme", "", time.Minute)
	if err != nil || !ok || hl.Worktree == "" || hl.WorktreeState != runner.WorktreePreparing {
		t.Fatalf("the host lease: %+v, %v, %v", hl, ok, err)
	}
	if wt.acquired == nil || len(wt.acquired) != 1 || wt.acquired[0] != hl.Run {
		t.Fatalf("slots asked for %v; only the host run %s should be", wt.acquired, hl.Run)
	}
}

// worktree.list shows each slot, and why a quarantined one is held; worktree.discard removes a
// quarantined slot only when the confirmation repeats it exactly, never a leased one, and only for
// a caller with the admin scope, recording the discard (by whom) before it removes anything.
func TestWorktreeListAndDiscard(t *testing.T) {
	var src *worktree.Source
	f := start(t, os.Geteuid(), 0, withWorktrees(t, &src, ""))
	b := f.srv.opts.Runs
	remote := runner.Remote{Client: f.client}
	lease := func(run string) runner.Lease {
		t.Helper()
		if err := b.Submit(ctx, runner.Task{Run: run, Org: "acme", Project: "web", Flight: strings.TrimSuffix(run, "#1"), Generation: 1, Goal: "g"}); err != nil {
			t.Fatal(err)
		}
		l, ok, err := remote.Lease(ctx, "acme-host-1", runner.PlacementHost, "acme", "", time.Minute)
		if err != nil || !ok {
			t.Fatalf("lease %s: %v, %v", run, ok, err)
		}
		src.Wait()
		return l
	}
	l := lease("f1#1")
	if beat, err := remote.Heartbeat(ctx, "acme-host-1", l.Run, l.Fence, time.Minute, nil, ""); err != nil || beat.WorktreeState != runner.WorktreeReady {
		t.Fatalf("the prepared run's heartbeat: %+v, %v", beat, err)
	}
	if err := os.WriteFile(filepath.Join(l.Worktree, "work.txt"), []byte("w"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.Worktree, "add", "work.txt")
	gitIn(t, l.Worktree, "commit", "--quiet", "-m", "work")
	if err := remote.Finish(ctx, "acme-host-1", l.Run, l.Fence, runner.Result{Outcome: runner.OutcomeDone}); err != nil {
		t.Fatal(err)
	}
	src.Wait()
	leased := lease("f2#1")

	list, err := f.client.Worktrees(ctx, "acme")
	if err != nil || len(list.Slots) != 2 {
		t.Fatalf("ls: %+v, %v", list, err)
	}
	q := list.Slots[0]
	if q.Slot != "acme/web/1" || q.State != worktree.StateQuarantined || q.Reason != worktree.QuarantinedUnpushed || q.Run != "f1#1" || q.Branch != l.Branch {
		t.Fatalf("the quarantined slot %+v", q)
	}
	if s := list.Slots[1]; s.State != worktree.StateLeased || s.Prep != worktree.PrepReady || s.Run != "f2#1" {
		t.Fatalf("the leased slot %+v", s)
	}
	if other, err := f.client.Worktrees(ctx, "beta"); err != nil || len(other.Slots) != 0 {
		t.Fatalf("another org's slots: %+v, %v", other, err)
	}

	for name, p := range map[string]api.WorktreeDiscardParams{
		"a wrong confirmation": {Slot: "acme/web/1", Confirm: "acme/web/2"},
		"a leased slot":        {Slot: "acme/web/2", Confirm: "acme/web/2"},
		"no such slot":         {Slot: "acme/web/9", Confirm: "acme/web/9"},
		"a malformed slot":     {Slot: "acme/web", Confirm: "acme/web"},
	} {
		if _, err := f.client.WorktreeDiscard(ctx, p); !api.IsCode(err, api.CodeInvalidParams) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Lstat(leased.Worktree); err != nil {
		t.Fatalf("a refused discard touched the leased slot: %v", err)
	}
	head := f.storeHead(t)
	d, err := f.client.WorktreeDiscard(ctx, api.WorktreeDiscardParams{Slot: "acme/web/1", Confirm: "acme/web/1"})
	if err != nil || d.State != worktree.StateDiscarded {
		t.Fatalf("discard: %+v, %v", d, err)
	}
	if _, err := os.Lstat(l.Worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discarded slot's directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(leased.Worktree, "README.md")); err != nil {
		t.Fatalf("the other slot: %v", err)
	}
	evs, err := f.store.ReadAfter(ctx, head, 10)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
		if e.Type == (worktree.Discarded{}).EventType() && !strings.Contains(string(e.Payload), `"by":"local/uid/`+strconv.Itoa(os.Geteuid())+`"`) {
			t.Errorf("the discard does not say by whom: %s", e.Payload)
		}
	}
	if got := strings.Join(types, ","); got != "api.command,worktree.discarded" {
		t.Fatalf("recorded %s", got)
	}

	// A caller without the admin scope may list, but not discard.
	g := start(t, os.Geteuid(), 0, func(o *Options) { withBroker(o); o.Worktrees = &fakeWorktrees{}; o.Authorizer = denyAdmin{} })
	if _, err := g.client.Worktrees(ctx, ""); err != nil {
		t.Fatalf("a read-only caller's ls: %v", err)
	}
	if _, err := g.client.WorktreeDiscard(ctx, api.WorktreeDiscardParams{Slot: "acme/web/1", Confirm: "acme/web/1"}); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("a read-only caller's discard: %v", err)
	}
}

func (f *fixture) storeHead(t *testing.T) int64 {
	t.Helper()
	h, err := f.store.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// A pool root others can open fails the lease at once, with worktree_error, and no slot is recorded.
func TestOpenPoolRootFailsTheLeaseAtOnce(t *testing.T) {
	var src *worktree.Source
	f := start(t, os.Geteuid(), 0, withWorktrees(t, &src, ""))
	if err := os.MkdirAll(src.Pool.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src.Pool.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.opts.Runs.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	l, ok, err := runner.Remote{Client: f.client}.Lease(ctx, "acme-host-1", runner.PlacementHost, "acme", "", time.Minute)
	if err != nil || !ok || l.WorktreeState != runner.WorktreeFailed || !strings.Contains(l.WorktreeError, "only its owner can open") || l.Worktree != "" {
		t.Fatalf("the lease: %+v, %v, %v", l, ok, err)
	}
	if slots, err := src.List(ctx, ""); err != nil || len(slots) != 0 {
		t.Fatalf("slots %+v, %v", slots, err)
	}
}

// A run whose slot is ready soon after the lease starts within about a second, through runner.slot,
// not on the lease's next heartbeat, which with a 60 s lease is 20 s away.
func TestReadySlotStartsTheRunWithinASecond(t *testing.T) {
	var src *worktree.Source
	f := start(t, os.Geteuid(), 0, withWorktrees(t, &src, ""))
	if err := f.srv.opts.Runs.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	ex := &startedExec{}
	r, err := runner.New(runner.Config{ID: "acme-host-1", Org: "acme", Protocol: runner.Remote{Client: f.client}, Executor: ex, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	begun := time.Now()
	go func() { done <- r.Run(rctx, time.Second) }()
	defer func() { cancel(); <-done }()
	for len(ex.started()) == 0 {
		if time.Since(begun) > 10*time.Second {
			t.Fatal("the run never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The slot's preparation (a local clone) takes a fraction of that; the polls start at 250 ms.
	if took := time.Since(begun); took > 2*time.Second {
		t.Fatalf("the run started %v after the lease; its slot was ready well before", took)
	}
	if l := ex.started()[0]; l.WorktreeState != runner.WorktreeReady {
		t.Fatalf("the started lease %+v", l)
	}
}

// runner.slot reads only: polled many times it appends no event and no audit entry, and it refuses
// a runner that does not hold the run under that fence.
func TestRunnerSlotRecordsNothingAndIsFenced(t *testing.T) {
	f := start(t, os.Geteuid(), 0, func(o *Options) { withBroker(o); o.Worktrees = &fakeWorktrees{} })
	if err := f.srv.opts.Runs.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	remote := runner.Remote{Client: f.client}
	l, ok, err := remote.Lease(ctx, "acme-host-1", runner.PlacementHost, "acme", "", time.Minute)
	if err != nil || !ok || l.WorktreeState != runner.WorktreePreparing {
		t.Fatalf("lease: %+v, %v, %v", l, ok, err)
	}
	head := func() (int64, api.AuditReport) {
		t.Helper()
		st, err := f.client.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ar, err := f.client.VerifyAudit(ctx)
		if err != nil || !ar.Verified {
			t.Fatalf("audit: %+v, %v", ar, err)
		}
		return st.EventHead, ar
	}
	seq, audit := head()
	for range 50 {
		state, why, err := remote.Slot(ctx, "acme-host-1", l.Run, l.Fence)
		if err != nil || state != runner.WorktreePreparing || why != "" {
			t.Fatalf("slot: %q, %q, %v", state, why, err)
		}
	}
	if seq2, audit2 := head(); seq2 != seq || audit2.Entries != audit.Entries || audit2.Head != audit.Head {
		t.Fatalf("50 slot polls moved the log from %d to %d and the audit chain from %+v to %+v", seq, seq2, audit, audit2)
	}
	for _, c := range []struct {
		runner string
		fence  int64
	}{{"acme-host-2", l.Fence}, {"acme-host-1", l.Fence + 1}} {
		if _, _, err := remote.Slot(ctx, c.runner, l.Run, c.fence); !errors.Is(err, runner.ErrFenced) {
			t.Fatalf("slot as %s under fence %d: %v, want fenced", c.runner, c.fence, err)
		}
	}
}
