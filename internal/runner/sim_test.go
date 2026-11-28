package runner_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/audit"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/sim"
	"github.com/ar4mirez/lucy/internal/tower"
)

// These tests fly whole flights through the runner protocol on the simulation harness: the
// Tower starts Pilots through runner.Runtime, runners lease the runs and drive the fake Pilot
// sessions of internal/sim as their executor, and everything meets in one event store on
// virtual time.

// simExecutor runs a leased run as a fake Pilot session.
type simExecutor struct{ rt *sim.Runtime }

type simExecution struct {
	rt       *sim.Runtime
	session  string
	activity time.Time
	asked    string
}

func (e simExecutor) Start(ctx context.Context, l runner.Lease) (runner.Execution, error) {
	s, err := e.rt.Start(ctx, tower.PilotRequest{Flight: l.Flight, Org: l.Org, Project: l.Project, Goal: l.Goal,
		Base: l.Base, Generation: l.Generation})
	return &simExecution{rt: e.rt, session: s}, err
}

func (x *simExecution) Poll() runner.Update {
	rep, _ := x.rt.Poll(context.Background(), x.session)
	var u runner.Update
	if rep.Activity.After(x.activity) {
		x.activity = rep.Activity
		u.Events = append(u.Events, runner.Event{Kind: runner.KindActivity, Text: "working"})
	}
	switch rep.State {
	case tower.PilotAsking:
		if rep.Question != x.asked {
			x.asked = rep.Question
			u.Events = append(u.Events, runner.Event{Kind: runner.KindAsking, Text: rep.Question, Question: rep.Question})
		}
	case tower.PilotDone:
		u.Result = &runner.Result{Outcome: runner.OutcomeDone, PR: rep.PR}
	case tower.PilotFailed:
		u.Result = &runner.Result{Outcome: runner.OutcomeFailed, Reason: rep.Reason}
	case tower.PilotLost:
		u.Result = &runner.Result{Outcome: runner.OutcomeLost, Reason: "the session crashed"}
	}
	return u
}

func (x *simExecution) Stop() { _ = x.rt.Stop(context.Background(), x.session) }

type world struct {
	t       *testing.T
	sim     *sim.World
	dir     string
	store   *eventstore.Store
	broker  *runner.Broker
	tower   *tower.Tower
	runners []*runner.Runner
	silent  map[int]bool // runners that do not step: cut off from the daemon
	down    bool         // the daemon is down: no runner reaches it, and it expires nothing
	stopped time.Time    // when the daemon went down; the next open resumes leases from it
}

func newWorld(t *testing.T, runners int) *world {
	t.Helper()
	sw, err := sim.Open(context.Background(), t.TempDir(), 7, sim.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sw.Close() })
	sw.Forge.AddRepo("acme/web", "ci")
	w := &world{t: t, sim: sw, dir: t.TempDir(), silent: map[int]bool{}}
	w.open()
	for i := range runners {
		r, err := runner.New(runner.Config{ID: []string{"alpha", "bravo"}[i], Org: "acme", Protocol: socket{w},
			Executor: simExecutor{rt: sw.Runtime}, Now: sw.Clock.Now, TTL: 3 * time.Minute,
			Log: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		w.runners = append(w.runners, r)
	}
	return w
}

// open opens the daemon's side: the store, the broker and a Tower on them.
func (w *world) open() {
	w.t.Helper()
	reg, err := eventstore.NewRegistry(append(append(tower.Payloads(), policy.DecisionRecord{}), runner.Payloads()...)...)
	if err != nil {
		w.t.Fatal(err)
	}
	st, err := eventstore.Open(context.Background(), filepath.Join(w.dir, "lucy", "lucy.db"), eventstore.Options{
		Registry: reg, Projectors: []eventstore.Projector{runner.Projector{}, audit.Projector{}}, Now: w.sim.Clock.Now,
		SnapshotDir: filepath.Join(w.dir, "lucy", "state"),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { st.Close() })
	w.store, w.broker = st, runner.NewBroker(st, w.sim.Clock.Now)
	if _, err := w.broker.Resume(context.Background(), w.stopped); err != nil { // as the daemon does on start
		w.t.Fatal(err)
	}
	tw, err := tower.New(context.Background(), tower.Config{Store: st, Runtime: runner.Runtime{Broker: w.broker},
		Forge: w.sim.Forge, Clock: w.sim.Clock, ForgeIdentity: w.sim.Forge.Identity})
	if err != nil {
		w.t.Fatal(err)
	}
	w.tower = tw
}

// socket is the runners' connection to whichever daemon is up, as the unix socket is.
type socket struct{ w *world }

func (s socket) Queued(ctx context.Context, pl runner.Placement, org string) (int, error) {
	return s.w.broker.Queued(ctx, pl, org)
}

func (s socket) Lease(ctx context.Context, id string, p runner.Placement, org, proof string, ttl time.Duration) (runner.Lease, bool, error) {
	return s.w.broker.Lease(ctx, id, p, org, proof, ttl)
}

func (s socket) Heartbeat(ctx context.Context, id, run string, fence int64, ttl time.Duration, evs []runner.Event, acked string) (runner.Beat, error) {
	return s.w.broker.Heartbeat(ctx, id, run, fence, ttl, evs, acked)
}

func (s socket) Finish(ctx context.Context, id, run string, fence int64, res runner.Result) error {
	return s.w.broker.Finish(ctx, id, run, fence, res)
}

// restart is a daemon restart: the store, broker and Tower are reopened; runners and sessions
// carry on.
func (w *world) restart() {
	w.t.Helper()
	if err := w.store.Close(); err != nil {
		w.t.Fatal(err)
	}
	if w.stopped.IsZero() {
		w.stopped = w.sim.Clock.Now() // no outage
	}
	w.open()
	w.stopped = time.Time{}
}

func (w *world) dispatch(id string, steps ...sim.Step) {
	w.t.Helper()
	if len(steps) > 0 {
		w.sim.Runtime.Script(id, steps...)
	}
	if err := w.tower.Dispatch(context.Background(), tower.Spec{ID: id, Org: "acme", Project: "web", Goal: "fix it",
		Posture: policy.PostureGreen, Delivery: tower.DeliveryPullRequest, Acceptance: "the checks pass"}); err != nil {
		w.t.Fatal(err)
	}
}

// fly runs runners and the Tower each minute of virtual time until every flight is final.
func (w *world) fly(every func(minute int)) {
	w.t.Helper()
	ctx := context.Background()
	for minute := 0; minute < 24*60; minute++ {
		if every != nil {
			every(minute)
		}
		if w.down {
			w.sim.Clock.Advance(time.Minute)
			continue
		}
		for i, r := range w.runners {
			if !w.silent[i] {
				if err := r.Step(ctx); err != nil {
					w.t.Fatal(err)
				}
			}
		}
		if _, err := w.broker.Expire(ctx); err != nil {
			w.t.Fatal(err)
		}
		if err := w.tower.Step(ctx); err != nil {
			w.t.Fatal(err)
		}
		if _, active := w.tower.NextWake(); !active {
			return
		}
		w.sim.Clock.Advance(time.Minute)
	}
	w.t.Fatal("the flights did not finish in a virtual day")
}

func (w *world) flight(id string) tower.Status {
	w.t.Helper()
	s, ok := w.tower.Flight(id)
	if !ok {
		w.t.Fatalf("no flight %s", id)
	}
	return s
}

func (w *world) runs() []runner.RunInfo {
	w.t.Helper()
	rs, err := w.broker.Runs(context.Background(), 0, 100)
	if err != nil {
		w.t.Fatal(err)
	}
	return rs
}

func TestFlightLandsThroughTheProtocol(t *testing.T) {
	w := newWorld(t, 1)
	w.dispatch("f1")
	w.fly(nil)
	if s := w.flight("f1"); s.State != tower.StateTakeoff || s.Session != "f1#1" {
		t.Fatalf("flight %+v", s)
	}
	rs := w.runs()
	if len(rs) != 1 || rs[0].State != runner.StateDone || rs[0].Runner != "alpha" || rs[0].PR == 0 || rs[0].Accepted == 0 {
		t.Fatalf("runs %+v", rs)
	}
	if merges := w.sim.Forge.Merges(); len(merges) != 1 {
		t.Fatalf("merges %+v", merges)
	}
	if _, err := audit.VerifyStore(context.Background(), w.store); err != nil {
		t.Fatal(err)
	}
}

// A Pilot session that crashes is reported lost by its runner; the Tower starts the next
// generation, which runs under a new lease with a larger fence.
func TestCrashedPilotIsReplacedUnderANewFence(t *testing.T) {
	w := newWorld(t, 1)
	w.dispatch("f1", sim.Work(7*time.Minute), sim.Crash())
	w.sim.Runtime.Script("f1", sim.DefaultScript()...)
	w.fly(nil)
	if s := w.flight("f1"); s.State != tower.StateTakeoff || s.Generation != 2 {
		t.Fatalf("flight %+v", s)
	}
	rs := w.runs()
	if len(rs) != 2 || rs[0].State != runner.StateLost || rs[1].State != runner.StateDone || rs[1].Fence <= rs[0].Fence {
		t.Fatalf("runs %+v", rs)
	}
}

// A runner cut off from the daemon loses its lease. The Tower sees a lost Pilot and starts the
// next generation, which the other runner takes; when the first runner comes back, everything it
// sends under its old fence is refused, and the flight lands exactly once.
func TestCutOffRunnerIsFenced(t *testing.T) {
	w := newWorld(t, 2)
	w.dispatch("f1", sim.Work(2*time.Hour), sim.OpenPR(sim.Passing("ci", time.Minute)))
	w.sim.Runtime.Script("f1", sim.Work(20*time.Minute), sim.OpenPR(sim.Passing("ci", time.Minute)))
	var firstFence int64
	w.fly(func(minute int) {
		switch minute {
		case 0:
			w.silent[1] = true // bravo waits, so alpha takes the first run
		case 2:
			firstFence = w.runs()[0].Fence
			w.silent[0], w.silent[1] = true, false // alpha is cut off
		case 30:
			w.silent[0] = false // alpha comes back
		}
	})
	s := w.flight("f1")
	if s.State != tower.StateTakeoff || s.Generation != 2 {
		t.Fatalf("flight %+v", s)
	}
	rs := w.runs()
	if len(rs) != 2 || rs[0].Runner != "alpha" || rs[0].State != runner.StateLost || rs[0].Fence != firstFence ||
		rs[1].Runner != "bravo" || rs[1].State != runner.StateDone {
		t.Fatalf("runs %+v", rs)
	}
	if len(w.runners[0].Held()) != 0 {
		t.Fatal("the cut-off runner still holds its fenced run")
	}
	// Nothing alpha streamed after its lease expired reached the log.
	evs, err := w.store.ReadStream(context.Background(), runner.Stream("acme", "f1#1"), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	types := []string{}
	for _, e := range evs {
		types = append(types, e.Type)
	}
	if i := slices.Index(types, "run.expired"); i != len(types)-1 {
		t.Fatalf("events after the expiry: %v", types)
	}
	if merges := w.sim.Forge.Merges(); len(merges) != 1 {
		t.Fatalf("merges %+v", merges)
	}
}

// A daemon restart mid-flight keeps the lease: the runner carries on under the same fence and
// the flight lands with one run.
func TestDaemonRestartKeepsTheLease(t *testing.T) {
	w := newWorld(t, 1)
	w.dispatch("f1", sim.Work(30*time.Minute), sim.OpenPR(sim.Passing("ci", time.Minute)))
	w.fly(func(minute int) {
		if minute > 0 && minute%10 == 0 {
			w.restart()
		}
	})
	if s := w.flight("f1"); s.State != tower.StateTakeoff || s.Generation != 1 {
		t.Fatalf("flight %+v", s)
	}
	if rs := w.runs(); len(rs) != 1 || rs[0].State != runner.StateDone {
		t.Fatalf("runs %+v", rs)
	}
	if _, err := audit.VerifyStore(context.Background(), w.store); err != nil {
		t.Fatal(err)
	}
}

// The daemon is down for longer than a lease: when it starts again the runner, which kept the work
// going, reattaches under the same fence, and the flight lands with one run (LUCY-OPS-005).
func TestALongDaemonOutageKeepsTheRun(t *testing.T) {
	w := newWorld(t, 1)
	w.dispatch("f1", sim.Work(30*time.Minute), sim.OpenPR(sim.Passing("ci", time.Minute)))
	w.fly(func(minute int) {
		switch minute {
		case 5:
			w.down, w.stopped = true, w.sim.Clock.Now() // three-minute leases; down for five
		case 10:
			w.restart()
			w.down = false
			// The daemon's expiry timer and the Tower run before the runner reconnects.
			if _, err := w.broker.Expire(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := w.tower.Step(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	})
	if s := w.flight("f1"); s.State != tower.StateTakeoff || s.Generation != 1 {
		t.Fatalf("flight %+v", s)
	}
	rs := w.runs()
	if len(rs) != 1 || rs[0].State != runner.StateDone {
		t.Fatalf("runs %+v", rs)
	}
	evs, err := w.store.ReadStream(context.Background(), runner.Stream("acme", rs[0].Run), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(evs, func(e eventstore.Event) bool { return e.Type == "run.resumed" }) {
		t.Fatal("the restart resumed no lease")
	}
	if _, err := audit.VerifyStore(context.Background(), w.store); err != nil {
		t.Fatal(err)
	}
}

// A planned runner restart loses no work: the draining runner finishes the run it holds and
// leases nothing more, the flight dispatched meanwhile waits for the restarted runner, and both
// land with one Pilot generation each (LUCY-RUN-005).
func TestAPlannedRunnerRestartLosesNoWork(t *testing.T) {
	w := newWorld(t, 1)
	w.dispatch("f1", sim.Work(30*time.Minute), sim.OpenPR(sim.Passing("ci", time.Minute)))
	restarted := false
	w.fly(func(minute int) {
		old := w.runners[0]
		switch {
		case minute == 5:
			old.Drain()
		case minute == 6:
			w.dispatch("f2", sim.Work(20*time.Minute), sim.OpenPR(sim.Passing("ci", time.Minute)))
		case !restarted && old.Drained():
			r, err := runner.New(runner.Config{ID: "bravo", Org: "acme", Protocol: socket{w}, Executor: simExecutor{rt: w.sim.Runtime},
				Now: w.sim.Clock.Now, TTL: 3 * time.Minute, Log: slog.New(slog.DiscardHandler)})
			if err != nil {
				t.Fatal(err)
			}
			w.runners[0], restarted = r, true
		}
	})
	if !restarted {
		t.Fatal("the runner never drained")
	}
	for _, id := range []string{"f1", "f2"} {
		if s := w.flight(id); s.State != tower.StateTakeoff || s.Generation != 1 {
			t.Fatalf("flight %+v", s)
		}
	}
	rs := w.runs()
	if len(rs) != 2 {
		t.Fatalf("runs %+v", rs)
	}
	for _, r := range rs {
		want := map[string]string{"f1#1": "alpha", "f2#1": "bravo"}[r.Run]
		if r.State != runner.StateDone || r.Runner != want {
			t.Fatalf("run %+v; want done by %s", r, want)
		}
	}
}

// No runner: the run stays queued, the Pilot timeout stops it and the flight is grounded, and the
// stopped run is never leased.
func TestNoRunnerTimesOut(t *testing.T) {
	w := newWorld(t, 0)
	w.dispatch("f1")
	w.fly(nil)
	if s := w.flight("f1"); s.State != tower.StateGrounded || s.Reason != tower.GroundTimeout {
		t.Fatalf("flight %+v", s)
	}
	if rs := w.runs(); len(rs) != 1 || rs[0].State != runner.StateStopped {
		t.Fatalf("runs %+v", rs)
	}
}

// The daemon stopped after queueing a Pilot's run but before recording its start: the Tower
// starts the same generation again and gets the queued run back, not a refusal or a second run.
func TestStartIsIdempotentPerGeneration(t *testing.T) {
	w := newWorld(t, 0)
	rt := runner.Runtime{Broker: w.broker}
	req := tower.PilotRequest{Flight: "f1", Org: "acme", Project: "web", Goal: "g", Base: "main", Generation: 1}
	a, err := rt.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := rt.Start(context.Background(), req)
	if err != nil || a != b {
		t.Fatalf("second start: %q, %v; first %q", b, err, a)
	}
	if rs := w.runs(); len(rs) != 1 {
		t.Fatalf("runs %+v", rs)
	}
	other := req
	other.Org = "other"
	if _, err := rt.Start(context.Background(), other); !errors.Is(err, runner.ErrExists) || !errors.Is(err, tower.ErrStartRefused) {
		t.Fatalf("a run id taken in another org: %v, want a refusal the Tower grounds on", err)
	}
	// A request no retry would make acceptable is a refusal too: the Tower grounds the flight on it
	// rather than retrying it (LUCY-SPEC-008).
	if _, err := rt.Start(context.Background(), tower.PilotRequest{Flight: "f3", Org: "acme", Project: "web", Goal: "g", Base: "main", Generation: 2}); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]tower.PilotRequest{
		"a generation older than the flight's newest": {Flight: "f3", Org: "acme", Project: "web", Goal: "g", Base: "main", Generation: 1},
		"no goal": {Flight: "f2", Org: "acme", Project: "web", Base: "main", Generation: 1},
	} {
		if _, err := rt.Start(context.Background(), bad); !errors.Is(err, tower.ErrStartRefused) {
			t.Errorf("%s: %v, want tower.ErrStartRefused", name, err)
		}
	}
}
