package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The oracle is LUCY-RUN-012 and docs/tech/shadow.md: lucy shadow import checks the whole file
// before importing anything, records each task of an admitted org once, and lucy shadow ls lists
// them by org, class and task. The file is a fixture; nothing is fetched.
func TestShadowImport(t *testing.T) {
	sock := startDaemonWith(t, func(db string) {
		res := filepath.Join(filepath.Dir(filepath.Dir(db)), "config", "lucy", "resources")
		if err := os.MkdirAll(res, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(res, "airspace.yaml"), []byte("apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\nspec: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	fixture := filepath.Join("..", "..", "internal", "shadow", "testdata", "tasks.jsonl")
	if code, out, errOut := lucy("shadow", "ls", "--socket", sock); code != 0 || out != "tasks[0]: none\n" {
		t.Fatalf("ls before: %d %q %q", code, out, errOut)
	}
	if code, out, errOut := lucy("shadow", "import", "--socket", sock, fixture); code != 0 || out != "imported: 2\nunchanged: 0\n" {
		t.Fatalf("import: %d %q %q", code, out, errOut)
	}
	if code, out, _ := lucy("shadow", "import", "--socket", sock, fixture); code != 0 || out != "imported: 0\nunchanged: 2\n" {
		t.Fatalf("import again: %d %q", code, out)
	}
	code, out, _ := lucy("shadow", "ls", "--socket", sock, "--org", "acme")
	if code != 0 || out != "tasks[2]{org,class,task,posture,actions}:\n  acme,bugfix,t-1,green,5\n  acme,docs,t-2,owner,3\n" {
		t.Fatalf("ls: %d %q", code, out)
	}

	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// A refused line refuses the whole file, before anything is sent.
	bad := write("bad.jsonl", string(data)+`{"v":1,"type":"task","org":"acme","task":"t-3","class":"docs","goal":"g","repository":"web","posture":"owner"}`+"\n"+
		`{"v":1,"type":"action","task":"t-3","at":"2026-11-22T00:00:00Z","action":"dispatched","diff":"+x"}`+"\n")
	if code, out, errOut := lucy("shadow", "import", "--socket", sock, bad); code != 1 || out != "" || !strings.Contains(errOut, "line 13") || !strings.Contains(errOut, "nothing was imported") {
		t.Fatalf("a refused file: %d %q %q", code, out, errOut)
	}
	// Another org's tasks are refused by the daemon.
	other := write("other.jsonl", strings.ReplaceAll(string(data), `"acme"`, `"beta"`))
	if code, out, errOut := lucy("shadow", "import", "--socket", sock, other); code != 1 || !strings.Contains(errOut, "no admitted Airspace") || !strings.Contains(out, "imported: 0") {
		t.Fatalf("another org: %d %q %q", code, out, errOut)
	}
	// A task id reused with other contents is refused.
	changed := write("changed.jsonl", strings.Replace(string(data), "fix the login page", "fix the sign-up page", 1))
	if code, _, errOut := lucy("shadow", "import", "--socket", sock, changed); code != 1 || !strings.Contains(errOut, "imported before with other contents") {
		t.Fatalf("changed: %d %q", code, errOut)
	}
	if code, out, _ := lucy("shadow", "ls", "--socket", sock); code != 0 || !strings.HasPrefix(out, "tasks[2]") {
		t.Fatalf("ls after: %d %q", code, out)
	}
}
