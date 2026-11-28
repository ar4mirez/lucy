package sim

import (
	"context"
	"errors"
	"slices"
	"strings"
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
			if st.ProposalID == "" {
				t.Fatalf("the proposal has no id: %+v", st)
			}
			if err := w.Tower.Answer(ctx, "f1", st.Proposed); err != nil { // the owner accepts it
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

// The same question asked again, after the owner answered it, is a new question: the supervisor is
// asked about it and proposes again, with a new proposal id. A restart in between changes nothing
// the log records.
func TestTheSameQuestionAskedAgainIsProposedOnAgain(t *testing.T) {
	ctx := context.Background()
	var logs [2][]string
	for i, restart := range []bool{false, true} {
		s := &fakeSupervisor{answer: "eu-west"}
		w := supervisedWorld(t, 85, s)
		dispatch(t, w, spec("f1", policy.PostureGreen), Work(5*time.Minute), Ask("which region?"), Work(5*time.Minute),
			Ask("which region?"), OpenPR(Passing("ci", time.Minute)))
		run(t, w, RunOptions{})
		first := state(t, w, "f1")
		if first.State != tower.StateAsking || first.ProposalID == "" || len(s.asked) != 1 {
			t.Fatalf("restart %v: first ask %+v, asked %d times", restart, first, len(s.asked))
		}
		if err := w.Tower.Answer(ctx, "f1", "us-east"); err != nil {
			t.Fatal(err)
		}
		if restart {
			if err := w.Restart(ctx); err != nil {
				t.Fatal(err)
			}
		}
		run(t, w, RunOptions{})
		second := state(t, w, "f1")
		if second.State != tower.StateAsking || second.Question != first.Question || len(s.asked) != 2 {
			t.Fatalf("restart %v: second ask %+v, asked %d times; want the supervisor asked again", restart, second, len(s.asked))
		}
		if second.ProposalID == "" || second.ProposalID == first.ProposalID || second.Proposed != "eu-west" {
			t.Fatalf("restart %v: second proposal %q after %q", restart, second.ProposalID, first.ProposalID)
		}
		log, err := w.Log(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range log {
			if strings.HasSuffix(l.Stream, "/f1") {
				logs[i] = append(logs[i], l.Type)
			}
		}
	}
	if !slices.Equal(logs[0], logs[1]) {
		t.Fatalf("a restart changed the log:\n%v\n%v", logs[0], logs[1])
	}
	if n := strings.Count(strings.Join(logs[0], " "), "task.answer_proposed"); n != 2 {
		t.Fatalf("%d proposals recorded, want 2: %v", n, logs[0])
	}
}

// A proposal that arrives after its step, as a model supervisor's does, is recorded through
// Tower.Propose only for the question still open in the same session; a stale one is refused, and
// a recorded one still never reaches the Pilot (LUCY-RUN-010).
func TestALateProposalIsRecordedOnlyForTheOpenQuestion(t *testing.T) {
	ctx := context.Background()
	s := &fakeSupervisor{} // declines in the step: the proposal comes later
	w := supervisedWorld(t, 83, s)
	dispatch(t, w, spec("f1", policy.PostureGreen), askingScript()...)
	run(t, w, RunOptions{})
	if len(s.asked) != 1 {
		t.Fatalf("asked %d times", len(s.asked))
	}
	e := s.asked[0]
	if e.Session == "" || e.Question != "which region?" {
		t.Fatalf("the escalation %+v", e)
	}
	for name, stale := range map[string]tower.Escalation{
		"another question": {Flight: e.Flight, Session: e.Session, Question: "which zone?"},
		"another session":  {Flight: e.Flight, Session: e.Session + "x", Question: e.Question},
		"another flight":   {Flight: "f2", Session: e.Session, Question: e.Question},
	} {
		if err := w.Tower.Propose(ctx, stale, tower.Proposal{By: "supervisor/test", Answer: "eu-west"}); !errors.Is(err, tower.ErrProposalStale) {
			t.Errorf("%s: %v, want ErrProposalStale", name, err)
		}
	}
	if err := w.Tower.Propose(ctx, e, tower.Proposal{By: "someone", Answer: "eu-west"}); err == nil {
		t.Fatal("a proposal not named supervisor/ was recorded")
	}
	if err := w.Tower.Propose(ctx, e, tower.Proposal{By: "supervisor/test", Answer: "eu-west"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Tower.Propose(ctx, e, tower.Proposal{By: "supervisor/test", Answer: "us-east"}); !errors.Is(err, tower.ErrProposalStale) {
		t.Fatalf("a second proposal: %v", err)
	}
	run(t, w, RunOptions{})
	stepAfter(t, w, 24*time.Hour)
	if st := state(t, w, "f1"); st.State != tower.StateAsking || st.Proposed != "eu-west" {
		t.Fatalf("flight %+v", st)
	}
	for _, a := range w.Runtime.Answers {
		if len(a) > 0 {
			t.Fatalf("the Pilot got %v", a)
		}
	}
	if err := w.Tower.Answer(ctx, "f1", "us-east"); err != nil {
		t.Fatal(err)
	}
	if err := w.Tower.Propose(ctx, e, tower.Proposal{By: "supervisor/test", Answer: "eu-west"}); !errors.Is(err, tower.ErrProposalStale) {
		t.Fatalf("after the owner's answer: %v", err)
	}
}
