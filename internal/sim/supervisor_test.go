package sim

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// fakeSupervisor proposes a fixed answer, or declines, or fails, and counts what it is asked.
type fakeSupervisor struct {
	answer string
	err    error
	asked  []tower.Escalation
}

func (s *fakeSupervisor) ProposeAnswer(_ context.Context, q tower.Escalation) (tower.Proposal, bool, error) {
	s.asked = append(s.asked, q)
	if s.err != nil || s.answer == "" {
		return tower.Proposal{}, false, s.err
	}
	return tower.Proposal{By: "supervisor/test", Answer: s.answer}, true, nil
}

func supervisedWorld(t *testing.T, seed uint64, s tower.Supervisor) *World {
	t.Helper()
	w, err := Open(context.Background(), t.TempDir(), seed, Options{Tower: tower.Config{Supervisor: s}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() }) //nolint:errcheck // best effort in cleanup
	return w
}

func askingScript() []Step {
	return []Step{Work(5 * time.Minute), Ask("which region?"), Work(10 * time.Minute), OpenPR(Passing("ci", 3*time.Minute))}
}

// Under every posture a proposal is recorded and shown, but only the owner answers (channel-05,
// LUCY-RUN-010): the Pilot gets nothing until the owner does, and then the owner's answer, which
// may be the proposal, accepted, and is recorded as the owner's.
func TestOnlyTheOwnerAnswersUnderEveryPosture(t *testing.T) {
	for _, posture := range []policy.Posture{policy.PostureGreen, policy.PostureOwner, policy.PostureClient} {
		t.Run(string(posture), func(t *testing.T) {
			ctx := context.Background()
			s := &fakeSupervisor{answer: "eu-west"}
			w := supervisedWorld(t, 82, s)
			dispatch(t, w, spec("f1", posture), askingScript()...)
			run(t, w, RunOptions{})
			stepAfter(t, w, 24*time.Hour)
			st := state(t, w, "f1")
			if st.State != tower.StateAsking || st.ProposedBy != "supervisor/test" || st.Proposed != "eu-west" || len(s.asked) != 1 {
				t.Fatalf("flight %+v, asked %d times; want asking with the supervisor's proposal shown", st, len(s.asked))
			}
			for _, a := range w.Runtime.Answers {
				if len(a) > 0 {
					t.Fatalf("the Pilot got an answer no owner gave: %v", a)
				}
			}
			by, answer, ok := w.Tower.Proposal("f1")
			if !ok || by != "supervisor/test" {
				t.Fatalf("the proposal: %q %q %v", by, answer, ok)
			}
			if err := w.Tower.Answer(ctx, "f1", answer); err != nil { // the owner accepts it
				t.Fatal(err)
			}
			run(t, w, RunOptions{})
			var answers []string
			for _, a := range w.Runtime.Answers {
				answers = append(answers, a...)
			}
			if !slices.Equal(answers, []string{"eu-west"}) {
				t.Fatalf("the Pilot got %v", answers)
			}
			log, err := w.Log(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range log {
				if l.Type == "task.pilot_answered" && l.Actor != tower.ActorOwner {
					t.Fatalf("the answer is recorded as %q's, want the owner's", l.Actor)
				}
			}
		})
	}
}

// A supervisor that declines or fails leaves the question to the owner, asked once; the Tower's
// own question (a harness needing a login) is never put to it.
func TestADecliningOrFailingSupervisorLeavesItToTheOwner(t *testing.T) {
	for name, s := range map[string]*fakeSupervisor{"declines": {}, "fails": {err: errors.New("model unreachable")}} {
		t.Run(name, func(t *testing.T) {
			w := supervisedWorld(t, 83, s)
			dispatch(t, w, spec("f1", policy.PostureGreen), askingScript()...)
			run(t, w, RunOptions{})
			stepAfter(t, w, time.Hour)
			st := state(t, w, "f1")
			if st.State != tower.StateAsking || st.ProposedBy != "" || len(s.asked) != 1 {
				t.Fatalf("flight %+v, asked %d times", st, len(s.asked))
			}
		})
	}
	s := &fakeSupervisor{answer: "done"}
	w := supervisedWorld(t, 84, s)
	w.Runtime.Script("f1", NeedsLogin())
	dispatch(t, w, spec("f1", policy.PostureGreen))
	run(t, w, RunOptions{})
	if st := state(t, w, "f1"); st.State != tower.StateAsking || len(s.asked) != 0 {
		t.Fatalf("flight %+v; the Tower's own question was put to the supervisor %d times", st, len(s.asked))
	}
}
