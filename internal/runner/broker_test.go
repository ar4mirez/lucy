package runner

import (
	"context"
	"errors"
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
	l, ok, err := f.b.Lease(context.Background(), runner, PlacementHost, "acme", ttl)
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
	if _, ok, err := f.b.Lease(ctx, "r1", PlacementHost, "acme", 0); ok || err != nil {
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
		"bad runner id":     second(f.b.Lease(ctx, "Runner One", PlacementHost, "acme", 0)),
		"bad placement":     second(f.b.Lease(ctx, "r1", "cloud", "acme", 0)),
		"too many events":   first(f.b.Heartbeat(ctx, "r1", "a#1", 1, 0, evs(1, MaxBatch+1), "")),
		"unknown kind":      first(f.b.Heartbeat(ctx, "r1", "a#1", 1, 0, []Event{{Seq: 1, Kind: "shout"}}, "")),
		"event 0":           first(f.b.Heartbeat(ctx, "r1", "a#1", 1, 0, []Event{{Seq: 0, Kind: KindOutput}}, "")),
		"unknown outcome":   f.b.Finish(ctx, "r1", "a#1", 1, Result{Outcome: "great"}),
		"PR on a failure":   f.b.Finish(ctx, "r1", "a#1", 1, Result{Outcome: OutcomeFailed, PR: 3}),
		"negative PR":       f.b.Finish(ctx, "r1", "a#1", 1, Result{Outcome: OutcomeDone, PR: -1}),
		"stop an unknown":   f.b.Stop(ctx, "nope"),
		"runner id length":  second(f.b.Lease(ctx, string(make([]byte, 64)), PlacementHost, "acme", 0)),
		"runner id leading": second(f.b.Lease(ctx, "-r", PlacementHost, "acme", 0)),
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

// A runner on the host leases the runs of exactly one org: it is never leased another org's run,
// which stays queued for that org's runner, and it is refused when it names no org, or names
// another org than that of a run it holds.
func TestAHostRunnerLeasesOneOrg(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	if err := f.b.Submit(ctx, Task{Run: "b#1", Org: "beta", Project: "web", Flight: "b", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	f.submit("a#1")
	if _, _, err := f.b.Lease(ctx, "r1", PlacementHost, "", 0); !errors.Is(err, ErrRunnerOrg) {
		t.Fatalf("a host lease without an org: %v, want ErrRunnerOrg", err)
	}
	if n, err := f.b.Queued(ctx, PlacementHost, "acme"); n != 1 || err != nil {
		t.Fatalf("acme's queued runs: %d, %v", n, err)
	}
	l, ok, err := f.b.Lease(ctx, "r1", PlacementHost, "acme", 0)
	if err != nil || !ok || l.Run != "a#1" || l.Org != "acme" {
		t.Fatalf("acme's runner leased %+v, %v, %v; want a#1, past beta's older run", l, ok, err)
	}
	if _, ok, err := f.b.Lease(ctx, "r1", PlacementHost, "acme", 0); ok || err != nil {
		t.Fatalf("acme's runner with only beta's run queued: ok %v, err %v", ok, err)
	}
	if f.run("b#1").State != StateQueued {
		t.Fatal("beta's run left the queue for acme's runner")
	}
	if _, _, err := f.b.Lease(ctx, "r1", PlacementHost, "beta", 0); !errors.Is(err, ErrRunnerOrg) {
		t.Fatalf("acme's runner, holding a#1, naming beta: %v, want ErrRunnerOrg", err)
	}
	if f.run("b#1").State != StateQueued {
		t.Fatal("a runner holding acme's run was leased beta's")
	}
	if l, ok, err := f.b.Lease(ctx, "r2", PlacementHost, "beta", 0); err != nil || !ok || l.Run != "b#1" {
		t.Fatalf("beta's runner leased %+v, %v, %v", l, ok, err)
	}
	if _, _, err := f.b.Lease(ctx, "r3", PlacementHost, "a/b", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an org that is not a name: %v, want ErrInvalid", err)
	}
}
