package worktree

import (
	"errors"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/spec"
)

func poolDoc(ns, name, specYAML string) string {
	return "apiVersion: ai-sdlc.io/v1alpha1\nkind: WorktreePool\nmetadata: { name: " + name + ", namespace: " + ns + " }\nspec: " + specYAML + "\n"
}

func loadPool(t *testing.T, doc string) []spec.Result {
	t.Helper()
	return spec.LoadFile("pool.yaml", []byte(doc))
}

// A WorktreePool is accepted with only what lucy acts on, and every other field is refused by its
// path; each case is a document the spec's schema accepts (LUCY-SPEC-017).
func TestParsePool(t *testing.T) {
	for _, ok := range []string{"{}", "{ layout: workspace-scoped }", "{ cleanup: { onMerge: true } }", "{ layout: workspace-scoped, cleanup: {} }"} {
		res := loadPool(t, poolDoc("acme", "pool", ok))
		if !res[0].Valid() {
			t.Fatalf("%s not admitted: %+v", ok, res[0].Conditions)
		}
		if err := ParsePool(*res[0].Resource); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for specYAML, path := range map[string]string{
		"{ rootDir: /srv/worktrees }":                "spec.rootDir",
		"{ layout: repo-local }":                     "spec.layout",
		"{ staleThresholdDays: 7 }":                  "spec.staleThresholdDays",
		"{ basePort: 4000 }":                         "spec.basePort",
		"{ ownershipGuard: advisory }":               "spec.ownershipGuard",
		"{ cleanup: { onAbort: false } }":            "spec.cleanup.onAbort",
		"{ cleanup: { onTimeout: false } }":          "spec.cleanup.onTimeout",
		"{ basePort: 3190, staleThresholdDays: 30 }": "spec.staleThresholdDays",
		"{ cleanup: { onMerge: false } }":            "spec.cleanup.onMerge",
		"{ databaseBranchPools: [db] }":              "spec.databaseBranchPools",
		"{ subscriptionPlans: [max] }":               "spec.subscriptionPlans",
	} {
		res := loadPool(t, poolDoc("acme", "pool", specYAML))
		if !res[0].Valid() {
			t.Fatalf("%s not admitted: %+v", specYAML, res[0].Conditions)
		}
		err := ParsePool(*res[0].Resource)
		if !errors.Is(err, ErrPoolUnsupported) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "acme/pool") {
			t.Errorf("%s: %v, want ErrPoolUnsupported naming acme/pool and %s", specYAML, err, path)
		}
	}
}

// An org's pool declaration is refused when its WorktreePool was not admitted, when lucy cannot
// accept it, or when the org declares two; other orgs are untouched.
func TestPoolErrors(t *testing.T) {
	var results []spec.Result
	for _, doc := range []string{
		poolDoc("acme", "pool", "{ cleanup: { onMerge: true } }"),
		poolDoc("beta", "pool", "{ layout: sideways }"), // the schema refuses it
		poolDoc("gamma", "pool", "{ rootDir: /tmp/x }"),
		poolDoc("delta", "one", "{}"),
		poolDoc("delta", "two", "{}"),
	} {
		results = append(results, loadPool(t, doc)...)
	}
	errs := PoolErrors(results)
	if errs["acme"] != nil {
		t.Errorf("acme: %v", errs["acme"])
	}
	for org, want := range map[string]string{"beta": "was not admitted", "gamma": "spec.rootDir", "delta": "more than one WorktreePool"} {
		if err := errs[org]; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", org, err, want)
		}
	}
}
