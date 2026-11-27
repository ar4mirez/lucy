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
	t     *testing.T
	dir   string
	berth *berthtest.Server
	store *eventstore.Store
	b     *runner.Broker
	sb    *sandbox.Sandbox
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
		Registry: reg, Projectors: []eventstore.Projector{runner.Projector{}}, SnapshotDir: filepath.Join(f.dir, "lucy", "state"),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { st.Close() })
	c, err := berth.New(berth.Options{Socket: f.berth.Socket})
	if err != nil {
		f.t.Fatal(err)
	}
	f.store, f.b = st, runner.NewBroker(st, nil)
	if f.sb, err = sandbox.New(st, c, f.b); err != nil {
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
	l, ok, err := f.b.Lease(ctx, "r1", placement, time.Minute)
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

func (f *fixture) confirm(org string, op sandbox.Op) {
	f.t.Helper()
	if _, err := f.sb.Confirm(context.Background(), org, op); err != nil {
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
	if _, err := f.sb.Confirm(context.Background(), "acme", sandbox.OpRestart); !errors.Is(err, sandbox.ErrNothingHeld) {
		t.Fatalf("confirm with nothing held: %v", err)
	}
	f.request("acme", sandbox.OpRestart) // cleared: nothing to confirm
	if _, err := f.sb.Confirm(context.Background(), "acme", sandbox.OpRestart); !errors.Is(err, sandbox.ErrNothingHeld) {
		t.Fatalf("confirm after a clearance: %v", err)
	}
	f.live("acme", "f1", runner.PlacementContainer)
	f.request("acme", sandbox.OpRestart)
	f.confirm("acme", sandbox.OpRestart)
	if _, err := f.sb.Confirm(context.Background(), "acme", sandbox.OpRestart); !errors.Is(err, sandbox.ErrNothingHeld) {
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
	if _, err := f.sb.Confirm(ctx, "acme/x", sandbox.OpDown); err == nil {
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
