package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// answering is a scripted Execution that takes answers.
type answering struct {
	scripted
	answers []string
}

func (a *answering) Answer(text string) error { a.answers = append(a.answers, text); return nil }

type answeringExecutor struct{ execs map[string]*answering }

func (e *answeringExecutor) Start(_ context.Context, l Lease) (Execution, error) {
	a := &answering{}
	e.execs[l.Run] = a
	return a, nil
}

// live is the Protocol on the fixture's current broker, so a test can restart the daemon under a
// runner.
type live struct{ f *fixture }

func (p live) Queued(ctx context.Context) (int, error) { return p.f.b.Queued(ctx) }
func (p live) Lease(ctx context.Context, r string, pl Placement, ttl time.Duration) (Lease, bool, error) {
	return p.f.b.Lease(ctx, r, pl, ttl)
}
func (p live) Heartbeat(ctx context.Context, r, run string, fence int64, ttl time.Duration, evs []Event) (Beat, error) {
	return p.f.b.Heartbeat(ctx, r, run, fence, ttl, evs)
}
func (p live) Finish(ctx context.Context, r, run string, fence int64, res Result) error {
	return p.f.b.Finish(ctx, r, run, fence, res)
}

// TestAnswerRelay: the Tower's answer to an asking run is recorded by its digest, handed to the
// run's runner once on its next heartbeat, and refused when the run is not asking.
func TestAnswerRelay(t *testing.T) {
	f := open(t)
	ctx := context.Background()
	f.submit("f1#1")
	l := f.lease("r1", 0)
	if err := f.b.Answer(ctx, "f1#1", "now"); !errors.Is(err, ErrNotAsking) {
		t.Fatalf("answer before a question = %v", err)
	}
	beat, err := f.b.Heartbeat(ctx, "r1", "f1#1", l.Fence, 0, []Event{{Seq: 1, Kind: KindAsking, Text: "which account?"}})
	if err != nil || !beat.Asking || beat.Answer != "" {
		t.Fatalf("beat %+v, %v", beat, err)
	}
	if err := f.b.Answer(ctx, "f1#1", strings.Repeat("x", MaxAnswer+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized answer = %v", err)
	}
	if err := f.b.Answer(ctx, "f1#1", "the staging account, ref 7731"); err != nil {
		t.Fatal(err)
	}
	if err := f.b.Answer(ctx, "f1#1", "again"); !errors.Is(err, ErrNotAsking) {
		t.Fatalf("second answer = %v", err)
	}
	if ri := f.run("f1#1"); ri.Asking {
		t.Fatalf("still asking after the answer: %+v", ri)
	}
	if _, err := f.b.Heartbeat(ctx, "r9", "f1#1", l.Fence, 0, nil); !errors.Is(err, ErrFenced) {
		t.Fatalf("another runner's heartbeat = %v", err)
	}
	f.submit("f1#2") // the answer is not anyone else's
	beat, err = f.b.Heartbeat(ctx, "r1", "f1#1", l.Fence, 0, nil)
	if err != nil || beat.Answer != "the staging account, ref 7731" || beat.Asking {
		t.Fatalf("beat %+v, %v", beat, err)
	}
	if beat, err = f.b.Heartbeat(ctx, "r1", "f1#1", l.Fence, 0, nil); err != nil || beat.Answer != "" {
		t.Fatalf("the answer was given twice: %+v, %v", beat, err)
	}
	evs, err := f.store.ReadAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if strings.Contains(string(e.Payload), "ref 7731") {
			t.Errorf("seq %d (%s) holds the answer's text", e.Seq, e.Type)
		}
	}
	if got := f.types("acme", "f1#1"); !strings.Contains(strings.Join(got, " "), "run.answered") {
		t.Errorf("no run.answered in %v", got)
	}
}

// TestRepeatedQuestionWhileAnswerWaits: a heartbeat that streams the question again while the
// owner's answer to it waits for the runner gets the answer, and the run stays not asking, so the
// Tower never asks the owner an answered question.
func TestRepeatedQuestionWhileAnswerWaits(t *testing.T) {
	f := open(t)
	ctx := context.Background()
	f.submit("f1#1")
	l := f.lease("r1", 0)
	ask := Event{Kind: KindAsking, Text: "which account?"}
	ask.Seq = 1
	if _, err := f.b.Heartbeat(ctx, "r1", "f1#1", l.Fence, 0, []Event{ask}); err != nil {
		t.Fatal(err)
	}
	if err := f.b.Answer(ctx, "f1#1", "staging"); err != nil {
		t.Fatal(err)
	}
	ask.Seq = 2
	beat, err := f.b.Heartbeat(ctx, "r1", "f1#1", l.Fence, 0, []Event{ask})
	if err != nil || beat.Answer != "staging" || beat.Asking || beat.Accepted != 2 {
		t.Fatalf("beat %+v, %v", beat, err)
	}
	if ri := f.run("f1#1"); ri.Asking {
		t.Fatalf("the answered question re-opened: %+v", ri)
	}
	if err := f.b.Answer(ctx, "f1#1", "again"); !errors.Is(err, ErrNotAsking) {
		t.Fatalf("a second answer = %v", err)
	}
}

// TestRunnerDeliversAndReasks: a runner gives the work the answer its heartbeat brings, and asks
// again when a daemon restart lost an answer the log says was given.
func TestRunnerDeliversAndReasks(t *testing.T) {
	f := open(t)
	ctx := context.Background()
	ex := &answeringExecutor{execs: map[string]*answering{}}
	r := newRunner(t, f, "r1", ex, live{f})
	f.submit("f1#1", "f2#1")
	step(t, r)
	r2 := newRunner(t, f, "r2", ex, live{f})
	step(t, r2)
	for _, run := range []string{"f1#1", "f2#1"} {
		ex.execs[run].queue = append(ex.execs[run].queue, Update{Events: []Event{{Kind: KindAsking, Text: "which account?"}}})
	}
	step(t, r)
	step(t, r2)

	// f1: answered and delivered on the next renewal.
	if err := f.b.Answer(ctx, "f1#1", "staging"); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(11 * time.Second)
	step(t, r)
	if got := ex.execs["f1#1"].answers; len(got) != 1 || got[0] != "staging" {
		t.Fatalf("f1 answers %q", got)
	}

	// f2: answered, then the daemon restarts before the runner's heartbeat collects it.
	if err := f.b.Answer(ctx, "f2#1", "production"); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	f.clock.Advance(11 * time.Second)
	step(t, r2) // no answer, and the broker no longer holds the question: ask again
	step(t, r2) // the second question is streamed
	if got := ex.execs["f2#1"].answers; len(got) != 0 {
		t.Fatalf("f2 got %q from a restarted daemon", got)
	}
	if ri := f.run("f2#1"); !ri.Asking || ri.Question != "which account?" {
		t.Fatalf("f2 is not asking again: %+v", ri)
	}
	if err := f.b.Answer(ctx, "f2#1", "production"); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(11 * time.Second)
	step(t, r2)
	if got := ex.execs["f2#1"].answers; len(got) != 1 || got[0] != "production" {
		t.Fatalf("f2 answers %q", got)
	}
}
