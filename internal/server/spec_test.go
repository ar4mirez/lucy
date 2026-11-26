package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/spec"
)

func TestResourcesList(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	page, err := f.client.Resources(ctx, api.ResourcesParams{})
	if err != nil || len(page.Resources) != 0 || page.More {
		t.Fatalf("empty config: %+v %v", page, err)
	}

	dir := t.TempDir()
	for name, doc := range map[string]string{
		"airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\nspec: {}\n",
		"pilot.yaml":    "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p1, namespace: acme }\nspec: { role: captain }\n",
		"runway.yaml":   "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web }\nspec: {}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set, err := spec.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Record(ctx, f.store, "daemon", set); err != nil {
		t.Fatal(err)
	}
	page, err = f.client.Resources(ctx, api.ResourcesParams{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Resources) != 1 || !page.More || page.Next != 1 {
		t.Fatalf("first page %+v", page)
	}
	if len(page.Refused) != 1 || page.Refused[0].Source != "runway.yaml#1" || page.Refused[0].Reason != spec.ReasonMissingNamespace ||
		page.Refused[0].Conditions[0].Status != "False" || page.Refused[0].Conditions[0].LastTransitionTime == "" {
		t.Fatalf("first page refused %+v", page.Refused)
	}
	a := page.Resources[0]
	if a.Kind != "Airspace" || a.SpecSHA256 == "" || a.Conditions[0].Type != "Valid" || a.Conditions[0].Status != "True" || a.Conditions[0].LastTransitionTime == "" {
		t.Fatalf("airspace %+v", a)
	}
	page, err = f.client.Resources(ctx, api.ResourcesParams{Offset: page.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Resources) != 1 || page.More || page.Resources[0].Conditions[0].Reason != spec.ReasonSchemaViolation || len(page.Refused) != 0 {
		t.Fatalf("second page %+v", page)
	}

	// Each load replaces the refusals of the one before.
	if err := os.Remove(filepath.Join(dir, "runway.yaml")); err != nil {
		t.Fatal(err)
	}
	if set, err = spec.LoadDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Record(ctx, f.store, "daemon", set); err != nil {
		t.Fatal(err)
	}
	if page, err = f.client.Resources(ctx, api.ResourcesParams{}); err != nil || len(page.Refused) != 0 || len(page.Resources) != 2 {
		t.Fatalf("after fixing the runway %+v %v", page, err)
	}
}

func TestAuditVerify(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	if _, err := f.client.Evaluate(ctx, api.EvaluateParams{Kind: "read", Org: "acme"}); err != nil {
		t.Fatal(err)
	}
	rep, err := f.client.VerifyAudit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// policy.evaluate appends its api.command and its policy.decision.
	if !rep.Verified || rep.Entries != 2 || rep.Head == "" {
		t.Fatalf("report %+v", rep)
	}
}
