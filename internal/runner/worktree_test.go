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

// slotting is preparing with runner.slot: it reports state to each poll and records when it came.
type slotting struct {
	*preparing
	now   func() time.Time
	polls []time.Time
}

func (s *slotting) Slot(_ context.Context, _, _ string, _ int64) (string, string, error) {
	s.polls = append(s.polls, s.now())
	return s.state, "", nil
}

// While a run's worktree is prepared the runner polls runner.slot on a backoff from SlotPollMin
// doubling to SlotPollMax, without heartbeating beyond the lease's schedule, and starts the work at
// the first poll that reports it ready.
func TestWaitingOnAWorktreePollsItsSlot(t *testing.T) {
	f := open(t)
	if err := f.b.Submit(context.Background(), task("f1#1")); err != nil {
		t.Fatal(err)
	}
	s := &slotting{preparing: &preparing{Broker: f.b, deadline: f.clock.Now().Add(time.Hour), state: WorktreePreparing}, now: f.clock.Now}
	ex := &fakeExecutor{execs: map[string]*scripted{}}
	r := newRunner(t, f, "r1", ex, s)
	tick := 50 * time.Millisecond
	for range int(21 * time.Second / tick) { // past the heartbeat at 20 s, before the one at 30 s
		step(t, r)
		f.clock.Advance(tick)
	}
	want := []time.Duration{SlotPollMin, 2 * SlotPollMin, 4 * SlotPollMin, 8 * SlotPollMin, 16 * SlotPollMin, SlotPollMax, SlotPollMax}
	if len(s.polls) < len(want)+1 {
		t.Fatalf("%d polls in 21 s", len(s.polls))
	}
	for i, w := range want {
		if gap := s.polls[i+1].Sub(s.polls[i]); gap < w || gap > w+tick {
			t.Fatalf("poll %d came %v after the one before; want %v", i+1, gap, w)
		}
	}
	if len(ex.execs) != 0 || s.beats > 21/10+1 {
		t.Fatalf("started %d, %d heartbeats while waiting", len(ex.execs), s.beats)
	}
	s.state = WorktreeReady
	for len(ex.execs) == 0 {
		if f.clock.Now().Sub(s.polls[len(s.polls)-1]) > SlotPollMax+tick {
			t.Fatal("the ready run did not start at its next poll")
		}
		step(t, r)
		f.clock.Advance(tick)
	}
	if started := f.clock.Now().Add(-tick); !s.polls[len(s.polls)-1].Equal(started) {
		t.Fatalf("the run started at %v, not at its last slot poll %v", started, s.polls[len(s.polls)-1])
	}
}
