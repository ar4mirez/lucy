package runner_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/harness/claudecode"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/sim"
	"github.com/ar4mirez/lucy/internal/tower"
)

// TestPilotReachesTheOwnerOnlyThroughTheTower flies a flight whose Pilot is a recorded Claude Code
// session that asks a question (LUCY-RUN-002, LUCY-RUN-013). The question reaches the owner only
// as the Tower's record; the owner's answer reaches the session only through the Tower, the
// broker and the runner's heartbeat; and the log keeps the answer's digest, never its text. While
// the question waits the harness writes to its standard error and sends a message the adapter does
// not know, each repeating the question, and the reply that first carries the answer is lost; the
// owner is still asked once, and the session takes the answer once.
func TestPilotReachesTheOwnerOnlyThroughTheTower(t *testing.T) {
	w := newWorld(t, 0)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(filepath.Join("..", "harness", "claudecode", "testdata", claudecode.Pinned, "ask.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(recorded)), "\n") {
		lines = append(lines, l)
		if strings.Contains(l, `"can_use_tool"`) { // after the question's permission prompt
			lines = append(lines, `{"stderr":"warning: the network is slow"}`, `{"out":{"type":"mystery"}}`)
		}
	}
	path := filepath.Join(t.TempDir(), "ask-busy.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	p := &loseOneAnswer{socket: socket{w}}
	r, err := runner.New(runner.Config{ID: "local", Protocol: p, Now: w.sim.Clock.Now, TTL: 3 * time.Minute,
		Executor: &runner.ProcessExecutor{Root: filepath.Join(t.TempDir(), "runner"), Home: home, Command: []string{exe},
			Harness: claudecode.Adapter{}, StopGrace: time.Second},
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	for w.sim.Forge.OpenPR("acme/web", "pilot", "main", []sim.Check{sim.Passing("ci", time.Minute)}, w.sim.Clock.Now()) < 7 {
	}
	const answer = "use the staging account"
	ctx := context.Background()
	if err := w.tower.Dispatch(ctx, tower.Spec{ID: "f1", Org: "acme", Project: "web", Goal: "harness - " + path,
		Posture: policy.PostureOwner}); err != nil {
		t.Fatal(err)
	}
	answered := false
	deadline := time.Now().Add(30 * time.Second)
	for st := w.flight("f1"); st.State != tower.StateReview; st = w.flight("f1") {
		if time.Now().After(deadline) || st.State.Final() {
			t.Fatalf("flight %+v, runs %+v", st, w.runs())
		}
		if st.State == tower.StateAsking && !answered {
			if st.Question != "Which account should the migration run against? (staging | production)" {
				t.Fatalf("question %q", st.Question)
			}
			if err := w.tower.Answer(ctx, "f1", answer); err != nil {
				t.Fatal(err)
			}
			answered = true
		}
		if err := r.Step(ctx); err != nil && !errors.Is(err, errReplyLost) {
			t.Fatal(err)
		}
		if err := w.tower.Step(ctx); err != nil {
			t.Fatal(err)
		}
		if answered && !w.runs()[0].Asking && w.runs()[0].Accepted <= 7 { // the events before the session has its answer
			// the answer waits for the runner's next renewal, at a third of its lease: move time
			// on until the runner has collected it, and not past, so the Pilot timeout stays away
			w.sim.Clock.Advance(20 * time.Second)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := w.flight("f1"); !answered || st.PR != 7 || !p.lost {
		t.Fatalf("answered %v, reply lost %v, flight %+v", answered, p.lost, st)
	}
	if ri := w.runs()[0]; ri.Asking {
		t.Fatalf("the run ended asking: %+v", ri)
	}
	evs, err := w.store.ReadAfter(ctx, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	asked := 0
	for _, e := range evs {
		if e.Type == "task.pilot_asked" {
			asked++
		}
		if strings.Contains(string(e.Payload), answer) {
			t.Errorf("seq %d (%s) holds the answer's text", e.Seq, e.Type)
		}
		types = append(types, e.Type)
	}
	if asked != 1 {
		t.Errorf("the owner was asked %d times about one question", asked)
	}
	for _, want := range []string{"task.pilot_asked", "task.pilot_answered", "run.answered"} {
		if !strings.Contains(strings.Join(types, " "), want) {
			t.Errorf("no %s event in %v", want, types)
		}
	}
}

var errReplyLost = errors.New("reply lost")

// loseOneAnswer loses the first heartbeat reply that carries an answer, after the broker recorded
// the heartbeat.
type loseOneAnswer struct {
	socket
	lost bool
}

func (p *loseOneAnswer) Heartbeat(ctx context.Context, id, run string, fence int64, ttl time.Duration, evs []runner.Event, acked string) (runner.Beat, error) {
	b, err := p.socket.Heartbeat(ctx, id, run, fence, ttl, evs, acked)
	if err == nil && b.Answer != "" && !p.lost {
		p.lost = true
		return runner.Beat{}, errReplyLost
	}
	return b, err
}
