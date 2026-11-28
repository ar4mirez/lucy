package pipeline

import (
	"errors"
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
// is replaced once, and a ship merge's approval is left to the autonomy posture, with no timeout.
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
		{"{ required: false, blocking: true, timeout: P1D, onTimeout: abort }", Approval{Timeout: 24 * time.Hour, OnTimeout: OnTimeoutAbort}},
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
		{"maxRetries with abort", Ship, "    - { name: pilot, onFailure: { strategy: abort, maxRetries: 2 } }\n", "", "replaces no Pilot"},
		{"onTimeout without a timeout", Ship, pilotStage + "    - { name: merge, approval: { required: true, onTimeout: abort } }\n", "", "without a timeout"},
		{"an agent on the merge", Ship, pilotStage + "    - { name: merge, agent: worker }\n", "", "no agent does"},
		{"the wrong pilot agent", Ship, "    - { name: pilot, agent: reviewer }\n", "", "a ship Pilot is a worker"},
		{"an approval before the Pilot", Ship, "    - { name: pilot, approval: { required: true } }\n", "", "does not hold a Pilot's start"},
		{"another Pipeline", "deploy", pilotStage, "", "only the Pipelines"},
	} {
		_, err := Parse(load(t, pipelineDoc("acme", c.pipeline, c.stages, c.extra)))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "acme/"+c.pipeline) {
			t.Errorf("%s: %v, want ErrInvalid naming acme/%s and %q", c.name, err, c.pipeline, c.want)
		}
	}
}

// A field lucy does not act on yet is refused, not silently ignored, naming the Pipeline and the
// field's path, wherever it is written.
func TestUnsupported(t *testing.T) {
	for _, c := range []struct{ name, pipeline, stages, extra, path string }{
		{"a stage timeout", Ship, "    - { name: pilot, timeout: PT30M }\n", "", "spec.stages[0].timeout"},
		{"a retry delay", Ship, "    - { name: pilot, onFailure: { strategy: retry, maxRetries: 2, retryDelay: PT1M } }\n", "", "spec.stages[0].onFailure.retryDelay"},
		{"a failure notification", Ship, "    - { name: pilot, onFailure: { strategy: abort, notification: owner } }\n", "", "spec.stages[0].onFailure.notification"},
		{"credentials on the pilot", Ship, "    - { name: pilot, credentials: { scope: [contents:read], ttl: PT10M } }\n", "", "spec.stages[0].credentials"},
		{"credentials on the merge", Ship, pilotStage + "    - { name: merge, credentials: { scope: [contents:write] } }\n", "", "spec.stages[1].credentials"},
		{"a tier override", Ship, pilotStage + "    - { name: merge, approval: { required: true, tierOverride: auto } }\n", "", "spec.stages[1].approval.tierOverride"},
		{"an escalating timeout", Ship, pilotStage + "    - { name: merge, approval: { required: true, timeout: PT1H, onTimeout: escalate } }\n", "", "spec.stages[1].approval.timeout"},
		{"a timeout without onTimeout", Ship, pilotStage + "    - { name: merge, approval: { required: false, timeout: PT1H } }\n", "", "spec.stages[1].approval.timeout"},
		{"routing", Ship, pilotStage, "  routing: { complexityThresholds: {} }\n", "spec.routing"},
		{"a model", Ship, pilotStage, "  defaultModel: opus\n", "spec.defaultModel"},
		{"a pull request on ship", Ship, pilotStage, "  pullRequest: { closeKeyword: Closes }\n", "spec.pullRequest"},
		{"a pull request on research", Research, pilotStage, "  pullRequest: { closeKeyword: Fixes }\n", "spec.pullRequest"},
	} {
		res := spec.LoadFile("p.yaml", []byte(pipelineDoc("acme", c.pipeline, c.stages, c.extra)))
		if len(res) != 1 || res[0].Resource == nil {
			t.Errorf("%s: not admitted by the spec loader: %+v", c.name, res)
			continue
		}
		_, err := Parse(*res[0].Resource)
		if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "acme/"+c.pipeline) || !strings.Contains(err.Error(), c.path) {
			t.Errorf("%s: %v, want ErrUnsupported naming acme/%s and %q", c.name, err, c.pipeline, c.path)
		}
	}
}

// A Pipeline of only what the format requires and a pilot stage loads, with the Tower's defaults:
// abort on a failed Pilot and no merge approval of its own.
func TestMinimal(t *testing.T) {
	p, err := Parse(load(t, pipelineDoc("acme", Ship, "    - { name: pilot }\n", "")))
	if err != nil || p.Pilot != (Failure{Strategy: StrategyAbort}) || p.Merge != (Approval{}) {
		t.Fatalf("%+v, %v", p, err)
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
