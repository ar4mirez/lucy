package spec

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func admitYAML(t *testing.T, doc string) Result {
	t.Helper()
	rs := LoadFile("test.yaml", []byte(doc))
	if len(rs) != 1 {
		t.Fatalf("want one result, got %d: %+v", len(rs), rs)
	}
	return rs[0]
}

func wantReason(t *testing.T, r Result, reason string) {
	t.Helper()
	c, ok := r.Condition(ConditionValid)
	if !ok {
		t.Fatalf("no Valid condition: %+v", r)
	}
	if c.Reason != reason {
		t.Fatalf("reason %s (%s), want %s; errors %v", c.Reason, c.Message, reason, r.Errors)
	}
	wantStatus := False
	if reason == ReasonAdmitted {
		wantStatus = True
	}
	if c.Status != wantStatus || r.Valid() != (wantStatus == True) {
		t.Fatalf("status %s, valid %v for reason %s", c.Status, r.Valid(), reason)
	}
}

const airspace = `apiVersion: lucy/v1alpha1
kind: Airspace
metadata: { name: acme, namespace: acme, labels: { tier: client } }
spec: { displayName: Acme }
`

func TestAdmitLucyKinds(t *testing.T) {
	r := admitYAML(t, airspace)
	wantReason(t, r, ReasonAdmitted)
	if r.Resource.ID() != "acme/Airspace/acme" || r.Resource.Metadata.Labels["tier"] != "client" {
		t.Fatalf("resource %+v", r.Resource)
	}
	for _, doc := range []string{
		"apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: { repository: acme/web }\n",
		"apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: fix-login, namespace: acme }\nspec: { shape: ship, goal: fix the login page }\n",
		"apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p1, namespace: acme }\nspec: { role: reviewer }\n",
	} {
		wantReason(t, admitYAML(t, doc), ReasonAdmitted)
	}
}

func TestDefaultsApplied(t *testing.T) {
	r := admitYAML(t, "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: { repository: acme/web }\n")
	wantReason(t, r, ReasonAdmitted)
	if got := r.Resource.Spec["defaultBranch"]; got != "main" {
		t.Fatalf("defaultBranch = %v, want the schema default main", got)
	}
	// An explicit value wins over the default.
	r = admitYAML(t, "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: { defaultBranch: trunk }\n")
	if got := r.Resource.Spec["defaultBranch"]; got != "trunk" {
		t.Fatalf("defaultBranch = %v, want trunk", got)
	}
	// A vendored schema's defaults, reached through nested properties.
	r = admitYAML(t, `apiVersion: ai-sdlc.io/v1alpha1
kind: Pipeline
metadata: { name: ship, namespace: acme }
spec:
  triggers: [{ event: issue.assigned }]
  providers: {}
  stages:
    - name: implement
      credentials: { scope: ["repo:read"] }
  branching: { pattern: "lucy/{issueNumber}" }
`)
	wantReason(t, r, ReasonAdmitted)
	stage := r.Resource.Spec["stages"].([]any)[0].(map[string]any)
	creds := stage["credentials"].(map[string]any)
	if creds["ttl"] != "PT10M" || creds["revokeOnComplete"] != true {
		t.Fatalf("credential policy defaults (through $ref and array items) not applied: %v", creds)
	}
	if b := r.Resource.Spec["branching"].(map[string]any); b["targetBranch"] != "main" {
		t.Fatalf("branching defaults not applied: %v", b)
	}
}

func TestAdmitRefusals(t *testing.T) {
	cases := []struct {
		name, doc, reason string
	}{
		{"scalar document", "just text\n", ReasonNotAnObject},
		{"list document", "- a\n- b\n", ReasonNotAnObject},
		{"no kind", "apiVersion: lucy/v1alpha1\nmetadata: { name: a, namespace: a }\nspec: {}\n", ReasonInvalidEnvelope},
		{"metadata not a mapping", "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: acme\nspec: {}\n", ReasonInvalidEnvelope},
		{"unknown kind", "apiVersion: ai-sdlc.io/v1alpha1\nkind: Decision\nmetadata: { name: a, namespace: a }\nspec: {}\n", ReasonUnknownKind},
		{"lucy kind under the spec's group", "apiVersion: ai-sdlc.io/v1alpha1\nkind: Airspace\nmetadata: { name: a, namespace: a }\nspec: {}\n", ReasonUnknownKind},
		{"newer spec version", "apiVersion: ai-sdlc.io/v1alpha2\nkind: Pipeline\nmetadata: { name: a, namespace: a }\nspec: {}\n", ReasonUnsupportedAPIVersion},
		{"foreign group", "apiVersion: example.com/v1\nkind: Pipeline\nmetadata: { name: a, namespace: a }\nspec: {}\n", ReasonUnsupportedAPIVersion},
		{"status in config", "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: a, namespace: a }\nspec: {}\nstatus: { conditions: [] }\n", ReasonStatusInConfig},
		{"no namespace, core kind", "apiVersion: ai-sdlc.io/v1alpha1\nkind: AdapterBinding\nmetadata: { name: gh }\nspec: { interface: SourceControl, type: github, version: 1.0.0 }\n", ReasonMissingNamespace},
		{"no namespace, lucy kind", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p }\nspec: { role: worker }\n", ReasonMissingNamespace},
		{"unknown field", "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: a, namespace: a }\nspec: { colour: blue }\n", ReasonSchemaViolation},
		{"unknown top-level field", "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: a, namespace: a }\nspec: {}\nextra: 1\n", ReasonSchemaViolation},
		{"unknown metadata field", "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: a, namespace: a, uid: x }\nspec: {}\n", ReasonSchemaViolation},
		{"bad name", "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: Acme, namespace: Acme }\nspec: {}\n", ReasonSchemaViolation},
		{"wrong type", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p, namespace: a }\nspec: { role: 3 }\n", ReasonSchemaViolation},
		{"enum", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p, namespace: a }\nspec: { role: captain }\n", ReasonSchemaViolation},
		{"required spec field", "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p, namespace: a }\nspec: {}\n", ReasonSchemaViolation},
		{"format asserted", "apiVersion: ai-sdlc.io/v1alpha1\nkind: AgentRole\nmetadata: { name: a, namespace: a }\nspec: { role: r, goal: g, tools: [t], agentCard: { endpoint: 'not a uri', version: '1.0.0' } }\n", ReasonSchemaViolation},
		{"no spec", "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: a, namespace: a }\n", ReasonSchemaViolation},
		{"airspace outside itself", "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: other }\nspec: {}\n", ReasonNamespaceMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantReason(t, admitYAML(t, c.doc), c.reason)
		})
	}
}

func TestSchemaViolationReportsPaths(t *testing.T) {
	r := admitYAML(t, "apiVersion: lucy/v1alpha1\nkind: Pilot\nmetadata: { name: p, namespace: a }\nspec: { role: captain, extra: 1 }\n")
	wantReason(t, r, ReasonSchemaViolation)
	paths := map[string]bool{}
	for _, e := range r.Errors {
		paths[e.Path] = true
	}
	if !paths["/spec"] || !paths["/spec/role"] {
		t.Fatalf("errors %v, want /spec (extra) and /spec/role (enum)", r.Errors)
	}
}

// TestExtensionsExample checks the example in EXTENSIONS.md section 2 is valid config.
func TestExtensionsExample(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "EXTENSIONS.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := bytes.Cut(data, []byte("```yaml\n"))
	example, _, ok2 := bytes.Cut(rest, []byte("```"))
	if !ok || !ok2 {
		t.Fatal("no yaml example in EXTENSIONS.md")
	}
	for _, r := range LoadFile("EXTENSIONS.md", example) {
		wantReason(t, r, ReasonAdmitted)
	}
}

func TestDecodeRefusesHostileYAML(t *testing.T) {
	cases := map[string]string{
		"anchor and alias": "a: &x [1, 2]\nb: *x\n",
		"anchor alone":     "a: &x 1\n",
		"alias bomb":       "a: &a [x, x]\nb: &b [*a, *a]\nc: &c [*b, *b]\n",
		"merge key":        "base: {a: 1}\nm:\n  <<: {a: 2}\n",
		"custom tag":       "a: !secret value\n",
		"binary tag":       "a: !!binary aGVsbG8=\n",
		"set tag":          "a: !!set {x: null}\n",
		"duplicate key":    "a: 1\na: 2\n",
		"non-scalar key":   "? [a, b]\n: 1\n",
		"null key":         "~: 1\n",
		"NaN":              "a: .nan\n",
		"infinity":         "a: -.inf\n",
		"syntax error":     "a: [1, 2\n",
		"tab indentation":  "a:\n\tb: 1\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(doc)); !errors.Is(err, ErrDecode) {
				t.Fatalf("Decode = %v, want ErrDecode", err)
			}
		})
	}
}

func TestDecodeLimits(t *testing.T) {
	deep := strings.Repeat("[", MaxDecodeDepth+2) + strings.Repeat("]", MaxDecodeDepth+2)
	if _, err := Decode([]byte(deep)); !errors.Is(err, ErrDecode) {
		t.Errorf("deep nesting: %v", err)
	}
	big := bytes.Repeat([]byte("#"), MaxFileBytes+1)
	if _, err := Decode(big); !errors.Is(err, ErrDecode) {
		t.Errorf("oversized file: %v", err)
	}
	many := strings.Repeat("a: 1\n---\n", MaxDocuments+1)
	if _, err := Decode([]byte(many)); !errors.Is(err, ErrDecode) {
		t.Errorf("too many documents: %v", err)
	}
	wide := "[" + strings.Repeat("1,", MaxNodes) + "1]"
	if _, err := Decode([]byte(wide)); !errors.Is(err, ErrDecode) {
		t.Errorf("too many nodes: %v", err)
	}
}

func TestDecodeValues(t *testing.T) {
	docs, err := Decode([]byte("---\n# empty first document\n---\na: 1\nb: 2.5\nc: 1e3\nd: true\ne: null\nf: 2026-02-07T10:00:00Z\ng: '007'\nh: 0x10\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 {
		t.Fatalf("want empty documents skipped, got %d", len(docs))
	}
	m := docs[0].(map[string]any)
	want := map[string]string{"a": "1", "b": "2.5", "c": "1000", "d": "true", "e": "<nil>", "f": "2026-02-07T10:00:00Z", "g": "007", "h": "16"}
	for k, w := range want {
		if got := fmt.Sprint(m[k]); got != w {
			t.Errorf("%s = %s (%T), want %s", k, got, m[k], w)
		}
	}
	// JSON is read through the same decoder.
	docs, err = Decode([]byte(`{"apiVersion": "lucy/v1alpha1", "n": [1, 2]}`))
	if err != nil || len(docs) != 1 {
		t.Fatalf("JSON: %v", err)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(airspace))
	f.Add([]byte("a: &x 1\nb: *x\n"))
	f.Add([]byte("- [1, {a: b}]\n---\nx: 2026-01-01\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		docs, err := Decode(data)
		if err != nil {
			return
		}
		for i, d := range docs {
			Admit(d, Source{File: "fuzz.yaml", Doc: i + 1}) // must not panic
		}
	})
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "acme/airspace.yaml", airspace)
	writeFile(t, dir, "acme/runways.yaml", "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: {}\n---\napiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: api, namespace: acme }\nspec: { colour: red }\n")
	writeFile(t, dir, "acme/again.json", `{"apiVersion": "lucy/v1alpha1", "kind": "Runway", "metadata": {"name": "web", "namespace": "acme"}, "spec": {}}`)
	writeFile(t, dir, "acme/README.md", "not config")
	writeFile(t, dir, ".hidden/x.yaml", "broken: [")
	writeFile(t, dir, "acme/.draft.yaml", "broken: [")
	writeFile(t, dir, "broken.yml", "a: [1\n")
	if err := os.Symlink(filepath.Join(dir, "acme", "airspace.yaml"), filepath.Join(dir, "link.yaml")); err != nil {
		t.Fatal(err)
	}
	set, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range set.Results {
		c, _ := r.Condition(ConditionValid)
		got[r.Source.String()] = c.Reason
	}
	// Walk order is lexical, so again.json defines acme/Runway/web first and runways.yaml#1
	// is the duplicate.
	want := map[string]string{
		"acme/again.json#1":    ReasonAdmitted,
		"acme/airspace.yaml#1": ReasonAdmitted,
		"acme/runways.yaml#1":  ReasonDuplicate,
		"acme/runways.yaml#2":  ReasonSchemaViolation,
		"broken.yml":           ReasonDecodeError,
		"link.yaml":            ReasonSymlink,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("results\n got  %v\n want %v", got, want)
	}
	if set.Invalid() != 4 || len(set.Admitted()) != 2 {
		t.Fatalf("invalid %d, admitted %d", set.Invalid(), len(set.Admitted()))
	}
	for _, r := range set.Results {
		if r.Source.String() == "acme/runways.yaml#1" && !strings.Contains(r.Conditions[0].Message, "already defined in acme/again.json#1") {
			t.Fatalf("duplicate message %q", r.Conditions[0].Message)
		}
	}
}

func TestLoadDirLimits(t *testing.T) {
	if _, err := LoadDir(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrLoad) {
		t.Fatalf("missing dir: %v", err)
	}
	file := filepath.Join(t.TempDir(), "f.yaml")
	writeFile(t, filepath.Dir(file), "f.yaml", airspace)
	if _, err := LoadDir(file); !errors.Is(err, ErrLoad) {
		t.Fatalf("a file: %v", err)
	}

	dir := t.TempDir()
	writeFile(t, dir, "big.yaml", "a: "+strings.Repeat("x", MaxFileBytes)+"\n")
	set, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := set.Results[0].Condition(ConditionValid); c.Reason != ReasonTooLarge {
		t.Fatalf("oversized file: %+v", set.Results[0])
	}

	deep := t.TempDir()
	writeFile(t, deep, strings.Repeat("d/", MaxDirDepth+1)+"x.yaml", airspace)
	set, err = LoadDir(deep)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Results) != 1 || set.Results[0].Conditions[0].Reason != ReasonTooLarge {
		t.Fatalf("deep tree: %+v", set.Results)
	}

	many := t.TempDir()
	for i := range MaxFiles + 1 {
		writeFile(t, many, fmt.Sprintf("f%04d.yaml", i), "a: 1\n")
	}
	if _, err := LoadDir(many); !errors.Is(err, ErrLoad) {
		t.Fatalf("too many files: %v", err)
	}
}
