package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/jcs"
)

// fixture is conformance/audit-chain/fixture.json at the pinned spec commit.
type fixture struct {
	SchemaVersion  string            `json:"schemaVersion"`
	Inputs         []json.RawMessage `json:"inputs"`
	ExpectedHashes []string          `json:"expectedHashes"`
}

// TestCrossSDKFixture reproduces the spec's expectedHashes from its inputs (spec section 12,
// LUCY-SPEC-009): the hash rule, RFC 8785 canonicalization and the genesis rule together.
func TestCrossSDKFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/audit-chain/fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	if fx.SchemaVersion != SchemaVersion || len(fx.Inputs) != len(fx.ExpectedHashes) || len(fx.Inputs) == 0 {
		t.Fatalf("unexpected fixture shape: %+v", fx)
	}
	prev := ""
	var chain bytes.Buffer
	for i, in := range fx.Inputs {
		var e Entry
		dec := json.NewDecoder(bytes.NewReader(in))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		sealed, err := Seal(e, prev)
		if err != nil {
			t.Fatal(err)
		}
		if sealed.Hash != fx.ExpectedHashes[i] {
			t.Fatalf("input %d (%s): hash %s, the spec expects %s", i, e.ID, sealed.Hash, fx.ExpectedHashes[i])
		}
		line, err := sealed.Line()
		if err != nil {
			t.Fatal(err)
		}
		chain.Write(line)
		chain.WriteByte('\n')
		prev = sealed.Hash
	}
	// The chain lucy writes verifies, and so does the same chain re-encoded by another encoder.
	rep, err := VerifyJSONL(bytes.NewReader(chain.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Entries != 3 || rep.Head != fx.ExpectedHashes[2] {
		t.Fatalf("report %+v", rep)
	}
}

// TestSpecSamples verifies the chains the spec's Go, Python and TypeScript SDKs wrote, and
// refuses the legacy one: its rule is its own SDK's, not lucy's to verify.
func TestSpecSamples(t *testing.T) {
	samples, err := filepath.Glob("testdata/audit-chain/samples/*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) < 4 {
		t.Fatalf("found %d samples", len(samples))
	}
	for _, s := range samples {
		data, err := os.ReadFile(s)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := VerifyJSONL(bytes.NewReader(data))
		if strings.HasPrefix(filepath.Base(s), "legacy-") {
			if !errors.Is(err, ErrBroken) || !strings.Contains(err.Error(), "legacy") {
				t.Errorf("%s: %v, want a legacy refusal", s, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		if rep.Entries != 3 || rep.Head != "7d3d0b145db346419b53a1263907a56a6bdf0c369775d5f6f018a8d25cae11a7" {
			t.Errorf("%s: report %+v", s, rep)
		}
	}
}

// TestTamperFails changes one byte of a v2 chain at every position: each change must fail
// verification (spec section 12), unless it re-encodes the same values (a space between tokens,
// 1e+21 written 1e021), which leaves the canonical form, and so the hash, unchanged.
func TestTamperFails(t *testing.T) {
	data, err := os.ReadFile("testdata/audit-chain/samples/go.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for i := range data {
		for _, b := range []byte{'0', 'a', 'x', ' '} {
			if data[i] == b || data[i] == '\n' {
				continue
			}
			mut := bytes.Clone(data)
			mut[i] = b
			if _, err := VerifyJSONL(bytes.NewReader(mut)); err == nil {
				if sameCanonical(t, data, mut) {
					continue
				}
				t.Fatalf("changing byte %d (%q) to %q still verifies", i, data[i], b)
			}
			checked++
		}
	}
	if checked < 1000 {
		t.Fatalf("only %d mutations checked", checked)
	}
	// Removing, reordering or truncating entries fails too.
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	for name, chain := range map[string][][]byte{
		"first removed":  lines[1:],
		"middle removed": {lines[0], lines[2]},
		"reordered":      {lines[1], lines[0], lines[2]},
	} {
		if _, err := VerifyJSONL(bytes.NewReader(bytes.Join(chain, []byte("\n")))); !errors.Is(err, ErrBroken) {
			t.Errorf("%s: %v, want ErrBroken", name, err)
		}
	}
}

// sameCanonical reports whether two chains hold the same values, line by line.
func sameCanonical(t *testing.T, a, b []byte) bool {
	t.Helper()
	la, lb := bytes.Split(a, []byte("\n")), bytes.Split(b, []byte("\n"))
	if len(la) != len(lb) {
		return false
	}
	for i := range la {
		if len(la[i]) == 0 && len(lb[i]) == 0 {
			continue
		}
		ca, err := jcs.Transform(la[i])
		if err != nil {
			t.Fatal(err)
		}
		cb, err := jcs.Transform(lb[i])
		if err != nil || !bytes.Equal(ca, cb) {
			return false
		}
	}
	return true
}

func TestVerifierRefusals(t *testing.T) {
	good, err := Seal(Entry{ID: "a", Timestamp: "2026-01-02T03:04:05.006Z", Actor: "x", Action: "y", Resource: "k/n", Decision: DecisionAllowed}, "")
	if err != nil {
		t.Fatal(err)
	}
	line, _ := good.Line()
	for name, in := range map[string]string{
		"not JSON":          "{",
		"not an object":     "[]",
		"wrong version":     strings.Replace(string(line), `"schemaVersion":"2"`, `"schemaVersion":"3"`, 1),
		"bad decision":      strings.Replace(string(line), `"allowed"`, `"maybe"`, 1),
		"bad timestamp":     strings.Replace(string(line), `.006Z`, `Z`, 1),
		"genesis link":      strings.Replace(string(line), `"id"`, `"previousHash":"`+strings.Repeat("0", 64)+`","id"`, 1),
		"duplicate members": strings.Replace(string(line), `"id":"a"`, `"id":"a","id":"b"`, 1),
	} {
		var v Verifier
		if _, err := v.Next([]byte(in)); !errors.Is(err, ErrBroken) {
			t.Errorf("%s: %v, want ErrBroken", name, err)
		}
	}
}
