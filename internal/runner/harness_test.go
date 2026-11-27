package runner_test

import (
	"context"
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
// broker and the runner's heartbeat; and the log keeps the answer's digest, never its text.
func TestPilotReachesTheOwnerOnlyThroughTheTower(t *testing.T) {
	w := newWorld(t, 0)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Abs(filepath.Join("..", "harness", "claudecode", "testdata", claudecode.Pinned, "ask.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	r, err := runner.New(runner.Config{ID: "local", Protocol: socket{w}, Now: w.sim.Clock.Now, TTL: 3 * time.Minute,
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
		if err := r.Step(ctx); err != nil {
			t.Fatal(err)
		}
		if err := w.tower.Step(ctx); err != nil {
			t.Fatal(err)
		}
		if answered && !w.runs()[0].Asking && w.runs()[0].Accepted == 3 {
			// the answer waits for the runner's next renewal, at a third of its lease: move time
			// on until the runner has collected it, and not past, so the Pilot timeout stays away
			w.sim.Clock.Advance(20 * time.Second)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := w.flight("f1"); !answered || st.PR != 7 {
		t.Fatalf("answered %v, flight %+v", answered, st)
	}
	evs, err := w.store.ReadAfter(ctx, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		if strings.Contains(string(e.Payload), answer) {
			t.Errorf("seq %d (%s) holds the answer's text", e.Seq, e.Type)
		}
		types = append(types, e.Type)
	}
	for _, want := range []string{"task.pilot_asked", "task.pilot_answered", "run.answered"} {
		if !strings.Contains(strings.Join(types, " "), want) {
			t.Errorf("no %s event in %v", want, types)
		}
	}
}
