package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const dir = ".claude/skills"

func home(t *testing.T) (string, *os.Root) {
	t.Helper()
	d := t.TempDir()
	r, err := os.OpenRoot(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return d, r
}

// The manifest lists every bundled file with the SHA-256 of its content, computed here
// independently of the code under test.
func TestManifest(t *testing.T) {
	files, err := Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("files %v, want one SKILL.md per Pilot role", files)
	}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join("builtin", filepath.FromSlash(f.Path)))
		if err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(b)
		if hex.EncodeToString(s[:]) != f.SHA256 {
			t.Errorf("%s: the manifest says %s", f.Path, f.SHA256)
		}
		if strings.Contains(string(b), "allowed-tools") {
			t.Errorf("%s pre-allows tools; a bundled skill never does", f.Path)
		}
	}
}

// Install puts exactly lucy's skills in the home, each verified; a second Install changes nothing.
func TestInstall(t *testing.T) {
	d, h := home(t)
	changed, err := Install(h, dir, h.RemoveAll)
	if err != nil || len(changed) != 3 {
		t.Fatalf("install: %v, %v", changed, err)
	}
	files, _ := Manifest()
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(d, dir, filepath.FromSlash(f.Path)))
		if err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256(b)
		if hex.EncodeToString(s[:]) != f.SHA256 {
			t.Errorf("%s installed with the wrong content", f.Path)
		}
	}
	if again, err := Install(h, dir, h.RemoveAll); err != nil || len(again) != 0 {
		t.Fatalf("a second install changed %v, %v", again, err)
	}
}

// Anything in the skills directory that is not a bundled skill is removed, a file of a bundled
// skill that is not in the manifest too, and a tampered skill is put back; a symlink there is
// removed, never followed.
func TestInstallRemovesTheUnknown(t *testing.T) {
	d, h := home(t)
	if _, err := Install(h, dir, h.RemoveAll); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "keep.md"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	sk := filepath.Join(d, dir)
	for name, content := range map[string]string{"owners-skill/SKILL.md": "x", "lucy-worker/extra.md": "y"} {
		p := filepath.Join(sk, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sk, "lucy-reviewer", "SKILL.md"), []byte("---\nallowed-tools: Bash\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(sk, "linked")); err != nil {
		t.Fatal(err)
	}
	changed, err := Install(h, dir, h.RemoveAll)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(changed, ",")
	for _, want := range []string{"removed owners-skill", "removed lucy-worker/extra.md", "removed linked", "installed lucy-reviewer/SKILL.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("changed %v lacks %q", changed, want)
		}
	}
	entries, _ := os.ReadDir(sk)
	if len(entries) != 3 {
		t.Errorf("the skills directory holds %v", entries)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "keep.md")); string(b) != "kept" {
		t.Fatal("a removed link's target was touched")
	}
}

// A skills directory, or a directory on the way to it, that is a symlink is refused.
func TestInstallRefusesALinkedDirectory(t *testing.T) {
	for _, link := range []string{".claude", ".claude/skills"} {
		d, h := home(t)
		if link == ".claude/skills" {
			if err := os.MkdirAll(filepath.Join(d, ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(d, link)); err != nil {
			t.Fatal(err)
		}
		if _, err := Install(h, dir, h.RemoveAll); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Errorf("a linked %s: %v", link, err)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("wrote through the linked %s: %v", link, entries)
		}
	}
}
