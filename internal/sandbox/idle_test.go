package sandbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/sandbox"
)

// The oracle for these tests is LUCY-SBX-001 with LUCY-SBX-003: only orgs with active work keep a
// running container, and stopping one is the same guarded `down` as any other, so it never stops
// a session without the owner's confirmation.

const after = 30 * time.Minute

// ended runs one run of org to its end in a container and returns when it ended (about now).
func (f *fixture) ended(org, flight string) {
	f.t.Helper()
	l := f.live(org, flight, runner.PlacementContainer)
	if err := f.b.Finish(context.Background(), "r1", l.Run, l.Fence, runner.Result{Outcome: runner.OutcomeFailed, Reason: "done"}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) stopIdle(org string, now time.Time) sandbox.IdleResult {
	f.t.Helper()
	r, err := f.sb.StopIdle(context.Background(), org, f.b, now, after)
	if err != nil {
		f.t.Fatalf("StopIdle %s: %v", org, err)
	}
	return r
}

func TestAnIdleContainerIsStopped(t *testing.T) {
	f := open(t)
	f.ended("acme", "f1")
	if r := f.stopIdle("acme", time.Now().Add(after/3)); r.Idle || r.Stop != nil || !r.Running || r.Last.IsZero() {
		t.Fatalf("before the idle time: %+v", r)
	}
	if f.changes() != 0 {
		t.Fatal("a container with recent work was stopped")
	}
	r := f.stopIdle("acme", time.Now().Add(after+time.Minute))
	if !r.Idle || r.Stop == nil || !r.Stop.Acted || r.Stop.Outcome != policy.Cleared {
		t.Fatalf("after the idle time: %+v", r)
	}
	ch := f.berth.Changes()
	if len(ch) != 1 || ch[0].Path != "/v1/orgs/acme/down" || f.berth.Running("acme") {
		t.Fatalf("berth changes %+v, running %v", ch, f.berth.Running("acme"))
	}
	ds := f.decisions()
	if last := ds[len(ds)-1]; last.Target.Resource != "down" || last.Actor.Role != policy.RoleDaemon {
		t.Errorf("the stop was not decided as the daemon's down: %+v", last)
	}
	// Now stopped, it is left alone: nothing is asked, nothing recorded.
	n := len(f.decisions())
	if r := f.stopIdle("acme", time.Now().Add(2*after)); r.Running || r.Idle {
		t.Fatalf("a stopped container: %+v", r)
	}
	if len(f.berth.Changes()) != 1 || len(f.decisions()) != n {
		t.Error("a stopped container was asked about again")
	}
}

func TestBusyContainersAreKept(t *testing.T) {
	later := time.Now().Add(2 * after)
	t.Run("a run is queued", func(t *testing.T) {
		f := open(t)
		f.ended("acme", "f1")
		if err := f.b.Submit(context.Background(), runner.Task{Run: "f2#1", Org: "acme", Project: "web", Flight: "f2", Generation: 1, Goal: "g"}); err != nil {
			t.Fatal(err)
		}
		if r := f.stopIdle("acme", later); !r.Busy || r.Idle || f.changes() != 0 {
			t.Fatalf("%+v, changes %d", r, f.changes())
		}
	})
	t.Run("a session is live in the container", func(t *testing.T) {
		f := open(t)
		f.live("acme", "f1", runner.PlacementContainer)
		if r := f.stopIdle("acme", later); !r.Busy || r.Idle || f.changes() != 0 {
			t.Fatalf("%+v, changes %d", r, f.changes())
		}
	})
	t.Run("lucy never ran work in the org", func(t *testing.T) {
		f := open(t)
		if r := f.stopIdle("acme", later); r.Idle || !r.Last.IsZero() || f.changes() != 0 || len(f.decisions()) != 0 {
			t.Fatalf("%+v, changes %d", r, f.changes())
		}
	})
}

// A session on this machine does not use the org's container, so it does not keep it running.
func TestHostSessionsDoNotKeepAContainer(t *testing.T) {
	f := open(t)
	f.live("acme", "host", runner.PlacementHost)
	r := f.stopIdle("acme", time.Now().Add(after+time.Minute))
	if r.Busy || !r.Idle || r.Stop == nil || !r.Stop.Acted {
		t.Fatalf("%+v", r)
	}
}

// activity is an Activity that reports what a check saw before a session started.
type activity struct{ last time.Time }

func (a activity) OrgActivity(context.Context, string) (bool, time.Time, error) { return false, a.last, nil }

// A session that starts between the idle check and the stop is not stopped: the guard sees it and
// holds the stop for the owner, as for any other down.
func TestASessionStartedSinceTheCheckHoldsTheStop(t *testing.T) {
	f := open(t)
	f.live("acme", "f1", runner.PlacementContainer)
	r, err := f.sb.StopIdle(context.Background(), "acme", activity{last: time.Now().Add(-2 * after)}, time.Now(), after)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Idle || r.Stop == nil || r.Stop.Acted || r.Stop.Outcome != policy.Holding || !r.Stop.NeedsOwner {
		t.Fatalf("%+v", r)
	}
	if f.changes() != 0 || !f.berth.Running("acme") {
		t.Fatal("a live session's container was stopped as idle")
	}
}

func TestStopIdleOrgsTouchesOnlyTheOrgsGiven(t *testing.T) {
	f := open(t)
	f.berth.SetRunning("beta", true)
	f.ended("acme", "f1")
	f.ended("beta", "b1")
	res, err := f.sb.StopIdleOrgs(context.Background(), []string{"gamma", "acme"}, f.b, time.Now().Add(2*after), after)
	if !errors.Is(err, sandbox.ErrNotFound) || len(res) != 2 || res[0].Error == "" || !res[1].Idle || res[1].Stop == nil || !res[1].Stop.Acted {
		t.Fatalf("results %+v, err %v", res, err)
	}
	if f.berth.Running("acme") || !f.berth.Running("beta") {
		t.Fatalf("acme running %v, beta running %v: only acme was given", f.berth.Running("acme"), f.berth.Running("beta"))
	}
	if _, err := f.sb.StopIdle(context.Background(), "acme", nil, time.Now(), after); err == nil {
		t.Error("StopIdle without activity")
	}
}
