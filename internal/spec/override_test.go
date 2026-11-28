package spec

import (
	"fmt"
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
		"the spec's fixture": {fixture, "test-coverage (role engineering-manager)"},
		"an owner override":  {LoadFile("g.yaml", []byte(gate("      override: { requiredRole: owner, requiresJustification: true }\n", "{ metric: line-coverage, operator: '>=', threshold: 80 }"))), ""},
		"no override":        {LoadFile("g.yaml", []byte(gate("", "{ metric: line-coverage, operator: '>=', threshold: 80 }"))), ""},
		"visual approvers": {LoadFile("g.yaml", []byte(gate("", "{ visualRegression: true, designSystem: web, override: { approvers: [owner, design-lead] } }"))),
			"coverage (approvers design-lead)"},
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

func gates(n int, nameLen int) string {
	var b strings.Builder
	b.WriteString("apiVersion: ai-sdlc.io/v1alpha1\nkind: QualityGate\nmetadata: { name: g, namespace: acme }\nspec:\n  gates:\n")
	for i := range n {
		name := fmt.Sprintf("gate-%02d-%s", i, strings.Repeat("x", nameLen))
		fmt.Fprintf(&b, "    - name: %s\n      enforcement: soft-mandatory\n      rule: { metric: line-coverage, operator: '>=', threshold: 80 }\n"+
			"      override: { requiredRole: engineering-manager, requiresJustification: true }\n", name)
	}
	return b.String()
}

// The Override condition names every gate it does not act on, compactly, and when they do not all
// fit its cap it ends with "and N more" rather than dropping any unannounced.
func TestOverrideMessageIsComplete(t *testing.T) {
	results := LoadFile("g.yaml", []byte(gates(3, 0)))
	markOverrides(results)
	cond, ok := results[0].Condition(ConditionOverride)
	if !ok {
		t.Fatalf("no Override condition: %+v", results[0].Conditions)
	}
	for i := range 3 {
		if want := fmt.Sprintf("gate-%02d- (role engineering-manager)", i); !strings.Contains(cond.Message, want) {
			t.Errorf("message %q does not name %q", cond.Message, want)
		}
	}
	if strings.Contains(cond.Message, "more") || len(cond.Message) > maxOverrideMessage {
		t.Errorf("message %q (%d bytes) is not the complete list", cond.Message, len(cond.Message))
	}

	const n = 20
	results = LoadFile("g.yaml", []byte(gates(n, 20)))
	markOverrides(results)
	cond, _ = results[0].Condition(ConditionOverride)
	if len(cond.Message) > maxOverrideMessage {
		t.Errorf("message is %d bytes, over %d", len(cond.Message), maxOverrideMessage)
	}
	listed := strings.Count(cond.Message, "(role engineering-manager)")
	if listed == 0 || listed == n || !strings.HasSuffix(cond.Message, fmt.Sprintf("and %d more", n-listed)) {
		t.Errorf("message %q lists %d of %d gates; want a prefix ending with \"and %d more\"", cond.Message, listed, n, n-listed)
	}
	if rec := recorded(results[0]); len(rec) == 0 || rec[len(rec)-1].Message != cond.Message {
		t.Errorf("recorded status %+v does not carry the whole message %q", rec, cond.Message)
	}
}
