package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/spec"
)

// A QualityGate that names an override role other than the owner is loaded and admitted, and its
// status, which lucy config status shows, says that lucy lets only the owner override it
// (LUCY-SPEC-010).
func TestAGateOverrideIsTheOwners(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	if err := os.MkdirAll(o.ResourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := "apiVersion: ai-sdlc.io/v1alpha1\nkind: QualityGate\nmetadata: { name: coverage-gate, namespace: acme }\nspec:\n  gates:\n" +
		"    - name: test-coverage\n      enforcement: soft-mandatory\n      rule: { metric: line-coverage, operator: '>=', threshold: 80 }\n" +
		"      override: { requiredRole: engineering-manager, requiresJustification: true }\n"
	if err := os.WriteFile(filepath.Join(o.ResourcesDir, "gate.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	c := startClient(t, o)
	page, err := c.Resources(context.Background(), api.ResourcesParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Resources {
		if r.Kind != "QualityGate" {
			continue
		}
		var valid, override bool
		for _, cond := range r.Conditions {
			valid = valid || cond.Type == spec.ConditionValid && cond.Status == "True"
			override = override || cond.Type == spec.ConditionOverride && cond.Reason == spec.ReasonOverrideIsOwner &&
				strings.Contains(cond.Message, "engineering-manager")
		}
		if !valid || !override {
			t.Fatalf("the QualityGate's conditions %+v; want Valid and Override (OverrideIsOwner) naming engineering-manager", r.Conditions)
		}
		return
	}
	t.Fatalf("no QualityGate in %+v", page.Resources)
}
