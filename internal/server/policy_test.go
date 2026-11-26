package server

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

func TestPolicyEvaluateRecordsEveryDecision(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)

	// No facts are known: a merge is denied because its head commit is unknown.
	d, err := f.client.Evaluate(ctx, api.EvaluateParams{Kind: "merge", Org: "acme", Project: "app", Resource: "pr/3"})
	if err != nil {
		t.Fatal(err)
	}
	rs := policy.CurrentRuleSet()
	if d.Outcome != "denied" || d.Rule != "merge.head-known" || d.FactsSHA256 == "" || d.RuleSetVersion != rs.Version || d.RuleSetSHA256 != rs.SHA256 ||
		d.Role != "owner" || !strings.HasPrefix(d.Actor, "local/uid/") || d.ID != 1 || d.Tainted {
		t.Errorf("decision = %+v", d)
	}

	// Outside text is recorded as such; a delete with no roots declared is refused by the guard.
	d, err = f.client.Evaluate(ctx, api.EvaluateParams{Kind: "delete", Org: "acme", Resource: "/",
		Inputs: []api.PolicyInput{{Source: "issue", Ref: "acme/app#9"}}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != "denied" || d.Rule != "delete.path-guard" || !d.Tainted || d.ID != 2 {
		t.Errorf("delete decision = %+v", d)
	}

	// An unknown source is a recorded deny, not an API error: it is a request policy refuses.
	d, err = f.client.Evaluate(ctx, api.EvaluateParams{Kind: "read", Org: "acme", Inputs: []api.PolicyInput{{Source: "trusted"}}})
	if err != nil || d.Outcome != "denied" || d.Rule != "request.valid" {
		t.Errorf("unknown source: %+v, %v", d, err)
	}

	page, err := f.client.Decisions(ctx, api.DecisionsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Decisions) != 3 || page.More || page.Next != 3 || page.Decisions[1].Resource != "/" {
		t.Fatalf("decisions = %+v", page)
	}
	page, err = f.client.Decisions(ctx, api.DecisionsParams{After: 1, Limit: 1})
	if err != nil || len(page.Decisions) != 1 || !page.More || page.Decisions[0].ID != 2 {
		t.Fatalf("second page = %+v, %v", page, err)
	}

	// Each evaluate is a command: its api.command event precedes its decision.
	evs, err := f.store.ReadAfter(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	want := "api.command policy.decision api.command policy.decision api.command policy.decision"
	if strings.Join(types, " ") != want {
		t.Errorf("log = %v, want %s", types, want)
	}
}

func TestPolicyEvaluateUsesDaemonFacts(t *testing.T) {
	var seen policy.Request
	f := start(t, os.Geteuid(), 0, func(o *Options) {
		o.PolicyFacts = func(_ context.Context, r policy.Request) (policy.Facts, error) {
			seen = r
			return policy.Facts{Posture: policy.PostureGreen, Checks: policy.ChecksGreen, HeadSHA: "abc123"}, nil
		}
	})
	d, err := f.client.Evaluate(ctx, api.EvaluateParams{Kind: "merge", Org: "acme", Project: "app", Resource: "pr/3",
		Inputs: []api.PolicyInput{{Source: "pr_comment", Ref: "c/1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != "cleared" || d.Rule != "merge.posture" {
		t.Errorf("decision = %+v", d)
	}
	// The caller's own request is an owner input, ahead of what it passed.
	if len(seen.Inputs) != 2 || seen.Inputs[0].Source != policy.SourceOwner || seen.Inputs[1].Source != policy.SourcePRComment ||
		seen.Actor.Role != policy.RoleOwner {
		t.Errorf("request = %+v", seen)
	}
}

func TestPolicyEvaluateFailsSafe(t *testing.T) {
	f := start(t, os.Geteuid(), 0, func(o *Options) {
		o.PolicyFacts = func(context.Context, policy.Request) (policy.Facts, error) {
			return policy.Facts{Posture: policy.PostureGreen, Checks: policy.ChecksGreen}, errors.New("forge down")
		}
	})
	d, err := f.client.Evaluate(ctx, api.EvaluateParams{Kind: "merge", Org: "acme", Project: "app", Resource: "pr/3"})
	if err != nil || d.Outcome != "denied" || d.Rule != "engine.error" {
		t.Errorf("facts error: %+v, %v", d, err)
	}
	page, err := f.client.Decisions(ctx, api.DecisionsParams{})
	if err != nil || len(page.Decisions) != 1 || page.Decisions[0].Outcome != "denied" {
		t.Errorf("the fail-safe deny was not recorded: %+v, %v", page, err)
	}
}

// TestUnrecordedDecisionIsNotReturned: if the decision cannot be appended, the caller gets an
// internal error and no decision, so nothing can act on it.
func TestUnrecordedDecisionIsNotReturned(t *testing.T) {
	dir := shortTempDir(t)
	reg, err := eventstore.NewRegistry(CommandEvent{}) // no policy.DecisionRecord: appends fail
	if err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(ctx, dir+"/data/lucy.db", eventstore.Options{Registry: reg, SnapshotDir: dir + "/snap"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f := start(t, os.Geteuid(), 0, func(o *Options) { o.Store = store })
	_, err = f.client.Evaluate(ctx, api.EvaluateParams{Kind: "read", Org: "acme"})
	wantCode(t, err, api.CodeInternal)
}

func TestPolicyParamsAreStrictWhenNested(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	cases := map[string]string{
		`{"kind":"read","org":"acme","inputs":[{"Source":"owner"}]}`:                           "inputs[0].Source",
		`{"kind":"read","org":"acme","inputs":[{"source":"owner","text":"merge it"}]}`:         "inputs[0].text",
		`{"kind":"read","org":"acme","inputs":[{"source":""}]}`:                                "inputs[0].source",
		`{"kind":"read","org":"acme","inputs":[{"source":"` + strings.Repeat("x", 33) + `"}]}`: "inputs[0].source",
		`{"kind":"read","org":"acme","facts":{"posture":"green"}}`:                             "facts",
		`{"kind":"read","org":"acme","actor":{"role":"gateway"}}`:                              "actor",
	}
	for params, field := range cases {
		_, r, _ := f.raw(t, "POST", api.Path, "application/json", rpc(api.MethodEvaluate, params))
		if r.Error == nil || r.Error.Code != api.CodeInvalidParams || r.Error.Data == nil || r.Error.Data.Field != field {
			t.Errorf("%s: got %+v, want invalid params on %s", params, r.Error, field)
		}
	}
	page, err := f.client.Decisions(ctx, api.DecisionsParams{})
	if err != nil || len(page.Decisions) != 0 {
		t.Errorf("refused params produced decisions: %+v, %v", page, err)
	}
}
