package supervise_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/spec"
	"github.com/ar4mirez/lucy/internal/supervise"
	"github.com/ar4mirez/lucy/internal/tower"
)

// The oracle is LUCY-RUN-010 and docs/runbooks/supervisor.md: rules come before any model; the built-in safety
// rules always send a question about a secret, a credential, a payment or an irreversible or
// production change to the owner, never to a model, and the org's rules can only add to them; the
// org's ordered rules either propose a fixed answer (supervisor/rules) or send the question to the
// owner; rules spend no tokens and apply without --supervise.

func ask(q string) tower.Escalation {
	e := question
	e.Question = q
	return e
}

func declined(t *testing.T, f *fixture) []string {
	t.Helper()
	evs, err := f.store.ReadAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Type == "supervisor.declined" {
			var d supervise.Declined
			if err := json.Unmarshal(e.Payload, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d.Reason)
		}
	}
	return out
}

func withRules(f *fixture, models bool, rules ...supervise.Rule) {
	c := supervise.Config{Org: "acme", Rules: rules}
	if models {
		c.Route, c.Small, c.Large, c.DailyTokens = "models", "small-1", "large-1", 1000
	}
	f.sup.SetOrgs(map[string]supervise.Config{"acme": c})
}

func rule(match, propose string) supervise.Rule {
	return supervise.Rule{Match: regexp.MustCompile("(?i)" + match), Propose: propose, Owner: propose == ""}
}

func TestSafetyRulesSendSensitiveQuestionsToTheOwner(t *testing.T) {
	for q, about := range map[string]string{
		"What is the database password?":                     "a secret or a credential",
		"Can you give me the API key for the forge?":         "a secret or a credential",
		"Should I log in with the shared account?":           "a secret or a credential",
		"Which credit card should the purchase go on?":       "a payment",
		"Shall I pay the invoice?":                           "a payment",
		"OK to deploy this to production?":                   "an irreversible or production change",
		"Can I force-push the branch?":                       "an irreversible or production change",
		"Should I delete the old migrations?":                "an irreversible or production change",
		"Run rm -rf on the build cache?":                     "an irreversible or production change",
		"staging or production?":                             "an irreversible or production change",
		"What value should I set for AWS_SECRET_ACCESS_KEY?": "a secret or a credential",
		"Where do I get GITHUB_TOKEN?":                       "a secret or a credential",
		"Is DATABASE_PASSWORD in the vault?":                 "a secret or a credential",
		"set githubToken?":                                   "a secret or a credential",
		"Where is dbPassword read?":                          "a secret or a credential",
		"run deploy_prod.sh?":                                "an irreversible or production change",
	} {
		t.Run(q, func(t *testing.T) {
			f := open(t, 1000)
			// An org rule that would propose an answer to everything cannot take precedence.
			withRules(f, true, rule(".*", "yes"))
			f.model.replies["small-1"] = `{"answer": "yes", "confident": true}`
			p, ok, err := f.sup.ProposeAnswer(ctx, ask(q))
			if err != nil || ok || p.Answer != "" {
				t.Fatalf("proposal %+v, %v, %v; want the owner", p, ok, err)
			}
			if len(f.model.requests) != 0 {
				t.Fatalf("a model was asked about %q", q)
			}
			if r := declined(t, f); len(r) != 1 || !strings.Contains(r[0], about) || !strings.Contains(r[0], "needs the owner") {
				t.Fatalf("declined %q, want about %s", r, about)
			}
		})
	}
}

// Plain questions match no safety rule: an org rule that proposes for everything proposes for them.
func TestSafetyRulesLetPlainQuestionsThrough(t *testing.T) {
	for _, q := range []string{"Tabs or spaces?", "Which region should the bucket use?", "Should the product page reproduce the old layout?"} {
		f := open(t, 1000)
		withRules(f, false, rule(".*", "yes"))
		if p, ok, err := f.sup.ProposeAnswer(ctx, ask(q)); err != nil || !ok || p.Answer != "yes" {
			t.Errorf("%q: %+v, %v, %v; want the org rule's answer", q, p, ok, err)
		}
	}
}

func TestTheOrgsRulesComeBeforeTheModels(t *testing.T) {
	f := open(t, 1000)
	withRules(f, true, rule(`\bregion\b`, "eu-west"), rule(`\bregion\b`, "us-east"), rule(`\btimezone\b`, ""))
	f.model.replies["small-1"] = `{"answer": "from the model", "confident": true}`

	p, ok, err := f.sup.ProposeAnswer(ctx, ask("Which region should the bucket use?"))
	if err != nil || !ok || p.By != supervise.ByRules || p.Answer != "eu-west" {
		t.Fatalf("the first matching rule: %+v, %v, %v", p, ok, err)
	}
	if _, ok, err := f.sup.ProposeAnswer(ctx, ask("Which TIMEZONE do the reports use?")); err != nil || ok {
		t.Fatalf("an owner rule: %v, %v", ok, err)
	}
	if r := declined(t, f); len(r) != 1 || !strings.Contains(r[0], "rule 3") {
		t.Fatalf("declined %q", r)
	}
	if len(f.model.requests) != 0 {
		t.Fatalf("a model was asked when a rule matched: %d calls", len(f.model.requests))
	}
	if used, _ := supervise.Used(ctx, f.store, "acme", "2026-11-28"); used != 0 {
		t.Fatalf("rules spent %d tokens", used)
	}
	// No rule matches: the small model is asked.
	if p, ok, err := f.sup.ProposeAnswer(ctx, ask("Tabs or spaces?")); err != nil || !ok || p.By != supervise.By {
		t.Fatalf("unmatched: %+v, %v, %v", p, ok, err)
	}
}

// A rules-only org, or one whose models the daemon did not arm, is supervised by its rules alone.
func TestRulesApplyWithoutTheModels(t *testing.T) {
	for name, armed := range map[string]bool{"rules only": true, "models not armed": false} {
		t.Run(name, func(t *testing.T) {
			f := open(t, 1000)
			f.sup.Models = armed
			withRules(f, !armed, rule(`\bregion\b`, "eu-west"))
			if f.sup.ModelsFor("acme") {
				t.Fatal("the models are asked")
			}
			if p, ok, err := f.sup.ProposeAnswer(ctx, ask("Which region?")); err != nil || !ok || p.Answer != "eu-west" {
				t.Fatalf("a rule: %+v, %v, %v", p, ok, err)
			}
			if _, ok, err := f.sup.ProposeAnswer(ctx, ask("Tabs or spaces?")); err != nil || ok || len(f.model.requests) != 0 || len(declined(t, f)) != 0 {
				t.Fatalf("unmatched: %v, %v, %d calls", ok, err, len(f.model.requests))
			}
		})
	}
}

// Rules read from an Airspace through admission and Configs; refused ones are never admitted.
func TestRulesFromTheAirspace(t *testing.T) {
	admit := func(supervisor string) []spec.Result {
		return spec.LoadFile("airspace.yaml", []byte("apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\n"+
			"spec: { supervisor: "+supervisor+" }\n"))
	}
	ok := admit(`{ rules: [ { match: '\bregion\b', propose: eu-west }, { match: 'time ?zone', owner: true } ] }`)
	if len(ok) != 1 || ok[0].Resource == nil {
		t.Fatalf("refused: %+v", ok)
	}
	c := supervise.Configs([]spec.Resource{*ok[0].Resource})["acme"]
	if c.Invalid != "" || c.UsesModels() || len(c.Rules) != 2 || c.Rules[0].Propose != "eu-west" || !c.Rules[1].Owner {
		t.Fatalf("config %+v", c)
	}
	// A match counts characters, as the schema does; a proposed answer counts bytes, the most the
	// Tower keeps of it, so an admitted answer is never cut short.
	long := strings.Repeat("я", spec.MaxProposeBytes/2) // 512 characters, 1024 bytes
	if r := admit(`{ rules: [ { match: '` + strings.Repeat("я", 500) + `', propose: '` + long + `' } ] }`); len(r) != 1 || r[0].Resource == nil {
		t.Fatalf("multi-byte rule refused: %+v", r)
	} else if c := supervise.Configs([]spec.Resource{*r[0].Resource})["acme"]; c.Invalid != "" || len(c.Rules) != 1 || c.Rules[0].Propose != long {
		t.Fatalf("multi-byte rule not read: %+v", c.Invalid)
	}
	over := admit(`{ rules: [ { match: x, owner: true }, { match: y, propose: '` + long + `z' } ] }`) // 1025 bytes, 513 characters
	if len(over) != 1 || over[0].Resource != nil || len(over[0].Conditions) != 1 || !strings.Contains(over[0].Conditions[0].Message, "spec.supervisor.rules[1].propose is 1025 bytes; at most 1024 bytes") {
		t.Fatalf("a 1025-byte answer: %+v", over)
	}
	for name, bad := range map[string]string{
		"not RE2":                `{ rules: [ { match: '(?=x)', propose: y } ] }`,
		"both propose and owner": `{ rules: [ { match: x, propose: y, owner: true } ] }`,
		"neither":                `{ rules: [ { match: x } ] }`,
		"owner false":            `{ rules: [ { match: x, owner: false } ] }`,
		"half the models":        `{ route: models, smallModel: s }`,
		"a long match":           `{ rules: [ { match: '` + strings.Repeat("a", 513) + `', owner: true } ] }`,
	} {
		if r := admit(bad); len(r) != 1 || r[0].Resource != nil {
			t.Errorf("%s: admitted", name)
		}
	}
}
