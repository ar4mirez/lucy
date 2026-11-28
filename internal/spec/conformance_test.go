package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// coreFixtures is the spec's conformance suite, vendored at the pin (conformance/tests/v1alpha1,
// listed in PROVENANCE): the Core level's fixtures, every pipeline and quality-gate one and every
// invalid one of any kind.
const coreFixtures = "testdata/conformance/v1alpha1"

// lucyRules are the two admission rules lucy adds to the spec, each a requirement of its own:
// metadata.namespace names the org (LUCY-SPEC-002), and status is never read from config
// (LUCY-SPEC-003). They are the only reasons lucy may refuse a document the spec accepts.
var lucyRules = []string{ReasonMissingNamespace, ReasonStatusInConfig}

// asLucyConfig is doc as an org's config file: in a namespace when it names none, and without
// status. It changes nothing else.
func asLucyConfig(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	out := map[string]any{}
	for k, v := range doc {
		if k != "status" {
			out[k] = v
		}
	}
	md := map[string]any{}
	if m, ok := doc["metadata"].(map[string]any); ok {
		for k, v := range m {
			md[k] = v
		}
	}
	if _, ok := md["namespace"]; !ok {
		md["namespace"] = "conformance"
	}
	out["metadata"] = md
	data, err := json.Marshal(out) // JSON is YAML: LoadFile reads it as is
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestCoreConformance is the spec's Core conformance level (LUCY-SPEC-017, spec section 11, and
// the suite's README): every pipeline/valid-* and quality-gate/valid-* fixture is accepted and
// every invalid-* fixture is rejected. The expectation comes from the suite's naming rule, not
// from lucy: valid-* documents MUST be accepted, invalid-* documents MUST be rejected.
//
// lucy's loader admits a valid fixture once it is written as an org's config (in a namespace, no
// status), and refuses an invalid one, so written, on the schema. As the fixture ships, lucy
// refuses a valid one only under its own two rules, never on the schema.
func TestCoreConformance(t *testing.T) {
	var valid, invalid int
	err := filepath.WalkDir(coreFixtures, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		kind := filepath.Base(filepath.Dir(path))
		isValid := strings.HasPrefix(name, "valid-")
		if !strings.HasPrefix(name, "invalid-") && !(isValid && (kind == "pipeline" || kind == "quality-gate")) {
			t.Errorf("%s is not a Core fixture; vendor only the Core level", path)
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		docs, err := Decode(data)
		if err != nil || len(docs) != 1 {
			t.Errorf("%s: %v (%d documents)", path, err, len(docs))
			return nil
		}
		doc, _ := docs[0].(map[string]any)
		results := LoadFile(name, asLucyConfig(t, doc))
		if len(results) != 1 {
			t.Errorf("%s: %d results", path, len(results))
			return nil
		}
		r := results[0]
		if isValid {
			valid++
			if !r.Valid() {
				t.Errorf("%s must be accepted: %+v", path, r.Conditions)
			}
			raw := LoadFile(name, data)[0]
			if c, _ := raw.Condition(ConditionValid); !raw.Valid() && !slices.Contains(lucyRules, c.Reason) {
				t.Errorf("%s as shipped is refused for %s; lucy may refuse a spec-valid document only under %v", path, c.Reason, lucyRules)
			}
			return nil
		}
		invalid++
		if c, _ := r.Condition(ConditionValid); r.Valid() || c.Reason != ReasonSchemaViolation {
			t.Errorf("%s must be rejected by the schema: valid %v, %+v", path, r.Valid(), c)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The pinned suite's Core level: 9 pipeline and 3 quality-gate valid fixtures, 9 invalid ones.
	if valid != 12 || invalid != 9 {
		t.Errorf("ran %d valid and %d invalid fixtures, want 12 and 9: the vendored suite is incomplete", valid, invalid)
	}
}
