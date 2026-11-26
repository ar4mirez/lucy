package workflowcheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const workflowsDir = "../../.github/workflows"

var (
	usesLine   = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)`)
	pinnedUses = regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	writeScope = regexp.MustCompile(`^\s+([a-z-]+):\s*write\b`)
)

func workflows(t *testing.T) map[string][]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(workflowsDir, "*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no workflows found in %s: %v", workflowsDir, err)
	}
	out := map[string][]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = strings.Split(string(b), "\n")
	}
	return out
}

func TestActionsPinnedBySHA(t *testing.T) {
	for name, lines := range workflows(t) {
		for i, l := range lines {
			if m := usesLine.FindStringSubmatch(l); m != nil && !pinnedUses.MatchString(m[1]) {
				t.Errorf("%s:%d: %s is not pinned by a full commit SHA", name, i+1, m[1])
			}
		}
	}
}

func TestDefaultPermissionsReadOnly(t *testing.T) {
	for name, lines := range workflows(t) {
		if !strings.Contains(strings.Join(lines, "\n"), "\npermissions:\n  contents: read\n") {
			t.Errorf("%s: want top-level `permissions:` with only `contents: read`", name)
		}
	}
}

// Write scopes exist only in release.yml's release job, and only these three.
func TestWriteScopesOnlyOnRelease(t *testing.T) {
	allowed := map[string]bool{"contents": true, "id-token": true, "attestations": true}
	for name, lines := range workflows(t) {
		job := ""
		for i, l := range lines {
			if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") && strings.HasSuffix(l, ":") {
				job = strings.TrimSuffix(strings.TrimSpace(l), ":")
			}
			m := writeScope.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			if name != "release.yml" || job != "release" || !allowed[m[1]] {
				t.Errorf("%s:%d: `%s: write` is only allowed on release.yml's release job (contents, id-token, attestations)", name, i+1, m[1])
			}
		}
	}
}

func TestReleaseOnlyOnVersionTags(t *testing.T) {
	lines := workflows(t)["release.yml"]
	if lines == nil {
		t.Fatal("release.yml is missing")
	}
	text := strings.Join(lines, "\n")
	want := "\non:\n  push:\n    tags: [\"v*\"]\n\n"
	if !strings.Contains(text, want) {
		t.Errorf("release.yml: want the only trigger to be `on: push: tags: [\"v*\"]`")
	}
	for _, trigger := range []string{"pull_request", "workflow_dispatch", "branches:", "schedule:", "workflow_run"} {
		if strings.Contains(text, trigger) {
			t.Errorf("release.yml: unexpected trigger %q", trigger)
		}
	}
}
