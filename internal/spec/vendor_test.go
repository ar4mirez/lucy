package spec

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const provenanceFile = "schemas/ai-sdlc/PROVENANCE"

// vendoredDirs are the directories whose every file must be listed in PROVENANCE, relative to
// the repository root.
var vendoredDirs = []string{
	"internal/spec/schemas/ai-sdlc/v1alpha1",
	"internal/spec/testdata/examples",
	"internal/audit/testdata/audit-chain",
}

// TestVendoredProvenance fails when a vendored file changes without PROVENANCE changing with it,
// a listed file is missing, or a file in a vendored directory is not listed (LUCY-SPEC-004).
func TestVendoredProvenance(t *testing.T) {
	root := filepath.Join("..", "..")
	f, err := os.Open(provenanceFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	listed := map[string]bool{}
	var commit string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "source", "licence":
		case "commit":
			commit = fields[1]
		case "file":
			if len(fields) != 4 {
				t.Fatalf("PROVENANCE: malformed line %q", line)
			}
			sum, path := fields[1], fields[2]
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
			if err != nil {
				t.Errorf("PROVENANCE lists %s: %v", path, err)
				continue
			}
			got := sha256.Sum256(data)
			if hex.EncodeToString(got[:]) != sum {
				t.Errorf("%s changed: its SHA-256 is %x, PROVENANCE says %s; vendored files are copied verbatim from the pinned commit", path, got, sum)
			}
			listed[path] = true
		default:
			t.Fatalf("PROVENANCE: unknown line %q", line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if commit != SpecCommit {
		t.Errorf("PROVENANCE pins commit %s, the code pins %s", commit, SpecCommit)
	}
	for _, d := range vendoredDirs {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(d)), func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			if !listed[filepath.ToSlash(rel)] {
				t.Errorf("%s is in a vendored directory but not in PROVENANCE", filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchemasCompile(t *testing.T) {
	if err := CheckSchemas(); err != nil {
		t.Fatal(err)
	}
	for _, k := range Kinds() {
		if _, err := schemaFor(k.Schema); err != nil {
			t.Errorf("%s: %v", k.Name, err)
		}
	}
	if _, err := schemaFor(AuditEntrySchema); err != nil {
		t.Error(err)
	}
}

// TestSpecExamples validates the spec's own examples (spec.md sections 5.1.3 to 5.5.3) against the
// vendored schemas (LUCY-SPEC-011, m0 exit test 2). The examples carry status, as the spec shows
// a reconciled resource, so this checks the schemas, not lucy's admission rules.
func TestSpecExamples(t *testing.T) {
	files, err := filepath.Glob("testdata/examples/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 5 {
		t.Fatalf("want the 5 core examples, found %d", len(files))
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		docs, err := Decode(data)
		if err != nil || len(docs) != 1 {
			t.Fatalf("%s: %v (%d documents)", f, err, len(docs))
		}
		obj := docs[0].(map[string]any)
		k, ok := LookupKind(obj["apiVersion"].(string), obj["kind"].(string))
		if !ok || !k.Core {
			t.Fatalf("%s: not a core kind", f)
		}
		errs, err := Validate(k.Schema, obj)
		if err != nil || len(errs) > 0 {
			t.Errorf("%s does not validate against %s: %v %v", f, k.Schema, err, errs)
		}
	}
}
