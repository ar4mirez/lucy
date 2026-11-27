package runner

import (
	"context"
	"testing"
	"time"
)

// preparing is the broker as the daemon's API serves it to a host run whose project names a
// repository: each lease carries a worktree still being prepared, until deadline, and each
// heartbeat reports state.
type preparing struct {
	*Broker
	deadline time.Time
	state    string
	beats    int
}

func (p *preparing) Lease(ctx context.Context, runner string, placement Placement, org, proof string, ttl time.Duration) (Lease, bool, error) {
	l, ok, err := p.Broker.Lease(ctx, runner, placement, org, proof, ttl)
	l.Worktree, l.Branch, l.WorktreeState, l.WorktreeDeadline = "/pool/acme/web/slot-1", "lucy/x", WorktreePreparing, p.deadline
	return l, ok, err
}

func (p *preparing) Heartbeat(ctx context.Context, runner, run string, fence int64, ttl time.Duration, events []Event, acked string) (Beat, error) {
	p.beats++
	b, err := p.Broker.Heartbeat(ctx, runner, run, fence, ttl, events, acked)
	b.WorktreeState = p.state
	return b, err
}

func (f *fixture) renewals(run string) int {
	f.t.Helper()
	evs, err := f.store.ReadStream(context.Background(), Stream("acme", run), 0, 10000)
	if err != nil {
		f.t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == (Renewed{}).EventType() {
			n++
		}
	}
	return n
}

// A run waiting on its worktree heartbeats only on the lease's usual schedule, a third of its
// life, however many steps it waits through; it starts once a heartbeat reports the worktree
// ready.
func TestWaitingOnAWorktreeAddsNoRenewals(t *testing.T) {
	f := open(t)
	if err := f.b.Submit(context.Background(), task("f1#1")); err != nil {
		t.Fatal(err)
	}
	p := &preparing{Broker: f.b, deadline: f.clock.Now().Add(time.Hour), state: WorktreePreparing}
	ex := &fakeExecutor{execs: map[string]*scripted{}}
	r := newRunner(t, f, "r1", ex, p)
	for range 600 { // ten minutes, a step a second; the lease lives 30 s
		step(t, r)
		f.clock.Advance(time.Second)
	}
	if len(ex.execs) != 0 {
		t.Fatal("the work started before its worktree was ready")
	}
	if n, most := f.renewals("f1#1"), 600/10+1; n > most || p.beats > most {
		t.Fatalf("%d renewals in %d heartbeats while waiting; the schedule allows %d", n, p.beats, most)
	}
	p.state = WorktreeReady
	for range 11 {
		step(t, r)
		f.clock.Advance(time.Second)
	}
	if l := r.Held(); len(ex.execs) != 1 || len(l) != 1 || l[0].WorktreeState != WorktreeReady {
		t.Fatalf("the ready run did not start: %+v", l)
	}
}

// A run whose worktree is still preparing a while past its deadline is failed by the runner.
func TestWaitingOnAWorktreeEndsPastItsDeadline(t *testing.T) {
	f := open(t)
	if err := f.b.Submit(context.Background(), task("f1#1")); err != nil {
		t.Fatal(err)
	}
	p := &preparing{Broker: f.b, deadline: f.clock.Now().Add(2 * time.Minute), state: WorktreePreparing}
	ex := &fakeExecutor{execs: map[string]*scripted{}}
	r := newRunner(t, f, "r1", ex, p)
	for range int((2*time.Minute + WorktreeGrace) / time.Second) {
		step(t, r)
		f.clock.Advance(time.Second)
	}
	if len(r.Held()) != 1 {
		t.Fatal("the run was failed before its deadline and grace")
	}
	f.clock.Advance(2 * time.Second)
	step(t, r)
	ri, err := f.b.Run(context.Background(), "f1#1")
	if err != nil || ri.State != StateFailed || ri.Reason != "the run's worktree was still being prepared past its deadline" || len(ex.execs) != 0 {
		t.Fatalf("run %+v, %v; started %d", ri, err, len(ex.execs))
	}
}
