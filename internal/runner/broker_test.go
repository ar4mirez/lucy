package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/ar4mirez/lucy/internal/harness"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/audit"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// clock is a settable clock for the broker and the store.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	t     *testing.T
	dir   string
	clock *clock
	store *eventstore.Store
	b     *Broker
}

func open(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir(), clock: newClock()}
	f.reopen()
	return f
}

// reopen closes the store, if open, and opens it again: a daemon restart.
func (f *fixture) reopen() {
	f.t.Helper()
	if f.store != nil {
		if err := f.store.Close(); err != nil {
			f.t.Fatal(err)
		}
	}
	reg, err := eventstore.NewRegistry(append(Payloads(), policy.DecisionRecord{})...)
	if err != nil {
		f.t.Fatal(err)
	}
	st, err := eventstore.Open(context.Background(), filepath.Join(f.dir, "lucy", "lucy.db"), eventstore.Options{
		Registry: reg, Projectors: []eventstore.Projector{Projector{}, audit.Projector{}}, Now: f.clock.Now,
		SnapshotDir: filepath.Join(f.dir, "lucy", "state"),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { st.Close() })
	f.store, f.b = st, NewBroker(st, f.clock.Now)
}

func task(run string) Task {
	return Task{Run: run, Org: "acme", Project: "web", Flight: run, Generation: 1, Goal: "fix the build"}
}

func (f *fixture) submit(runs ...string) {
	f.t.Helper()
	for _, r := range runs {
		if err := f.b.Submit(context.Background(), task(r)); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) lease(runner string, ttl time.Duration) Lease {
	f.t.Helper()
	l, ok, err := f.b.Lease(context.Background(), runner, PlacementHost, "acme", "", ttl)
	if err != nil || !ok {
		f.t.Fatalf("lease: ok %v, err %v", ok, err)
	}
	return l
}

func (f *fixture) run(id string) RunInfo {
	f.t.Helper()
	ri, err := f.b.Run(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return ri
}

// types returns the event types of a run's stream.
func (f *fixture) types(org, run string) []string {
	f.t.Helper()
	evs, err := f.store.ReadStream(context.Background(), Stream(org, run), 0, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func evs(from, to int64) []Event {
	var out []Event
	for i := from; i <= to; i++ {
		out = append(out, Event{Seq: i, Kind: KindOutput, Text: "line"})
	}
	return out
}

func TestLeaseHeartbeatFinish(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	if _, ok, err := f.b.Lease(ctx, "r1", PlacementHost, "acme", "", 0); ok || err != nil {
		t.Fatalf("lease from an empty queue: ok %v, err %v", ok, err)
	}
	f.submit("a#1", "b#1")
	if n, err := f.b.Queued(ctx, PlacementHost, ""); n != 2 || err != nil {
		t.Fatalf("queued %d, %v", n, err)
	}
	la := f.lease("r1", 0)
	lb := f.lease("r2", 0)
	if la.Run != "a#1" || lb.Run != "b#1" {
		t.Fatalf("leased %s then %s, want the oldest first", la.Run, lb.Run)
	}
	if la.Fence <= 0 || lb.Fence <= la.Fence {
		t.Fatalf("fences %d then %d, want increasing", la.Fence, lb.Fence)
	}
	if want := f.clock.Now().Add(DefaultTTL); !la.Expires.Equal(want) {
		t.Fatalf("expires %v, want %v", la.Expires, want)
	}

	f.clock.Advance(10 * time.Second)
	beat, err := f.b.Heartbeat(ctx, "r1", "a#1", la.Fence, 20*time.Second, evs(1, 3), "")
	if err != nil || beat.Accepted != 3 || beat.Stop || !beat.Expires.Equal(f.clock.Now().Add(20*time.Second)) {
		t.Fatalf("heartbeat: %+v, %v", beat, err)
	}
	// A batch resent after a lost reply records only what is new.
	if beat, err = f.b.Heartbeat(ctx, "r1", "a#1", la.Fence, 0, evs(2, 4), ""); err != nil || beat.Accepted != 4 {
		t.Fatalf("resent heartbeat: %+v, %v", beat, err)
	}
	if _, err := f.b.Heartbeat(ctx, "r1", "a#1", la.Fence, 0, evs(6, 6), ""); !errors.Is(err, ErrSequence) {
		t.Fatalf("a gap: %v", err)
	}
	if err := f.b.Finish(ctx, "r1", "a#1", la.Fence, Result{Outcome: OutcomeDone, PR: 12}); err != nil {
		t.Fatal(err)
	}
	ri := f.run("a#1")
	if ri.State != StateDone || ri.PR != 12 || ri.Accepted != 4 || ri.Runner != "r1" {
		t.Fatalf("run: %+v", ri)
	}
	want := []string{"run.queued", "run.leased", "run.event", "run.event", "run.event", "run.renewed", "run.event", "run.renewed", "run.finished"}
	if got := f.types("acme", "a#1"); !equal(got, want) {
		t.Fatalf("stream %v, want %v", got, want)
	}
	// Nothing more under a finished lease.
	if _, err := f.b.Heartbeat(ctx, "r1", "a#1", la.Fence, 0, nil, ""); !errors.Is(err, ErrFenced) {
		t.Fatalf("heartbeat after finish: %v", err)
	}
	if err := f.b.Finish(ctx, "r1", "a#1", la.Fence, Result{Outcome: OutcomeFailed}); !errors.Is(err, ErrFenced) {
		t.Fatalf("finish twice: %v", err)
	}
}

func TestFencing(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	f.submit("a#1")
	l := f.lease("r1", 10*time.Second)
	for name, call := range map[string]func() error{
		"another runner": func() error { _, err := f.b.Heartbeat(ctx, "r2", l.Run, l.Fence, 0, nil, ""); return err },
		"another fence":  func() error { _, err := f.b.Heartbeat(ctx, "r1", l.Run, l.Fence+1, 0, nil, ""); return err },
		"finish by another": func() error {
			return f.b.Finish(ctx, "r2", l.Run, l.Fence, Result{Outcome: OutcomeDone})
		},
	} {
		if err := call(); !errors.Is(err, ErrFenced) {
			t.Errorf("%s: %v, want fenced", name, err)
		}
	}
	if _, err := f.b.Heartbeat(ctx, "r1", "nope", 1, 0, nil, ""); !errors.Is(err, ErrNoRun) {
		t.Errorf("unknown run: %v", err)
	}

	// The lease expires: the late heartbeat is refused, and the expiry it found is recorded.
	f.clock.Advance(10 * time.Second)
	if _, err := f.b.Heartbeat(ctx, "r1", l.Run, l.Fence, 0, evs(1, 2), ""); !errors.Is(err, ErrFenced) {
		t.Fatalf("heartbeat after expiry: %v", err)
	}
	ri := f.run(l.Run)
	if ri.State != StateLost || ri.Accepted != 0 {
		t.Fatalf("after expiry: %+v", ri)
	}
	if err := f.b.Finish(ctx, "r1", l.Run, l.Fence, Result{Outcome: OutcomeDone, PR: 3}); !errors.Is(err, ErrFenced) {
		t.Fatalf("finish after expiry: %v", err)
	}
	if got, want := f.types("acme", l.Run), []string{"run.queued", "run.leased", "run.expired"}; !equal(got, want) {
		t.Fatalf("stream %v, want %v", got, want)
	}
	// The next generation runs under a larger fence.
	if err := f.b.Submit(ctx, Task{Run: "a#2", Org: "acme", Project: "web", Flight: "a", Generation: 2, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if l2 := f.lease("r2", 0); l2.Fence <= l.Fence || l2.Generation != 2 {
		t.Fatalf("second lease %+v after fence %d", l2, l.Fence)
	}
}

func TestExpireAndStop(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	f.submit("a#1", "b#1", "c#1", "d#1")
	la, lb := f.lease("r1", 10*time.Second), f.lease("r1", time.Minute)
	f.clock.Advance(10 * time.Second)
	if n, err := f.b.Expire(ctx); n != 1 || err != nil {
		t.Fatalf("expired %d, %v; want only the short lease", n, err)
	}
	if n, err := f.b.Expire(ctx); n != 0 || err != nil {
		t.Fatalf("expired again %d, %v", n, err)
	}
	if f.run(la.Run).State != StateLost || f.run(lb.Run).State != StateLeased {
		t.Fatal("wrong run expired")
	}

	// A leased run learns of a stop on its heartbeat; a queued one stops at once.
	if err := f.b.Stop(ctx, lb.Run); err != nil {
		t.Fatal(err)
	}
	if err := f.b.Stop(ctx, lb.Run); err != nil {
		t.Fatal(err)
	}
	beat, err := f.b.Heartbeat(ctx, "r1", lb.Run, lb.Fence, 0, nil, "")
	if err != nil || !beat.Stop {
		t.Fatalf("heartbeat after stop: %+v, %v", beat, err)
	}
	if err := f.b.Finish(ctx, "r1", lb.Run, lb.Fence, Result{Outcome: OutcomeStopped}); err != nil {
		t.Fatal(err)
	}
	if err := f.b.Stop(ctx, "c#1"); err != nil {
		t.Fatal(err)
	}
	if ri := f.run("c#1"); ri.State != StateStopped {
		t.Fatalf("queued run after stop: %+v", ri)
	}
	if err := f.b.Stop(ctx, "c#1"); err != nil {
		t.Fatal("stopping a finished run:", err)
	}
	if l := f.lease("r2", 0); l.Run != "d#1" {
		t.Fatalf("leased %s, want d#1: a stopped run is not leased", l.Run)
	}
	if got := len(f.types("acme", lb.Run)); got != 5 { // queued, leased, stop_requested, renewed, finished
		t.Fatalf("a second stop request was recorded: %v", f.types("acme", lb.Run))
	}
}

// A daemon restart keeps every lease: the same fence renews, and the projection rebuilt from the
// log equals the live one.
func TestRestartKeepsLeases(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	f.submit("a#1", "b#1")
	l := f.lease("r1", 0)
	if _, err := f.b.Heartbeat(ctx, "r1", l.Run, l.Fence, 0, evs(1, 2), ""); err != nil {
		t.Fatal(err)
	}
	before := f.run(l.Run)
	f.reopen()
	if after := f.run(l.Run); after != before {
		t.Fatalf("after restart %+v, before %+v", after, before)
	}
	if beat, err := f.b.Heartbeat(ctx, "r1", l.Run, l.Fence, 0, evs(1, 3), ""); err != nil || beat.Accepted != 3 {
		t.Fatalf("heartbeat after restart: %+v, %v", beat, err)
	}
	live, err := f.b.Runs(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Rebuild(ctx, Projector{}.Name()); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := f.b.Runs(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 || len(rebuilt) != 2 || live[0] != rebuilt[0] || live[1] != rebuilt[1] {
		t.Fatalf("rebuilt %+v, live %+v", rebuilt, live)
	}
	if _, err := audit.VerifyStore(ctx, f.store); err != nil {
		t.Fatal(err)
	}
}

// A daemon that was down for longer than a lease moves every lease still live when it stopped later
// by the outage, at least DefaultTTL from now, before it expires anything: a runner that kept
// working renews under its old fence, one that is gone is expired when its moved lease ends, a
// lease that had run out before the daemon stopped is not moved, and neither a restart with no
// outage nor another restart before the runner renews moves anything (LUCY-OPS-005, LUCY-RUN-005).
func TestResumeAfterAnOutage(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	t0 := f.clock.Now()
	f.submit("a#1", "b#1", "c#1", "d#1", "e#1")
	a, b, c := f.lease("r1", 0), f.lease("r2", 0), f.lease("r3", MaxTTL)
	d, e := f.lease("r4", MinTTL), f.lease("r5", 3*time.Minute)
	if _, err := f.b.Heartbeat(ctx, "r1", a.Run, a.Fence, 0, evs(1, 1), ""); err != nil {
		t.Fatal(err)
	}
	earned := f.run(a.Run).Expires
	f.clock.Advance(10 * time.Second) // d's lease runs out; then the daemon stops
	stopped := f.clock.Now()
	f.clock.Advance(140 * time.Second) // down past a's, b's and e's leases, not c's
	f.reopen()
	n, err := f.b.Resume(ctx, stopped)
	if err != nil || n != 3 {
		t.Fatalf("resumed %d, %v; want a, b and e", n, err)
	}
	floor := t0.Add(150*time.Second + DefaultTTL) // now plus a lease life
	for run, want := range map[string]time.Time{a.Run: floor, b.Run: floor, e.Run: e.Expires.Add(140 * time.Second), c.Run: c.Expires, d.Run: d.Expires} {
		if ri := f.run(run); !ri.Expires.Equal(want) {
			t.Fatalf("%s expires %v, want %v", run, ri.Expires, want)
		}
	}
	if ri := f.run(a.Run); !ri.Earned.Equal(earned) || ri.Fence != a.Fence {
		t.Fatalf("a resumed lease keeps what the runner earned and its fence: %+v", ri)
	}
	if n, err := f.b.Expire(ctx); err != nil || n != 1 || f.run(d.Run).State != StateLost {
		t.Fatalf("expired %d, %v; want only d, which ran out before the daemon stopped", n, err)
	}

	f.clock.Advance(30 * time.Second)
	f.reopen()
	if n, err := f.b.Resume(ctx, f.clock.Now()); err != nil || n != 0 {
		t.Fatalf("a restart with no outage: %d, %v", n, err)
	}
	if n, err := f.b.Resume(ctx, f.clock.Now().Add(-10*time.Second)); err != nil || n != 0 {
		t.Fatalf("another outage before the runners renewed: %d, %v", n, err)
	}

	beat, err := f.b.Heartbeat(ctx, "r1", a.Run, a.Fence, MaxTTL, evs(1, 2), "")
	if err != nil || beat.Accepted != 2 {
		t.Fatalf("reattach under the old fence: %+v, %v", beat, err)
	}
	if ri := f.run(a.Run); !ri.Earned.Equal(beat.Expires) || !ri.Expires.Equal(beat.Expires) {
		t.Fatalf("renewed lease %+v", ri)
	}
	f.clock.Advance(floor.Sub(f.clock.Now())) // b's moved lease ends; a, c and e run longer
	if n, err := f.b.Expire(ctx); err != nil || n != 1 || f.run(b.Run).State != StateLost {
		t.Fatalf("expired %d, %v; want b", n, err)
	}
	if err := first(f.b.Heartbeat(ctx, "r2", b.Run, b.Fence, 0, nil, "")); !errors.Is(err, ErrFenced) {
		t.Fatalf("heartbeat of an expired lease: %v", err)
	}
	if got, want := f.types("acme", a.Run), []string{"run.queued", "run.leased", "run.event", "run.renewed", "run.resumed",
		"run.event", "run.renewed"}; !equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}

	live, err := f.b.Runs(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Rebuild(ctx, Projector{}.Name()); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := f.b.Runs(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := range live {
		if live[i] != rebuilt[i] {
			t.Fatalf("rebuilt %+v, live %+v", rebuilt[i], live[i])
		}
	}
	if _, err := audit.VerifyStore(ctx, f.store); err != nil {
		t.Fatal(err)
	}
}

// A newer Pilot generation fences the flight's live run before it is queued, in one transaction:
// the flight never has two live runs, the old runner is refused from then on, and a generation that
// is not newer than the flight's newest is refused (LUCY-RUN-004).
func TestANewerGenerationFencesTheOldRun(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	g1 := Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}
	if err := f.b.Submit(ctx, g1); err != nil {
		t.Fatal(err)
	}
	old := f.lease("r1", 0)
	g2 := g1
	g2.Run, g2.Generation = "f1#2", 2
	if err := f.b.Submit(ctx, g2); err != nil {
		t.Fatal(err)
	}
	if ri := f.run(old.Run); ri.State != StateLost || ri.Reason != "replaced by f1#2" {
		t.Fatalf("the replaced run %+v", ri)
	}
	if err := first(f.b.Heartbeat(ctx, "r1", old.Run, old.Fence, 0, evs(1, 1), "")); !errors.Is(err, ErrFenced) {
		t.Fatalf("the old runner's heartbeat: %v", err)
	}
	if err := f.b.Finish(ctx, "r1", old.Run, old.Fence, Result{Outcome: OutcomeDone, PR: 7}); !errors.Is(err, ErrFenced) {
		t.Fatalf("the old runner's finish: %v", err)
	}
	if got, want := f.types("acme", old.Run), []string{"run.queued", "run.leased", "run.replaced"}; !equal(got, want) {
		t.Fatalf("the old run's events %v, want %v", got, want)
	}

	// A queued generation is replaced too, and another org's flight of the same name is untouched.
	other := Task{Run: "beta-f1#1", Org: "beta", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}
	if err := f.b.Submit(ctx, other); err != nil {
		t.Fatal(err)
	}
	g3 := g1
	g3.Run, g3.Generation = "f1#3", 3
	if err := f.b.Submit(ctx, g3); err != nil {
		t.Fatal(err)
	}
	if ri := f.run("f1#2"); ri.State != StateLost || ri.Fence != 0 {
		t.Fatalf("the replaced queued run %+v", ri)
	}
	if ri := f.run(other.Run); ri.State != StateQueued {
		t.Fatalf("another org's flight %+v", ri)
	}

	for _, g := range []int{2, 3} { // older than the newest, and the newest itself under a new id
		stale := g1
		stale.Run, stale.Generation = fmt.Sprintf("f1-again#%d", g), g
		if err := f.b.Submit(ctx, stale); !errors.Is(err, ErrStale) {
			t.Fatalf("generation %d after 3: %v", g, err)
		}
	}
	live := 0
	runs, err := f.b.Runs(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, ri := range runs {
		if ri.Org == "acme" && ri.Flight == "f1" && !ri.State.Final() {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("%d live runs of f1: %+v", live, runs)
	}
	if err := f.store.Rebuild(ctx, Projector{}.Name()); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := f.b.Runs(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := range runs {
		if runs[i] != rebuilt[i] {
			t.Fatalf("rebuilt %+v, live %+v", rebuilt[i], runs[i])
		}
	}
}

func TestRefusals(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	f.submit("a#1")
	for name, err := range map[string]error{
		"duplicate run":     f.b.Submit(ctx, task("a#1")),
		"no goal":           f.b.Submit(ctx, Task{Run: "x#1", Org: "acme", Project: "web", Flight: "x", Generation: 1}),
		"generation 0":      f.b.Submit(ctx, Task{Run: "x#0", Org: "acme", Project: "web", Flight: "x", Goal: "g"}),
		"slash in org":      f.b.Submit(ctx, Task{Run: "x#1", Org: "a/b", Project: "web", Flight: "x", Generation: 1, Goal: "g"}),
		"slash in run":      f.b.Submit(ctx, Task{Run: "x/1", Org: "acme", Project: "web", Flight: "x", Generation: 1, Goal: "g"}),
		"bad runner id":     second(f.b.Lease(ctx, "Runner One", PlacementHost, "acme", "", 0)),
		"bad placement":     second(f.b.Lease(ctx, "r1", "cloud", "acme", "", 0)),
		"too many events":   first(f.b.Heartbeat(ctx, "r1", "a#1", 1, 0, evs(1, MaxBatch+1), "")),
		"unknown kind":      first(f.b.Heartbeat(ctx, "r1", "a#1", 1, 0, []Event{{Seq: 1, Kind: "shout"}}, "")),
		"event 0":           first(f.b.Heartbeat(ctx, "r1", "a#1", 1, 0, []Event{{Seq: 0, Kind: KindOutput}}, "")),
		"unknown outcome":   f.b.Finish(ctx, "r1", "a#1", 1, Result{Outcome: "great"}),
		"PR on a failure":   f.b.Finish(ctx, "r1", "a#1", 1, Result{Outcome: OutcomeFailed, PR: 3}),
		"negative PR":       f.b.Finish(ctx, "r1", "a#1", 1, Result{Outcome: OutcomeDone, PR: -1}),
		"stop an unknown":   f.b.Stop(ctx, "nope"),
		"runner id length":  second(f.b.Lease(ctx, string(make([]byte, 64)), PlacementHost, "acme", "", 0)),
		"runner id leading": second(f.b.Lease(ctx, "-r", PlacementHost, "acme", "", 0)),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if f.run("a#1").State != StateQueued {
		t.Fatal("a refused call changed the run")
	}
	// The lease life is clamped.
	l := f.lease("r1", time.Second)
	if got := l.Expires.Sub(f.clock.Now()); got != MinTTL {
		t.Fatalf("a 1 s lease lives %v, want %v", got, MinTTL)
	}
}

func TestCapText(t *testing.T) {
	long := string(make([]byte, MaxTextBytes-1)) + "é"
	if got := capText(long); len(got) != MaxTextBytes-1 {
		t.Fatalf("capText split a rune: %d bytes", len(got))
	}
	if got := capText("short"); got != "short" {
		t.Fatal(got)
	}
}

func first[T any](_ T, err error) error          { return err }
func second[T, U any](_ T, _ U, err error) error { return err }

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Host placement serves one org: the first host lease sets it, recorded; a host lease for another
// org is refused and that org's runs stay queued; the owner changes it only while no host run is
// live; it survives a daemon restart; and container leases of other orgs are unaffected.
func TestHostPlacementServesOneOrg(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	sub := func(run, org string) {
		t.Helper()
		if err := f.b.Submit(ctx, Task{Run: run, Org: org, Project: "web", Flight: run, Generation: 1, Goal: "g"}); err != nil {
			t.Fatal(err)
		}
	}
	sub("b#1", "beta")
	sub("a#1", "acme")
	if _, _, err := f.b.Lease(ctx, "r1", PlacementHost, "", "", 0); !errors.Is(err, ErrRunnerOrg) {
		t.Fatalf("a host lease without an org: %v, want ErrRunnerOrg", err)
	}
	if org, err := f.b.HostOrg(ctx); org != "" || err != nil {
		t.Fatalf("host org before any host lease: %q, %v", org, err)
	}
	l, ok, err := f.b.Lease(ctx, "r1", PlacementHost, "acme", "", 0)
	if err != nil || !ok || l.Run != "a#1" {
		t.Fatalf("acme's runner leased %+v, %v, %v; want a#1, past beta's older run", l, ok, err)
	}
	if org, _ := f.b.HostOrg(ctx); org != "acme" {
		t.Fatalf("host org after acme's lease: %q", org)
	}
	if _, _, err := f.b.Lease(ctx, "r2", PlacementHost, "beta", "", 0); !errors.Is(err, ErrHostOrg) {
		t.Fatalf("beta's host runner: %v, want ErrHostOrg", err)
	}
	if f.run("b#1").State != StateQueued {
		t.Fatal("beta's run left the queue")
	}
	if err := f.b.SetHostOrg(ctx, "beta", "owner"); !errors.Is(err, ErrHostOrg) {
		t.Fatalf("switching while acme's host run is live: %v, want ErrHostOrg", err)
	}
	if err := f.b.Finish(ctx, "r1", "a#1", l.Fence, Result{Outcome: OutcomeDone}); err != nil {
		t.Fatal(err)
	}
	if err := f.b.SetHostOrg(ctx, "beta", "owner"); err != nil {
		t.Fatalf("switching with no host run live: %v", err)
	}
	f.reopen()
	if org, err := f.b.HostOrg(ctx); org != "beta" || err != nil {
		t.Fatalf("host org after a restart: %q, %v", org, err)
	}
	if _, _, err := f.b.Lease(ctx, "r1", PlacementHost, "acme", "", 0); !errors.Is(err, ErrHostOrg) {
		t.Fatalf("acme's host runner after the switch: %v, want ErrHostOrg", err)
	}
	if l, ok, err := f.b.Lease(ctx, "r2", PlacementHost, "beta", "", 0); err != nil || !ok || l.Run != "b#1" {
		t.Fatalf("beta's runner leased %+v, %v, %v", l, ok, err)
	}
	sub("c#1", "gamma")
	f.b.VerifyPlacementWith(testPlacement{})
	if l, ok, err := f.b.Lease(ctx, "box", PlacementContainer, "gamma", "test-proof", 0); err != nil || !ok || l.Run != "c#1" {
		t.Fatalf("a container lease of another org: %+v, %v, %v", l, ok, err)
	}
	if err := f.b.SetHostOrg(ctx, "a/b", "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an org that is not a name: %v, want ErrInvalid", err)
	}
}

// A placement a runner declares is never trusted: without a PlacementVerifier every container
// lease is refused, and with one only a proof it accepts leases; every lease names its org.
func TestContainerPlacementNeedsAProof(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	f.submit("a#1")
	for _, proof := range []string{"", "test-proof"} {
		if _, _, err := f.b.Lease(ctx, "box", PlacementContainer, "acme", proof, 0); !errors.Is(err, ErrPlacementProof) {
			t.Fatalf("a container lease with no verifier, proof %q: %v, want ErrPlacementProof", proof, err)
		}
	}
	f.b.VerifyPlacementWith(testPlacement{})
	if _, _, err := f.b.Lease(ctx, "box", PlacementContainer, "acme", "forged", 0); !errors.Is(err, ErrPlacementProof) {
		t.Fatalf("a container lease with a forged proof: %v, want ErrPlacementProof", err)
	}
	if _, _, err := f.b.Lease(ctx, "box", PlacementContainer, "", "test-proof", 0); !errors.Is(err, ErrRunnerOrg) {
		t.Fatalf("a container lease without an org: %v, want ErrRunnerOrg", err)
	}
	if f.run("a#1").State != StateQueued {
		t.Fatal("a refused lease took the run")
	}
	if l, ok, err := f.b.Lease(ctx, "box", PlacementContainer, "acme", "test-proof", 0); err != nil || !ok || l.Run != "a#1" {
		t.Fatalf("a proven container lease: %+v, %v, %v", l, ok, err)
	}
	if org, _ := f.b.HostOrg(ctx); org != "" {
		t.Fatalf("a container lease set the host org to %q", org)
	}
	if _, err := New(Config{ID: "box", Placement: PlacementContainer, Protocol: f.b, Executor: &fakeExecutor{}}); !errors.Is(err, ErrRunnerOrg) {
		t.Fatalf("a runner without an org: %v, want ErrRunnerOrg", err)
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

// A run whose harness needs the owner finishes as needs_owner, naming a known need, which is the
// run's reason (LUCY-RUN-008). Only a needs_owner run names a need, and only a known one.
func TestFinishNeedsOwner(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	if err := f.b.Submit(ctx, Task{Run: "a#1", Org: "acme", Project: "web", Flight: "a", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	l, ok, err := f.b.Lease(ctx, "r1", PlacementHost, "acme", "", 0)
	if err != nil || !ok {
		t.Fatalf("lease: %v, %v", ok, err)
	}
	for _, bad := range []Result{
		{Outcome: OutcomeNeedsOwner},
		{Outcome: OutcomeNeedsOwner, Need: "a root shell"},
		{Outcome: OutcomeFailed, Need: harness.NeedLogin},
	} {
		if err := f.b.Finish(ctx, "r1", l.Run, l.Fence, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: %v, want ErrInvalid", bad, err)
		}
	}
	if err := f.b.Finish(ctx, "r1", l.Run, l.Fence, Result{Outcome: OutcomeNeedsOwner, Need: harness.NeedLogin, Reason: "ignored text"}); err != nil {
		t.Fatal(err)
	}
	if ri := f.run("a#1"); ri.State != StateNeedsOwner || ri.Reason != harness.NeedLogin || !ri.State.Final() {
		t.Fatalf("run %+v", ri)
	}
}
