package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "rewrite openrpc.json from the method table")

// TestOpenRPCDocument checks the committed OpenRPC document matches the method table; with
// -update (`mise run api-schema`) it rewrites it.
func TestOpenRPCDocument(t *testing.T) {
	want, err := OpenRPC()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile("openrpc.json", want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(Document, want) {
		t.Error("internal/api/openrpc.json is stale: run `mise run api-schema`")
	}
	var doc struct {
		OpenRPC string `json:"openrpc"`
		Methods []struct {
			Name   string `json:"name"`
			Params []struct {
				Name string `json:"name"`
			} `json:"params"`
		} `json:"methods"`
	}
	if err := json.Unmarshal(want, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenRPC == "" || len(doc.Methods) != len(Methods()) {
		t.Errorf("document has %d methods, table has %d", len(doc.Methods), len(Methods()))
	}
}

// TestMethodTable checks every declared method is well formed: a valid name, a scope, a params
// struct whose bounds parse, and a result type the schema generator understands.
func TestMethodTable(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Methods() {
		if !methodName.MatchString(m.Name) || seen[m.Name] {
			t.Errorf("bad or duplicate method name %q", m.Name)
		}
		seen[m.Name] = true
		if m.Scope == "" || m.Summary == "" || m.Group == "" {
			t.Errorf("%s: missing scope, summary or group", m.Name)
		}
		if m.Mutating && m.Group != "command" {
			t.Errorf("%s: a mutating method must be a command", m.Name)
		}
		if err := DecodeParams(nil, m.NewParams()); err != nil && m.Name != MethodHello && m.Name != MethodEvaluate {
			t.Errorf("%s: empty params refused: %v (only methods with required params may refuse them)", m.Name, err)
		}
		schemaOf(m.Params, bounds{}, true)
		if _, ok := Lookup(m.Name); !ok {
			t.Errorf("Lookup(%q) failed", m.Name)
		}
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("Lookup found an undeclared method")
	}
}
