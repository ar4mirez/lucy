package spec

import (
	"os"
	"strings"
	"testing"
)

func gate(override, rule string) string {
	return "apiVersion: ai-sdlc.io/v1alpha1\nkind: QualityGate\nmetadata: { name: g, namespace: acme }\nspec:\n  gates:\n" +
		"    - name: coverage\n      enforcement: soft-mandatory\n      rule: " + rule + "\n" + override
}

// A QualityGate that lets anyone but the owner override a gate is still admitted, as the spec
// writes it (Core conformance holds), and carries an Override condition saying lucy lets only the
// owner override it (LUCY-SPEC-010); one that names only the owner, or no override, carries none.
func TestOverridesAreTheOwners(t *testing.T) {
	// The spec's own conformance fixture names an engineering-manager, as an org's config.
	data, err := os.ReadFile(coreFixtures + "/quality-gate/valid-metric-rule.yaml")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	fixture := LoadFile("fixture.json", asLucyConfig(t, docs[0].(map[string]any)))
	for name, c := range map[string]struct {
		results []Result
		want    string // a part of the Override condition's message; "" for no condition
	}{
		"the spec's fixture": {fixture, "gate test-coverage: override.requiredRole engineering-manager"},
		"an owner override":  {LoadFile("g.yaml", []byte(gate("      override: { requiredRole: owner, requiresJustification: true }\n", "{ metric: line-coverage, operator: '>=', threshold: 80 }"))), ""},
		"no override":        {LoadFile("g.yaml", []byte(gate("", "{ metric: line-coverage, operator: '>=', threshold: 80 }"))), ""},
		"visual approvers": {LoadFile("g.yaml", []byte(gate("", "{ visualRegression: true, designSystem: web, override: { approvers: [owner, design-lead] } }"))),
			"gate coverage: rule.override.approvers design-lead"},
		"visual, owner only": {LoadFile("g.yaml", []byte(gate("", "{ visualRegression: true, designSystem: web, override: { approvers: [owner] } }"))), ""},
	} {
		markOverrides(c.results)
		r := c.results[0]
		if !r.Valid() {
			t.Fatalf("%s was not admitted: %+v", name, r.Conditions)
		}
		cond, ok := r.Condition(ConditionOverride)
		switch {
		case c.want == "" && ok:
			t.Errorf("%s: an Override condition %+v", name, cond)
		case c.want != "" && (!ok || cond.Reason != ReasonOverrideIsOwner || cond.Status != True || !strings.Contains(cond.Message, c.want)):
			t.Errorf("%s: Override condition %+v, %v; want OverrideIsOwner naming %q", name, cond, ok, c.want)
		}
	}
}
