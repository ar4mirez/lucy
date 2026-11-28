package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/spec"
)

// lucy flights --json writes each Flight the daemon loaded as a BacklogTask document, one per line,
// that the spec's vendored BacklogTask schema accepts (LUCY-SPEC-017).
func TestFlightsAsBacklogTasks(t *testing.T) {
	sock := startDaemonWith(t, func(db string) {
		res := filepath.Join(filepath.Dir(filepath.Dir(db)), "config", "lucy", "resources")
		if err := os.MkdirAll(res, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, doc := range map[string]string{
			"airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\nspec: {}\n",
			"runway.yaml":   "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: {}\n",
			"ready.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: fix-login, namespace: acme }\n" +
				"spec: { shape: ship, goal: fix the login page, runway: web, delivery: pull-request, autonomy: owner, acceptance: the login test passes }\n",
			"draft.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: draft, namespace: acme }\nspec: { goal: someday }\n",
		} {
			if err := os.WriteFile(filepath.Join(res, name), []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	})
	code, out, errOut := lucy("flights", "--json", "--socket", sock)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 BacklogTask lines, got %q", out)
	}
	byID := map[string]map[string]any{}
	for _, l := range lines {
		var doc map[string]any
		if err := json.Unmarshal([]byte(l), &doc); err != nil {
			t.Fatal(err)
		}
		if errs, err := spec.Validate(spec.BacklogTaskSchema, doc); err != nil || len(errs) > 0 {
			t.Fatalf("%s is not a BacklogTask: %v %v", l, err, errs)
		}
		byID[doc["id"].(string)] = doc
	}
	ready, draft := byID["acme/fix-login"], byID["acme/draft"]
	if ready["title"] != "fix the login page" || ready["status"] != "ready" || ready["dispatchable"] != true {
		t.Errorf("the ready Flight: %v", ready)
	}
	if draft["status"] != "not ready" || draft["dispatchable"] != false || draft["dispatchableReason"] == nil {
		t.Errorf("the draft Flight: %v", draft)
	}
	code, out, _ = lucy("flights", "--socket", sock)
	if code != 0 || !strings.Contains(out, "flights[2]") || !strings.Contains(out, "acme/fix-login,ship,True (Ready)") {
		t.Errorf("the table: %d %q", code, out)
	}
}
