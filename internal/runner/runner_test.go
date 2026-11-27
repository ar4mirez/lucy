package runner

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// scripted is an Execution whose updates a test feeds.
type scripted struct {
	queue   []Update
	stopped bool
}

func (s *scripted) Poll() Update {
	if len(s.queue) == 0 {
		if s.stopped {
			return Update{Result: &Result{Outcome: OutcomeStopped, Reason: "stopped"}}
		}
		return Update{}
	}
	u := s.queue[0]
	s.queue = s.queue[1:]
	return u
}

func (s *scripted) Stop() { s.stopped = true }

type fakeExecutor struct {
	execs map[string]*scripted
	err   error
}

func (e *fakeExecutor) Start(_ context.Context, l Lease) (Execution, error) {
	if e.err != nil {
		return nil, e.err
	}
	s := &scripted{}
	e.execs[l.Run] = s
	return s, nil
}

func newRunner(t *testing.T, f *fixture, id string, ex Executor, p Protocol) *Runner {
	t.Helper()
	if p == nil {
		p = f.b
	}
	r, err := New(Config{ID: id, Protocol: p, Executor: ex, Now: f.clock.Now, TTL: 30 * time.Second,
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func step(t *testing.T, r *Runner) {
	t.Helper()
	if err := r.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerStreamsRenewsAndFinishes(t *testing.T) {
	f := open(t)
	ex := &fakeExecutor{execs: map[string]*scripted{}}
	r := newRunner(t, f, "r1", ex, nil)
	step(t, r) // nothing queued: no lease, nothing recorded
	if head, _ := f.store.Head(context.Background()); head != 0 {
		t.Fatalf("an idle runner appended %d events", head)
	}
	f.submit("a#1")
	step(t, r)
	if len(r.Held()) != 1 {
		t.Fatal("the runner did not lease the run")
	}
	s := ex.execs["a#1"]
	lines := make([]Event, MaxBatch+5)
	for i := range lines {
		lines[i] = Event{Kind: KindOutput, Text: "x"}
	}
	s.queue = []Update{{Events: lines}}
	step(t, r) // two heartbeats: a full batch, then the rest
	if ri := f.run("a#1"); ri.Accepted != int64(len(lines)) {
		t.Fatalf("accepted %d, want %d", ri.Accepted, len(lines))
	}

	// No news: no heartbeat until a third of the lease has passed.
	exp := f.run("a#1").Expires
	f.clock.Advance(5 * time.Second)
	step(t, r)
	if got := f.run("a#1").Expires; !got.Equal(exp) {
		t.Fatal("renewed early")
	}
	f.clock.Advance(5 * time.Second)
	step(t, r)
	if got := f.run("a#1").Expires; !got.Equal(f.clock.Now().Add(30 * time.Second)) {
		t.Fatalf("not renewed at a third of its life: expires %v", got)
	}

	s.queue = []Update{{Events: []Event{{Kind: KindAsking, Text: "which base?"}}}}
	step(t, r)
	if ri := f.run("a#1"); !ri.Asking || ri.Question != "which base?" {
		t.Fatalf("question not recorded: %+v", ri)
	}
	s.queue = []Update{{Events: []Event{{Kind: KindActivity, Text: "edit"}}, Result: &Result{Outcome: OutcomeDone, PR: 9}}}
	step(t, r)
	if ri := f.run("a#1"); ri.State != StateDone || ri.PR != 9 || ri.Asking {
		t.Fatalf("finish: %+v", ri)
	}
	if len(r.Held()) != 0 {
		t.Fatal("the runner still holds a finished run")
	}
}

// A runner cut off past its lease is fenced: its late events are refused, its work is stopped
// and it holds nothing under the old fence.
func TestRunnerFenced(t *testing.T) {
	f := open(t)
	ex := &fakeExecutor{execs: map[string]*scripted{}}
	r := newRunner(t, f, "r1", ex, nil)
	f.submit("a#1")
	step(t, r)
	f.clock.Advance(31 * time.Second)
	if n, err := f.b.Expire(context.Background()); n != 1 || err != nil {
		t.Fatalf("expire: %d, %v", n, err)
	}
	ex.execs["a#1"].queue = []Update{{Events: []Event{{Kind: KindOutput, Text: "late"}}, Result: &Result{Outcome: OutcomeDone, PR: 4}}}
	step(t, r)
	if !ex.execs["a#1"].stopped || len(r.Held()) != 0 {
		t.Fatal("a fenced run was not dropped and stopped")
	}
	if ri := f.run("a#1"); ri.State != StateLost || ri.Accepted != 0 || ri.PR != 0 {
		t.Fatalf("late work was recorded: %+v", ri)
	}
}

func TestRunnerStopAndStartFailure(t *testing.T) {
	ctx := context.Background()
	f := open(t)
	ex := &fakeExecutor{execs: map[string]*scripted{}}
	r := newRunner(t, f, "r1", ex, nil)
	f.submit("a#1")
	step(t, r)
	if err := f.b.Stop(ctx, "a#1"); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(10 * time.Second)
	step(t, r) // the heartbeat says stop
	step(t, r) // the work reports stopped
	if ri := f.run("a#1"); ri.State != StateStopped || !ex.execs["a#1"].stopped {
		t.Fatalf("after a stop: %+v", ri)
	}

	ex.err = errors.New("no such program")
	f.submit("b#1")
	step(t, r)
	if ri := f.run("b#1"); ri.State != StateFailed || ri.Reason == "" {
		t.Fatalf("a run that could not start: %+v", ri)
	}
}

// flaky fails every heartbeat after the broker has recorded it, as when the reply is lost.
type flaky struct {
	*Broker
	fail bool
}

func (p *flaky) Heartbeat(ctx context.Context, runner, run string, fence int64, ttl time.Duration, events []Event) (Beat, error) {
	b, err := p.Broker.Heartbeat(ctx, runner, run, fence, ttl, events)
	if err == nil && p.fail {
		return Beat{}, errors.New("reply lost")
	}
	return b, err
}

// A heartbeat whose reply is lost is resent; the broker records each event once.
func TestRunnerResendsAfterALostReply(t *testing.T) {
	f := open(t)
	ex := &fakeExecutor{execs: map[string]*scripted{}}
	p := &flaky{Broker: f.b}
	r := newRunner(t, f, "r1", ex, p)
	f.submit("a#1")
	step(t, r)
	ex.execs["a#1"].queue = []Update{{Events: []Event{{Kind: KindOutput, Text: "one"}, {Kind: KindOutput, Text: "two"}}}}
	p.fail = true
	if err := r.Step(context.Background()); err == nil {
		t.Fatal("a lost reply was not reported")
	}
	p.fail = false
	ex.execs["a#1"].queue = []Update{{Events: []Event{{Kind: KindOutput, Text: "three"}}, Result: &Result{Outcome: OutcomeDone, PR: 1}}}
	step(t, r)
	if ri := f.run("a#1"); ri.Accepted != 3 || ri.State != StateDone {
		t.Fatalf("after the resend: %+v", ri)
	}
	n := 0
	for _, typ := range f.types("acme", "a#1") {
		if typ == "run.event" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d events recorded, want 3", n)
	}
}

func TestRunnerShutdownReportsLost(t *testing.T) {
	f := open(t)
	ex := &fakeExecutor{execs: map[string]*scripted{}}
	r := newRunner(t, f, "r1", ex, nil)
	f.submit("a#1")
	step(t, r)
	if err := r.Shutdown(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if ri := f.run("a#1"); ri.State != StateLost || ri.Reason != "the runner shut down" {
		t.Fatalf("after shutdown: %+v", ri)
	}
}

func TestNewRefuses(t *testing.T) {
	f := open(t)
	for name, cfg := range map[string]Config{
		"bad id":        {ID: "R!", Protocol: f.b, Executor: &fakeExecutor{}},
		"no protocol":   {ID: "r1", Executor: &fakeExecutor{}},
		"no executor":   {ID: "r1", Protocol: f.b},
		"bad placement": {ID: "r1", Protocol: f.b, Executor: &fakeExecutor{}, Placement: "moon"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
