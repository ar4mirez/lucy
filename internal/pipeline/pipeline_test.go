package pipeline

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/spec"
)

// load admits doc through the spec loader, as the daemon does, so every case is also a valid spec
// Pipeline: what Parse refuses, the schema allows.
func load(t *testing.T, doc string) spec.Resource {
	t.Helper()
	res := spec.LoadFile("p.yaml", []byte(doc))
	if len(res) != 1 || res[0].Resource == nil {
		t.Fatalf("not admitted: %+v", res)
	}
	return *res[0].Resource
}

func pipelineDoc(ns, name, stages, extra string) string {
	return "apiVersion: ai-sdlc.io/v1alpha1\nkind: Pipeline\nmetadata: { name: " + name + ", namespace: " + ns + " }\n" +
		"spec:\n  triggers: [{ event: flight.dispatched }]\n  providers: { forge: { type: github } }\n  stages:\n" + stages + extra
}

const pilotStage = "    - { name: pilot, onFailure: { strategy: retry, maxRetries: 2 } }\n"

// The built-in Pipelines are valid spec Pipelines and say what lucy does by default: a lost Pilot
// is replaced once, its credentials last ten minutes and are revoked when it ends, and a ship
// merge's approval is left to the autonomy posture, with no timeout.
func TestBuiltin(t *testing.T) {
	b, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	ship, research := b[Ship], b[Research]
	if ship.Pilot != (Failure{Strategy: StrategyRetry, MaxRetries: 1}) || research.Pilot != (Failure{Strategy: StrategyRetry, MaxRetries: 1}) {
		t.Errorf("pilot failure policies: %+v, %+v", ship.Pilot, research.Pilot)
	}
	if ship.Merge != (Approval{}) {
		t.Errorf("ship merge approval %+v, want the posture to decide, with no timeout", ship.Merge)
	}
	if ship.Credentials.TTL != 10*time.Minute || !ship.Credentials.RevokeOnComplete || !slices.Equal(ship.Credentials.Scope, []string{"contents:write", "pull_requests:write"}) {
		t.Errorf("ship credentials %+v", ship.Credentials)
	}
	if !slices.Equal(research.Credentials.Scope, []string{"contents:read"}) {
		t.Errorf("research credentials %+v", research.Credentials)
	}
	if ship.PullRequest.CloseKeyword != "Closes" || !ship.PullRequest.IncludeProvenance {
		t.Errorf("ship pull request config %+v", ship.PullRequest)
	}
}

// A timeout never approves a merge: an approval whose onTimeout is auto-approve is refused on
// whichever stage it is written.
func TestAutoApproveIsRefused(t *testing.T) {
	for _, stages := range []string{
		pilotStage + "    - { name: merge, approval: { required: true, timeout: PT1H, onTimeout: auto-approve } }\n",
		"    - { name: pilot, approval: { required: true, timeout: PT1H, onTimeout: auto-approve } }\n",
	} {
		if _, err := Parse(load(t, pipelineDoc("acme", Ship, stages, ""))); !errors.Is(err, ErrAutoApprove) {
			t.Errorf("%s: %v, want ErrAutoApprove", stages, err)
		}
	}
}

func TestMergeApproval(t *testing.T) {
	for _, c := range []struct {
		approval string
		want     Approval
	}{
		{"{ required: true }", Approval{Required: true}},
		{"{ required: true, timeout: PT2H, onTimeout: abort }", Approval{Required: true, Timeout: 2 * time.Hour, OnTimeout: OnTimeoutAbort}},
		{"{ required: false, timeout: P1D }", Approval{Timeout: 24 * time.Hour, OnTimeout: OnTimeoutEscalate}},
	} {
		p, err := Parse(load(t, pipelineDoc("acme", Ship, pilotStage+"    - { name: merge, approval: "+c.approval+" }\n", "")))
		if err != nil || p.Merge != c.want {
			t.Errorf("%s: %+v, %v; want %+v", c.approval, p.Merge, err, c.want)
		}
		if p.Pilot != (Failure{Strategy: StrategyRetry, MaxRetries: 2}) {
			t.Errorf("pilot %+v", p.Pilot)
		}
	}
}

// What lucy does not fly is refused with a reason, not silently ignored.
func TestRefused(t *testing.T) {
	for _, c := range []struct{ name, pipeline, stages, extra, want string }{
		{"a pause strategy", Ship, "    - { name: pilot, onFailure: { strategy: pause } }\n", "", "not one lucy flies"},
		{"an unknown stage", Ship, pilotStage + "    - { name: deploy }\n", "", `"deploy"`},
		{"a merge in research", Research, pilotStage + "    - { name: merge }\n", "", `"merge"`},
		{"no pilot stage", Ship, "    - { name: merge }\n", "", "no pilot stage"},
		{"a non-blocking merge", Ship, pilotStage + "    - { name: merge, approval: { required: true, blocking: false } }\n", "", "always waits"},
		{"onTimeout without a timeout", Ship, pilotStage + "    - { name: merge, approval: { required: true, onTimeout: abort } }\n", "", "without a timeout"},
		{"an agent on the merge", Ship, pilotStage + "    - { name: merge, agent: worker }\n", "", "no agent does"},
		{"the wrong pilot agent", Ship, "    - { name: pilot, agent: reviewer }\n", "", "a ship Pilot is a worker"},
		{"an approval before the Pilot", Ship, "    - { name: pilot, approval: { required: true } }\n", "", "does not hold a Pilot's start"},
		{"credentials kept", Ship, "    - { name: pilot, credentials: { scope: [contents:read], revokeOnComplete: false } }\n", "", "revokes"},
		{"a long credential", Ship, "    - { name: pilot, credentials: { scope: [contents:read], ttl: PT5H } }\n", "", "ttl"},
		{"a pull request on research", Research, pilotStage, "  pullRequest: { closeKeyword: Fixes }\n", "a report"},
		{"another Pipeline", "deploy", pilotStage, "", "only the Pipelines"},
	} {
		_, err := Parse(load(t, pipelineDoc("acme", c.pipeline, c.stages, c.extra)))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "acme/"+c.pipeline) {
			t.Errorf("%s: %v, want ErrInvalid naming acme/%s and %q", c.name, err, c.pipeline, c.want)
		}
	}
}

// ISO 8601 durations, from the standard's own examples of days, hours, minutes and seconds.
func TestDuration(t *testing.T) {
	for s, want := range map[string]time.Duration{"PT10M": 10 * time.Minute, "P1DT2H30M": 26*time.Hour + 30*time.Minute, "PT45S": 45 * time.Second, "P2D": 48 * time.Hour} {
		if got, err := Duration(s); err != nil || got != want {
			t.Errorf("Duration(%q) = %v, %v; want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "P", "PT", "P1DT", "P1Y", "P1W", "PT1.5S", "10m", "PT-1M", "PT99999999999999999999H"} {
		if _, err := Duration(s); err == nil {
			t.Errorf("Duration(%q) accepted", s)
		}
	}
}

// An org's Pipeline of a lucy name replaces the built-in one for that org only; one in the
// reserved namespace changes nothing; one lucy cannot fly is an error naming it.
func TestCatalog(t *testing.T) {
	strict := pipelineDoc("acme", Ship, pilotStage+"    - { name: merge, approval: { required: true } }\n", "")
	override := pipelineDoc(Namespace, Ship, "    - { name: pilot, onFailure: { strategy: abort } }\n", "")
	c, err := NewCatalog([]spec.Resource{load(t, strict), load(t, override)})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := c.For("acme", Ship); !p.Merge.Required || p.Pilot.MaxRetries != 2 {
		t.Errorf("acme's ship %+v", p)
	}
	if p, _ := c.For("beta", Ship); p.Merge.Required || p.Pilot.MaxRetries != 1 {
		t.Errorf("beta's ship %+v; another org's Pipeline or the reserved namespace leaked", p)
	}
	if _, ok := c.For("acme", "deploy"); ok {
		t.Error("a Pipeline lucy does not fly was found")
	}
	_, err = NewCatalog([]spec.Resource{load(t, pipelineDoc("acme", Ship, "    - { name: pilot, onFailure: { strategy: continue } }\n", ""))})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "acme/ship") {
		t.Errorf("an invalid org Pipeline: %v", err)
	}
}
