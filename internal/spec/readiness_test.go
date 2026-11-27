package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every admitted Flight carries a Ready condition after a load: True only with a goal, a runway
// admitted in its namespace, a delivery that fits its shape, an autonomy posture and an acceptance
// test; otherwise False, naming what it lacks. A Flight that is not ready is still admitted
// (LUCY-SPEC-012).
func TestFlightReadiness(t *testing.T) {
	dir := t.TempDir()
	docs := map[string]string{
		"airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\nspec: {}\n",
		"runway.yaml":   "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: { repository: acme/web }\n",
		"beta.yaml":     "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: api, namespace: beta }\nspec: { repository: beta/api }\n",
		"ready.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: ready, namespace: acme }\n" +
			"spec: { shape: ship, goal: fix the login page, runway: web, delivery: pull-request, autonomy: green, acceptance: the login test passes }\n",
		"bare.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: bare, namespace: acme }\nspec: {}\n",
		"research.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: study, namespace: acme }\n" +
			"spec: { shape: research, goal: compare caches, runway: web, delivery: pull-request, autonomy: owner, acceptance: a written comparison }\n",
		"elsewhere.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: elsewhere, namespace: acme }\n" +
			"spec: { goal: g, runway: api, delivery: pull-request, autonomy: owner, acceptance: a }\n",
	}
	for name, doc := range docs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	set, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ready := map[string]Condition{}
	for _, r := range set.Results {
		if r.Resource == nil {
			t.Fatalf("%s was refused: %+v", r.Source.File, r.Conditions)
		}
		for _, c := range r.Conditions {
			if c.Type == ConditionReady {
				ready[r.Resource.Metadata.Name] = c
			}
		}
	}
	if len(ready) != 4 {
		t.Fatalf("Ready conditions on %v; want every Flight and nothing else", ready)
	}
	if c := ready["ready"]; c.Status != True || c.Reason != ReasonReady {
		t.Errorf("a ready Flight: %+v", c)
	}
	for name, want := range map[string][]string{
		"bare":      {"a goal", "a delivery path: a runway", "a delivery (pull-request)", "an autonomy posture", "an acceptance test"},
		"study":     {"a delivery path that fits a research Flight (report, not pull-request)"},
		"elsewhere": {`a runway "api" admitted in namespace "acme"`},
	} {
		c := ready[name]
		if c.Status != False || c.Reason != ReasonNotReady {
			t.Errorf("%s: %+v", name, c)
		}
		for _, w := range want {
			if !strings.Contains(c.Message, w) {
				t.Errorf("%s: %q lacks %q", name, c.Message, w)
			}
		}
	}
}
