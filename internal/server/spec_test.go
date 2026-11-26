package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/audit"
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
	a := page.Resources[0]
	if a.Kind != "Airspace" || a.SpecSHA256 == "" || a.Conditions[0].Type != "Valid" || a.Conditions[0].Status != "True" || a.Conditions[0].LastTransitionTime == "" {
		t.Fatalf("airspace %+v", a)
	}
	page, err = f.client.Resources(ctx, api.ResourcesParams{Offset: page.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Resources) != 1 || page.More || page.Resources[0].Conditions[0].Reason != spec.ReasonSchemaViolation {
		t.Fatalf("second page %+v", page)
	}
}

func TestAuditVerifyAndEntries(t *testing.T) {
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
	page, err := f.client.AuditEntries(ctx, api.EntriesParams{Limit: 1})
	if err != nil || len(page.Entries) != 1 || !page.More {
		t.Fatalf("first page %+v %v", page, err)
	}
	rest, err := f.client.AuditEntries(ctx, api.EntriesParams{AfterSeq: page.Next})
	if err != nil || len(rest.Entries) != 1 || rest.More {
		t.Fatalf("second page %+v %v", rest, err)
	}
	var v audit.Verifier
	for _, raw := range append(page.Entries, rest.Entries...) {
		if _, err := v.Next(raw); err != nil {
			t.Fatal(err)
		}
	}
	if v.Report().Head != rep.Head {
		t.Fatalf("exported head %s, daemon says %s", v.Report().Head, rep.Head)
	}
	var e audit.Entry
	if err := json.Unmarshal(rest.Entries[0], &e); err != nil || e.Action != "read" || e.Resource != "org/acme" {
		t.Fatalf("decision entry %s: %v", rest.Entries[0], err)
	}
}
