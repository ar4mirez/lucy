package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func instructions(t *testing.T, dir string) string {
	t.Helper()
	got, err := Instructions(dir)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestInstructionsFollowsImportsInsideTheRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "CLAUDE.md"), "main-text @AGENTS.md @docs/style.md\n")
	write(t, filepath.Join(dir, "AGENTS.md"), "agents-text\n")
	write(t, filepath.Join(dir, "docs", "style.md"), "style-text\n")
	got := instructions(t, dir)
	for _, want := range []string{"main-text", "agents-text", "style-text"} {
		if !strings.Contains(got, want) {
			t.Errorf("instructions lack %q: %q", want, got)
		}
	}
}

func TestInstructionsFallsBackToAgents(t *testing.T) {
	dir := t.TempDir()
	if got := instructions(t, dir); got != "" {
		t.Errorf("no instruction file gave %q", got)
	}
	write(t, filepath.Join(dir, "AGENTS.md"), "agents-only\n")
	if got := instructions(t, dir); got != "agents-only\n" {
		t.Errorf("instructions = %q, want the AGENTS.md text", got)
	}
}

func TestInstructionsDropsWhatIsNotInTheRepository(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "repo")
	write(t, filepath.Join(parent, "outside.md"), "outside-text\n")
	write(t, filepath.Join(dir, "a.md"), "a-text @b.md\n")
	write(t, filepath.Join(dir, "b.md"), "nested-import-text\n")
	write(t, filepath.Join(dir, "big.md"), "big-text"+strings.Repeat("x", MaxInstructionFile))
	if err := os.Symlink(filepath.Join(parent, "outside.md"), filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "CLAUDE.md"),
		"main-text @../outside.md @"+filepath.Join(parent, "outside.md")+" @link.md @a.md @big.md\n")
	got := instructions(t, dir)
	if !strings.Contains(got, "main-text") || !strings.Contains(got, "a-text") {
		t.Fatalf("instructions lack the repository's own text: %q", got)
	}
	for _, bad := range []string{"outside-text", "nested-import-text", "big-text"} {
		if strings.Contains(got, bad) {
			t.Errorf("instructions include %q: %q", bad, got)
		}
	}
}

func TestInstructionsCapsTheTotal(t *testing.T) {
	dir := t.TempDir()
	var main strings.Builder
	for i := range 5 {
		name := string(rune('a'+i)) + ".md"
		write(t, filepath.Join(dir, name), name+strings.Repeat("x", MaxInstructionFile-10))
		main.WriteString("@" + name + " ")
	}
	write(t, filepath.Join(dir, "CLAUDE.md"), main.String())
	got := instructions(t, dir)
	if len(got) > MaxInstructionTotal || !strings.Contains(got, "a.md") || strings.Contains(got, "e.md"+"x") {
		t.Errorf("instructions are %d bytes, cap %d; the last file must be dropped", len(got), MaxInstructionTotal)
	}
}
