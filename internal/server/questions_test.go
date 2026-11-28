package server

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/sim"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Without a hosted Tower no flight can be asking: the list is empty, and an answer is refused as
// unavailable rather than going anywhere else.
func TestQuestionsWithoutATower(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	list, err := f.client.Questions(ctx)
	if err != nil || list.Questions == nil || len(list.Questions) != 0 {
		t.Fatalf("questions %+v, %v", list, err)
	}
	_, err = f.client.Answer(ctx, api.AnswerParams{Flight: "f1", Answer: "yes"})
	wantCode(t, err, api.CodeUnavailable)
}

// The owner lists a Pilot's open question as the Tower recorded it and answers it through the
// API; the Tower relays the answer and records it as the owner's, by digest only.
func TestQuestionsThroughTheTower(t *testing.T) {
	w, err := sim.Open(context.Background(), shortTempDir(t), 7, sim.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	w.Forge.AddRepo("acme/web", "ci")
	if err := w.Dispatch(ctx, tower.Spec{ID: "f1", Org: "acme", Project: "web", Goal: "migrate", Posture: policy.PostureGreen, Delivery: tower.DeliveryPullRequest, Acceptance: "the checks pass"},
		sim.Ask("which account?"), sim.OpenPR(sim.Passing("ci", time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(ctx, sim.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	f := start(t, os.Geteuid(), 0, func(o *Options) { o.Questions = TowerQuestions{Tower: w.Tower, Lock: &mu} })

	list, err := f.client.Questions(ctx)
	if err != nil || len(list.Questions) != 1 {
		t.Fatalf("questions %+v, %v", list, err)
	}
	if q := list.Questions[0]; q.Flight != "f1" || q.Org != "acme" || q.Project != "web" || q.Question != "which account?" || q.Since.IsZero() {
		t.Fatalf("question %+v", q)
	}
	_, err = f.client.Answer(ctx, api.AnswerParams{Flight: "nope", Answer: "x"})
	if ae := wantCode(t, err, api.CodeInvalidParams); ae.Data.Field != "flight" {
		t.Fatalf("unknown flight: %+v", ae)
	}
	const answer = "the staging account, ref 7731"
	res, err := f.client.Answer(ctx, api.AnswerParams{Flight: "f1", Answer: answer})
	if err != nil || res.Flight != "f1" || res.State != string(tower.StateFlying) {
		t.Fatalf("answer %+v, %v", res, err)
	}
	if list, _ := f.client.Questions(ctx); len(list.Questions) != 0 {
		t.Fatalf("still listed after the answer: %+v", list)
	}
	_, err = f.client.Answer(ctx, api.AnswerParams{Flight: "f1", Answer: "again"})
	wantCode(t, err, api.CodeInvalidParams)

	// The Tower recorded the answer as the owner's, and neither log holds its text.
	log, err := w.Log(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answered := false
	for _, l := range log {
		answered = answered || (l.Type == "task.pilot_answered" && l.Actor == tower.ActorOwner)
		if strings.Contains(l.Payload, "ref 7731") {
			t.Errorf("tower log seq %d holds the answer", l.Seq)
		}
	}
	if !answered {
		t.Error("no task.pilot_answered by the owner")
	}
	evs, err := f.store.ReadAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, e := range evs {
		if strings.Contains(string(e.Payload), "ref 7731") {
			t.Errorf("daemon log seq %d holds the answer", e.Seq)
		}
		if e.Stream == CommandStream {
			c, err := eventstore.DecodeAs[CommandEvent](f.store.Registry(), e)
			if err != nil {
				t.Fatal(err)
			}
			methods = append(methods, c.Method)
		}
	}
	if strings.Count(strings.Join(methods, " "), "questions.answer") != 3 {
		t.Errorf("recorded commands %v: every answer call is a recorded command", methods)
	}
}

type proposingSupervisor struct{}

func (proposingSupervisor) ProposeAnswer(context.Context, tower.Escalation) (tower.Proposal, bool, error) {
	return tower.Proposal{By: "supervisor/test", Answer: "the staging account"}, true, nil
}

// A supervisor's proposal is shown with the question, never given to the Pilot; the owner may
// accept it in one step, naming the proposal and the question they saw, which is recorded as the
// owner's answer. A question or proposal that changed since the owner saw it is refused as stale,
// and nothing reaches the Pilot (LUCY-RUN-010, channel-05).
func TestAcceptingAProposal(t *testing.T) {
	w, err := sim.Open(context.Background(), shortTempDir(t), 7, sim.Options{Tower: tower.Config{Supervisor: proposingSupervisor{}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	w.Forge.AddRepo("acme/web", "ci")
	if err := w.Dispatch(ctx, tower.Spec{ID: "f1", Org: "acme", Project: "web", Goal: "migrate", Posture: policy.PostureGreen, Delivery: tower.DeliveryPullRequest, Acceptance: "the checks pass"},
		sim.Ask("which account?"), sim.Ask("which region?"), sim.Ask("which region?"), sim.OpenPR(sim.Passing("ci", time.Minute))); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	f := start(t, os.Geteuid(), 0, func(o *Options) { o.Questions = TowerQuestions{Tower: w.Tower, Lock: &mu} })
	shown := func() api.Question {
		t.Helper()
		if err := w.Run(ctx, sim.RunOptions{}); err != nil {
			t.Fatal(err)
		}
		list, err := f.client.Questions(ctx)
		if err != nil || len(list.Questions) != 1 {
			t.Fatalf("questions %+v, %v", list, err)
		}
		q := list.Questions[0]
		if q.ProposalID == "" || q.ProposedBy != "supervisor/test" || q.Proposed != "the staging account" || len(q.QuestionSHA256) != 64 {
			t.Fatalf("question %+v", q)
		}
		return q
	}
	pilotGot := func(want ...string) {
		t.Helper()
		var got []string
		for _, a := range w.Runtime.Answers {
			got = append(got, a...)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("the Pilot got %v, want %v", got, want)
		}
	}
	refused := func(p api.AnswerParams, field string) {
		t.Helper()
		_, err := f.client.Answer(ctx, p)
		if ae := wantCode(t, err, api.CodeInvalidParams); ae.Data.Field != field {
			t.Fatalf("%+v refused on %q, want %q", p, ae.Data.Field, field)
		}
	}

	q1 := shown()
	refused(api.AnswerParams{Flight: "f1", Answer: "x", AcceptProposal: q1.ProposalID, QuestionSHA256: q1.QuestionSHA256}, "answer")
	refused(api.AnswerParams{Flight: "f1", AcceptProposal: q1.ProposalID}, "question_sha256")
	refused(api.AnswerParams{Flight: "f1", AcceptProposal: q1.ProposalID, QuestionSHA256: q1.QuestionSHA256[:15]}, "question_sha256")
	refused(api.AnswerParams{Flight: "f1", Answer: "x", QuestionSHA256: q1.QuestionSHA256}, "question_sha256")
	if _, err := f.client.Answer(ctx, api.AnswerParams{Flight: "f1", Answer: "the dev account"}); err != nil {
		t.Fatal(err)
	}

	// The Pilot asks a new question and the supervisor proposes again: what the owner saw is stale.
	q2 := shown()
	if q2.ProposalID == q1.ProposalID || q2.QuestionSHA256 == q1.QuestionSHA256 {
		t.Fatalf("a new question kept the old ids: %+v after %+v", q2, q1)
	}
	refused(api.AnswerParams{Flight: "f1", AcceptProposal: q1.ProposalID, QuestionSHA256: q1.QuestionSHA256}, "accept_proposal")
	refused(api.AnswerParams{Flight: "f1", AcceptProposal: q1.ProposalID, QuestionSHA256: q2.QuestionSHA256}, "accept_proposal")
	refused(api.AnswerParams{Flight: "f1", AcceptProposal: q2.ProposalID, QuestionSHA256: q1.QuestionSHA256[:16]}, "accept_proposal")
	pilotGot("the dev account")
	if res, err := f.client.Answer(ctx, api.AnswerParams{Flight: "f1", AcceptProposal: q2.ProposalID, QuestionSHA256: q2.QuestionSHA256}); err != nil || res.State != string(tower.StateFlying) {
		t.Fatalf("accept: %+v, %v", res, err)
	}
	pilotGot("the dev account", "the staging account")

	// The same question asked again has a new proposal: the old id is stale, the new one, named
	// with the digest's first 16 hex characters, is accepted.
	q3 := shown()
	if q3.ProposalID == q2.ProposalID || q3.QuestionSHA256 != q2.QuestionSHA256 {
		t.Fatalf("the same question asked again: %+v after %+v", q3, q2)
	}
	refused(api.AnswerParams{Flight: "f1", AcceptProposal: q2.ProposalID, QuestionSHA256: q2.QuestionSHA256}, "accept_proposal")
	if _, err := f.client.Answer(ctx, api.AnswerParams{Flight: "f1", AcceptProposal: q3.ProposalID, QuestionSHA256: q3.QuestionSHA256[:16]}); err != nil {
		t.Fatal(err)
	}
	pilotGot("the dev account", "the staging account", "the staging account")
	log, err := w.Log(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range log {
		if l.Type == "task.pilot_answered" && l.Actor != tower.ActorOwner {
			t.Fatalf("an accepted proposal recorded as %q's answer, want the owner's", l.Actor)
		}
	}
	refused(api.AnswerParams{Flight: "f1", AcceptProposal: q3.ProposalID, QuestionSHA256: q3.QuestionSHA256}, "flight")
}
