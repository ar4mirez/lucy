package sandbox_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/sandbox"
	"github.com/ar4mirez/lucy/internal/sandbox/berth"
	"github.com/ar4mirez/lucy/internal/sandbox/berth/berthtest"
)

// These tests drive the guard end to end: lucy's event store, the runner broker as the source of
// live sessions, and berth's provider client against the fake berth API. The oracle for what may
// happen is LUCY-SBX-003: an org's container is never started, restarted or stopped while a
// session lives in it unless the owner confirmed stopping exactly those sessions.

type fixture struct {
	t      *testing.T
	dir    string
	berth  *berthtest.Server
	store  *eventstore.Store
	b      *runner.Broker
	sb     *sandbox.Sandbox
	skew   time.Duration                            // the broker's clock runs this far ahead
	during func(ctx context.Context, op sandbox.Op) // called as the provider starts an operation
}

// provider calls the fixture's during hook before berth runs an operation.
type provider struct {
	sandbox.Provider
	f *fixture
}

func (p provider) Do(ctx context.Context, org string, op sandbox.Op) error {
	if p.f.during != nil {
		p.f.during(ctx, op)
	}
	return p.Provider.Do(ctx, org, op)
}

func open(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), berth: berthtest.Start(t, "acme", "beta")}
	f.berth.SetRunning("acme", true)
	f.reopen()
	return f
}

// reopen opens the store again: a daemon restart.
func (f *fixture) reopen() {
	f.t.Helper()
	if f.store != nil {
		if err := f.store.Close(); err != nil {
			f.t.Fatal(err)
		}
	}
	reg, err := eventstore.NewRegistry(append(append(runner.Payloads(), sandbox.Payloads()...), policy.DecisionRecord{})...)
	if err != nil {
		f.t.Fatal(err)
	}
	st, err := eventstore.Open(context.Background(), filepath.Join(f.dir, "lucy", "lucy.db"), eventstore.Options{
		Registry: reg, Projectors: []eventstore.Projector{runner.Projector{}, sandbox.Projector{}}, SnapshotDir: filepath.Join(f.dir, "lucy", "state"),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { st.Close() })
	c, err := berth.New(berth.Options{Socket: f.berth.Socket})
	if err != nil {
		f.t.Fatal(err)
	}
	f.store, f.b = st, runner.NewBroker(st, func() time.Time { return time.Now().Add(f.skew) })
	f.b.VerifyPlacementWith(testPlacement{})
	if f.sb, err = sandbox.New(st, provider{Provider: c, f: f}, f.b); err != nil {
		f.t.Fatal(err)
	}
}

// live starts a run in org leased by a runner placed as placement, and returns it.
func (f *fixture) live(org, flight string, placement runner.Placement) runner.Lease {
	f.t.Helper()
	ctx := context.Background()
	if err := f.b.Submit(ctx, runner.Task{Run: runner.RunID(flight, 1), Org: org, Project: "web", Flight: flight,
		Generation: 1, Goal: "fix the build"}); err != nil {
		f.t.Fatal(err)
	}
	l, ok, err := f.b.Lease(ctx, "r1", placement, org, "test-proof", time.Minute)
	if err != nil || !ok {
		f.t.Fatalf("lease: %v %v", ok, err)
	}
	return l
}

func (f *fixture) request(org string, op sandbox.Op) sandbox.Result {
	f.t.Helper()
	res, err := f.sb.Request(context.Background(), org, op, sandbox.Daemon("test"))
	if err != nil {
		f.t.Fatalf("%s %s: %v", op, org, err)
	}
	return res
}

// confirm confirms the operation that waits on the owner, as `lucy sandbox held` shows it.
func (f *fixture) confirm(org string, op sandbox.Op) {
	f.t.Helper()
	held, err := f.sb.Held(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	var seq int64
	for _, h := range held {
		if h.Org == org && h.Op == op {
			seq = h.DecisionSeq
		}
	}
	if _, err := f.sb.Confirm(context.Background(), org, op, seq); err != nil {
		f.t.Fatalf("confirm %s %s: %v", op, org, err)
	}
}

// changes is how many changes berth was asked for.
func (f *fixture) changes() int { return len(f.berth.Changes()) }

// decisions returns the policy decisions recorded, oldest first.
func (f *fixture) decisions() []policy.DecisionRecord {
	f.t.Helper()
	evs, err := f.store.ReadStream(context.Background(), policy.Stream, 0, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []policy.DecisionRecord
	for _, e := range evs {
		d, err := eventstore.DecodeAs[policy.DecisionRecord](f.store.Registry(), e)
		if err != nil {
			f.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func TestNoLiveSessionIsCleared(t *testing.T) {
	f := open(t)
	res := f.request("acme", sandbox.OpRestart)
	if res.Outcome != policy.Cleared || !res.Acted || res.NeedsOwner || len(res.LiveSessions) != 0 {
		t.Fatalf("restart with no live session: %+v", res)
	}
	if f.changes() != 1 || !f.berth.Running("acme") {
		t.Fatalf("berth changes %d, running %v", f.changes(), f.berth.Running("acme"))
	}
	ds := f.decisions()
	if len(ds) != 1 || ds[0].Kind != policy.KindContainer || ds[0].Target.Resource != "restart" || ds[0].Rule != "container.live-sessions" {
		t.Fatalf("decisions: %+v", ds)
	}
	if res.DecisionSeq == 0 {
		t.Error("the result names no decision record")
	}
	if res := f.request("acme", sandbox.OpDown); !res.Acted || f.berth.Running("acme") {
		t.Errorf("down: %+v", res)
	}
}

func TestLiveSessionsHoldUntilTheOwnerConfirms(t *testing.T) {
	f := open(t)
	f.live("acme", "f1", runner.PlacementContainer)
	res := f.request("acme", sandbox.OpRestart)
	if res.Outcome != policy.Holding || res.Acted || !res.NeedsOwner || !slices.Equal(res.LiveSessions, []string{"f1#1"}) {
		t.Fatalf("restart with a live session: %+v", res)
	}
	if f.changes() != 0 {
		t.Fatalf("berth was asked to restart a container with a live session: %+v", f.berth.Changes())
	}
	// The owner's confirmation of the restart confirms nothing else.
	f.confirm("acme", sandbox.OpRestart)
	if res := f.request("acme", sandbox.OpDown); res.Outcome != policy.Holding || res.Acted {
		t.Fatalf("a confirmed restart allowed a stop: %+v", res)
	}
	if res := f.request("acme", sandbox.OpRestart); res.Outcome != policy.Cleared || !res.Acted {
		t.Fatalf("restart after the owner confirmed: %+v", res)
	}
	if f.changes() != 1 {
		t.Fatalf("berth changes %d, want 1", f.changes())
	}
	// One confirmation stops the container once.
	if res := f.request("acme", sandbox.OpRestart); res.Outcome != policy.Holding || res.Acted {
		t.Fatalf("a spent confirmation was used again: %+v", res)
	}
	if f.changes() != 1 {
		t.Fatalf("berth changes %d, want 1", f.changes())
	}
}

func TestAConfirmationLapsesWhenSessionsChange(t *testing.T) {
	f := open(t)
	f.live("acme", "f1", runner.PlacementContainer)
	f.request("acme", sandbox.OpDown)
	f.confirm("acme", sandbox.OpDown)
	f.live("acme", "f2", runner.PlacementContainer) // a second session started after the owner looked
	res := f.request("acme", sandbox.OpDown)
	if res.Outcome != policy.Denied || res.Acted || !res.NeedsOwner || !slices.Equal(res.LiveSessions, []string{"f1#1", "f2#1"}) {
		t.Fatalf("down after a new session started: %+v", res)
	}
	if f.changes() != 0 {
		t.Fatal("berth was asked to stop a container on a lapsed confirmation")
	}
	// The owner confirms what is live now.
	f.confirm("acme", sandbox.OpDown)
	if res := f.request("acme", sandbox.OpDown); res.Outcome != policy.Cleared || !res.Acted {
		t.Fatalf("down after the owner confirmed both sessions: %+v", res)
	}
}

func TestOnlyContainerSessionsInTheOrgCount(t *testing.T) {
	f := open(t)
	f.live("acme", "host", runner.PlacementHost) // runs on this machine, not in the container
	f.live("beta", "b1", runner.PlacementContainer)
	if res := f.request("acme", sandbox.OpRestart); res.Outcome != policy.Cleared || len(res.LiveSessions) != 0 {
		t.Fatalf("acme restart: %+v", res)
	}
	if res := f.request("beta", sandbox.OpRestart); res.Outcome != policy.Holding {
		t.Fatalf("beta restart: %+v", res)
	}
}

func TestEndedSessionsDoNotHold(t *testing.T) {
	f := open(t)
	l := f.live("acme", "f1", runner.PlacementContainer)
	if err := f.b.Finish(context.Background(), "r1", l.Run, l.Fence, runner.Result{Outcome: runner.OutcomeFailed, Reason: "gave up"}); err != nil {
		t.Fatal(err)
	}
	if res := f.request("acme", sandbox.OpRestart); res.Outcome != policy.Cleared || !res.Acted {
		t.Fatalf("restart once the session ended: %+v", res)
	}
}

func TestConfirmNeedsAHeldRequest(t *testing.T) {
	f := open(t)
	if _, err := f.sb.Confirm(context.Background(), "acme", sandbox.OpRestart, 0); !errors.Is(err, sandbox.ErrNothingHeld) {
		t.Fatalf("confirm with nothing held: %v", err)
	}
	f.request("acme", sandbox.OpRestart) // cleared: nothing to confirm
	if _, err := f.sb.Confirm(context.Background(), "acme", sandbox.OpRestart, 0); !errors.Is(err, sandbox.ErrNothingHeld) {
		t.Fatalf("confirm after a clearance: %v", err)
	}
	f.live("acme", "f1", runner.PlacementContainer)
	f.request("acme", sandbox.OpRestart)
	f.confirm("acme", sandbox.OpRestart)
	if _, err := f.sb.Confirm(context.Background(), "acme", sandbox.OpRestart, 0); !errors.Is(err, sandbox.ErrNothingHeld) {
		t.Fatalf("a second confirmation of the same request: %v", err)
	}
}

func TestConfirmationsSurviveARestart(t *testing.T) {
	f := open(t)
	f.live("acme", "f1", runner.PlacementContainer)
	f.request("acme", sandbox.OpRestart)
	f.reopen()
	f.confirm("acme", sandbox.OpRestart)
	f.reopen()
	if res := f.request("acme", sandbox.OpRestart); res.Outcome != policy.Cleared || !res.Acted {
		t.Fatalf("restart confirmed across daemon restarts: %+v", res)
	}
	f.reopen()
	if res := f.request("acme", sandbox.OpRestart); res.Outcome != policy.Holding {
		t.Fatalf("a confirmation spent before a restart was used again: %+v", res)
	}
}

func TestAgentsNeverOperateContainers(t *testing.T) {
	f := open(t)
	worker := sandbox.Requester{Actor: policy.Actor{ID: "org/acme/worker/1", Role: policy.RoleWorker, Org: "acme"},
		Inputs: []policy.Input{{Source: policy.SourceTask, Ref: "task/1"}}}
	res, err := f.sb.Request(context.Background(), "acme", sandbox.OpRestart, worker)
	if err != nil || res.Outcome != policy.Denied || res.Rule != "container.role" || res.Acted {
		t.Fatalf("a worker's restart: %+v, %v", res, err)
	}
	if f.changes() != 0 {
		t.Fatal("berth was asked on an agent's request")
	}
	if len(f.decisions()) != 1 {
		t.Error("the denial was not recorded")
	}
}

func TestProviderFailureIsRecorded(t *testing.T) {
	f := open(t)
	f.berth.FailNext("acme", berthtest.ErrInjected)
	res, err := f.sb.Request(context.Background(), "acme", sandbox.OpRestart, sandbox.Daemon("test"))
	if err == nil || res.Acted || res.Outcome != policy.Cleared {
		t.Fatalf("a failed restart: %+v, %v", res, err)
	}
	evs, err := f.store.ReadStream(context.Background(), sandbox.Stream("acme"), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	acted, err := eventstore.DecodeAs[sandbox.Acted](f.store.Registry(), last)
	if err != nil || acted.Error == "" || acted.DecisionSeq != res.DecisionSeq {
		t.Fatalf("last event %s: %+v, %v", last.Type, acted, err)
	}
}

func TestRefusesBadInput(t *testing.T) {
	f := open(t)
	ctx := context.Background()
	if _, err := f.sb.Request(ctx, "Acme", sandbox.OpRestart, sandbox.Daemon("test")); err == nil {
		t.Error("a bad org name was requested")
	}
	if _, err := f.sb.Request(ctx, "acme", "destroy", sandbox.Daemon("test")); err == nil {
		t.Error("an unknown operation was requested")
	}
	if _, err := f.sb.Confirm(ctx, "acme/x", sandbox.OpDown, 1); err == nil {
		t.Error("a bad org name was confirmed")
	}
	if f.changes() != 0 || len(f.decisions()) != 0 {
		t.Error("bad input reached berth or the policy log")
	}
	if _, err := sandbox.New(nil, nil, nil); err == nil {
		t.Error("New without its dependencies")
	}
}

func TestStatusIsARead(t *testing.T) {
	f := open(t)
	s, err := f.sb.Status(context.Background(), "acme")
	if err != nil || !s.Running {
		t.Fatalf("status: %+v, %v", s, err)
	}
	if f.changes() != 0 || len(f.decisions()) != 0 {
		t.Error("a status read changed something or asked the policy engine")
	}
}

func TestAnUnusedConfirmationIsRetiredByAClearance(t *testing.T) {
	f := open(t)
	l := f.live("acme", "f1", runner.PlacementContainer)
	f.request("acme", sandbox.OpRestart)
	f.confirm("acme", sandbox.OpRestart)
	// The session ends before the daemon asks again, so the restart clears without the owner.
	if err := f.b.Finish(context.Background(), "r1", l.Run, l.Fence, runner.Result{Outcome: runner.OutcomeFailed, Reason: "gave up"}); err != nil {
		t.Fatal(err)
	}
	if res := f.request("acme", sandbox.OpRestart); res.Outcome != policy.Cleared || !res.Acted {
		t.Fatalf("restart once the session ended: %+v", res)
	}
	// A later session is one the owner never saw: the restart waits for the owner again.
	f.live("acme", "f3", runner.PlacementContainer)
	res := f.request("acme", sandbox.OpRestart)
	if res.Outcome != policy.Holding || !res.NeedsOwner || !slices.Equal(res.LiveSessions, []string{"f3#1"}) {
		t.Fatalf("restart with a session the owner never confirmed: %+v", res)
	}
}

// leaseIn submits a run of org and tries to lease it to a runner placed in a container.
func (f *fixture) leaseIn(ctx context.Context, org, flight string) error {
	if err := f.b.Submit(ctx, runner.Task{Run: runner.RunID(flight, 1), Org: org, Project: "web", Flight: flight,
		Generation: 1, Goal: "fix the build"}); err != nil && !errors.Is(err, runner.ErrExists) {
		return err
	}
	_, ok, err := f.b.Lease(ctx, "r2", runner.PlacementContainer, org, "test-proof", time.Minute)
	if err == nil && !ok {
		return errors.New("nothing to lease")
	}
	return err
}

func TestNoSessionIsPlacedWhileTheContainerStops(t *testing.T) {
	for _, tc := range []struct {
		name      string
		op        sandbox.Op
		confirmed bool // a live session the owner confirmed stopping
	}{
		{"restart", sandbox.OpRestart, false},
		{"down", sandbox.OpDown, false},
		{"confirmed restart", sandbox.OpRestart, true},
		{"confirmed down", sandbox.OpDown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := open(t)
			if tc.confirmed {
				f.live("acme", "f1", runner.PlacementContainer)
				f.request("acme", tc.op)
				f.confirm("acme", tc.op)
			}
			var during error
			f.during = func(ctx context.Context, op sandbox.Op) { during = f.leaseIn(ctx, "acme", "f9") }
			res := f.request("acme", tc.op)
			if res.Outcome != policy.Cleared || !res.Acted {
				t.Fatalf("%s: %+v", tc.op, res)
			}
			if !errors.Is(during, runner.ErrSandboxBusy) {
				t.Fatalf("a lease while berth ran %s: %v, want ErrSandboxBusy", tc.op, during)
			}
			if live, err := f.b.LiveInContainer(context.Background(), "acme"); err != nil || len(live) != len(res.LiveSessions) {
				t.Fatalf("live after %s: %v, %v", tc.op, live, err)
			}
			f.during = nil
			if err := f.leaseIn(context.Background(), "acme", "f9"); err != nil {
				t.Fatalf("a lease once %s ended: %v", tc.op, err)
			}
		})
	}
}

func TestTheHoldIsReleasedWhenTheProviderFails(t *testing.T) {
	f := open(t)
	f.berth.FailNext("acme", berthtest.ErrInjected)
	if _, err := f.sb.Request(context.Background(), "acme", sandbox.OpDown, sandbox.Daemon("test")); err == nil {
		t.Fatal("a failed down reported no error")
	}
	if err := f.leaseIn(context.Background(), "acme", "f9"); err != nil {
		t.Fatalf("a lease after a failed down: %v", err)
	}
}

func TestHostSessionsAndOtherOrgsAreNotHeld(t *testing.T) {
	f := open(t)
	f.during = func(ctx context.Context, _ sandbox.Op) {
		if err := f.leaseIn(ctx, "beta", "b1"); err != nil {
			t.Errorf("a lease in another org while acme restarts: %v", err)
		}
		if err := f.b.Submit(ctx, runner.Task{Run: runner.RunID("h1", 1), Org: "acme", Project: "web", Flight: "h1",
			Generation: 1, Goal: "fix the build"}); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := f.b.Lease(ctx, "r3", runner.PlacementHost, "acme", "", time.Minute); err != nil || !ok {
			t.Errorf("a host lease while acme restarts: %v, %v", ok, err)
		}
	}
	f.request("acme", sandbox.OpRestart)
}

func TestAHoldLeftByACrashExpires(t *testing.T) {
	f := open(t)
	ctx := context.Background()
	// The daemon holds acme's container for a restart and dies before releasing it.
	if held, err := f.b.HoldContainer(ctx, "acme", nil, sandbox.HoldTTL); err != nil || !held {
		t.Fatalf("hold: %v, %v", held, err)
	}
	f.reopen()
	if err := f.leaseIn(ctx, "acme", "f9"); !errors.Is(err, runner.ErrSandboxBusy) {
		t.Fatalf("a lease after the restart, inside the hold: %v, want ErrSandboxBusy", err)
	}
	f.skew = sandbox.HoldTTL + time.Second
	f.reopen()
	if err := f.leaseIn(ctx, "acme", "f9"); err != nil {
		t.Fatalf("a lease once the hold expired: %v", err)
	}
}

func TestAHoldNeedsTheSessionsDecidedOn(t *testing.T) {
	f := open(t)
	ctx := context.Background()
	f.live("acme", "f1", runner.PlacementContainer)
	if held, err := f.b.HoldContainer(ctx, "acme", nil, sandbox.HoldTTL); err != nil || held {
		t.Fatalf("hold with a session the decision missed: %v, %v", held, err)
	}
	if err := f.leaseIn(ctx, "acme", "f9"); err != nil {
		t.Fatalf("a refused hold held the container: %v", err)
	}
	if held, err := f.b.HoldContainer(ctx, "acme", []string{"f1#1", "f9#1"}, sandbox.HoldTTL); err != nil || !held {
		t.Fatalf("hold on the live sessions: %v, %v", held, err)
	}
}

func TestAHeldOrgDoesNotHoldUpAnotherOrgsRun(t *testing.T) {
	f := open(t)
	ctx := context.Background()
	for _, tk := range []runner.Task{
		{Run: runner.RunID("f9", 1), Org: "acme", Project: "web", Flight: "f9", Generation: 1, Goal: "g"},
		{Run: runner.RunID("b1", 1), Org: "beta", Project: "web", Flight: "b1", Generation: 1, Goal: "g"},
	} {
		if err := f.b.Submit(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	if held, err := f.b.HoldContainer(ctx, "acme", nil, sandbox.HoldTTL); err != nil || !held {
		t.Fatalf("hold: %v, %v", held, err)
	}
	if _, _, err := f.b.Lease(ctx, "r2", runner.PlacementContainer, "acme", "test-proof", time.Minute); !errors.Is(err, runner.ErrSandboxBusy) {
		t.Fatalf("acme's container lease with acme held: %v, want ErrSandboxBusy", err)
	}
	l, ok, err := f.b.Lease(ctx, "r4", runner.PlacementContainer, "beta", "test-proof", time.Minute)
	if err != nil || !ok || l.Org != "beta" {
		t.Fatalf("beta's container lease with acme held: %+v, %v, %v; want beta's run", l, ok, err)
	}
	if l, ok, err := f.b.Lease(ctx, "r3", runner.PlacementHost, "acme", "", time.Minute); err != nil || !ok || l.Org != "acme" {
		t.Fatalf("a host lease with acme held: %+v, %v, %v", l, ok, err)
	}
	if _, ok, err := f.b.Lease(ctx, "r4", runner.PlacementContainer, "beta", "test-proof", time.Minute); err != nil || ok {
		t.Fatalf("a container lease with nothing queued: %v, %v", ok, err)
	}
}

func TestHeldListsWhatWaitsOnTheOwner(t *testing.T) {
	f := open(t)
	ctx := context.Background()
	if held, err := f.sb.Held(ctx); err != nil || len(held) != 0 {
		t.Fatalf("held at start: %+v, %v", held, err)
	}
	f.request("acme", sandbox.OpRestart) // cleared: nothing waits
	f.live("acme", "f1", runner.PlacementContainer)
	first := f.request("acme", sandbox.OpRestart)
	f.request("acme", sandbox.OpDown)
	held, err := f.sb.Held(ctx)
	if err != nil || len(held) != 2 {
		t.Fatalf("held: %+v, %v", held, err)
	}
	if h := held[0]; h.Org != "acme" || h.Op != sandbox.OpRestart || h.DecisionSeq != first.DecisionSeq ||
		h.Outcome != policy.Holding || !slices.Equal(h.LiveSessions, []string{"f1#1"}) || h.Since.IsZero() {
		t.Errorf("held restart: %+v", h)
	}
	// Asking again replaces what waits: the owner confirms the newest decision only.
	second := f.request("acme", sandbox.OpRestart)
	if _, err := f.sb.Confirm(ctx, "acme", sandbox.OpRestart, first.DecisionSeq); !errors.Is(err, sandbox.ErrStale) {
		t.Fatalf("confirming a replaced decision: %v, want ErrStale", err)
	}
	if _, err := f.sb.Confirm(ctx, "acme", sandbox.OpRestart, second.DecisionSeq); err != nil {
		t.Fatal(err)
	}
	held, _ = f.sb.Held(ctx)
	if len(held) != 1 || held[0].Op != sandbox.OpDown {
		t.Fatalf("held after the restart was confirmed: %+v", held)
	}
	// The projection is rebuilt from the log alone.
	if err := f.store.Rebuild(ctx, sandbox.Projector{}.Name()); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.sb.Held(ctx); !slices.EqualFunc(again, held, func(a, b sandbox.Held) bool {
		return a.Org == b.Org && a.Op == b.Op && a.DecisionSeq == b.DecisionSeq && a.Since.Equal(b.Since)
	}) {
		t.Errorf("rebuilt: %+v, want %+v", again, held)
	}
	// A cleared request releases what waited for its operation.
	f.request("acme", sandbox.OpRestart)
	if held, _ := f.sb.Held(ctx); len(held) != 1 {
		t.Errorf("held after the confirmed restart ran: %+v", held)
	}
}

// testPlacement is a test-only PlacementVerifier standing in for the Sandbox provider's: it
// accepts the proof "test-proof" and nothing else.
type testPlacement struct{}

func (testPlacement) VerifyPlacement(_ context.Context, _, _, proof string) error {
	if proof != "test-proof" {
		return errors.New("not a placement proof")
	}
	return nil
}
