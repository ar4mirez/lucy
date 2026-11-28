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
// accept it in one step, which is recorded as the owner's answer; with no proposal, accepting is
// refused (LUCY-RUN-010, channel-05).
func TestAcceptingAProposal(t *testing.T) {
	w, err := sim.Open(context.Background(), shortTempDir(t), 7, sim.Options{Tower: tower.Config{Supervisor: proposingSupervisor{}}})
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
	if err != nil || len(list.Questions) != 1 || list.Questions[0].ProposedBy != "supervisor/test" || list.Questions[0].Proposed != "the staging account" {
		t.Fatalf("questions %+v, %v", list, err)
	}
	_, err = f.client.Answer(ctx, api.AnswerParams{Flight: "f1", Answer: "x", AcceptProposal: true})
	wantCode(t, err, api.CodeInvalidParams)
	if res, err := f.client.Answer(ctx, api.AnswerParams{Flight: "f1", AcceptProposal: true}); err != nil || res.State != string(tower.StateFlying) {
		t.Fatalf("accept: %+v, %v", res, err)
	}
	var got []string
	for _, a := range w.Runtime.Answers {
		got = append(got, a...)
	}
	if len(got) != 1 || got[0] != "the staging account" {
		t.Fatalf("the Pilot got %v", got)
	}
	log, err := w.Log(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range log {
		if l.Type == "task.pilot_answered" && l.Actor != tower.ActorOwner {
			t.Fatalf("an accepted proposal recorded as %q's answer, want the owner's", l.Actor)
		}
	}
	_, err = f.client.Answer(ctx, api.AnswerParams{Flight: "f1", AcceptProposal: true})
	wantCode(t, err, api.CodeInvalidParams)
}
