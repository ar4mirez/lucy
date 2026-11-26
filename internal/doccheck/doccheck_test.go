package doccheck

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite docs/requirements.md from the PRD tables")

const repoRoot = "../.."

// TestRepoDocs enforces the documentation rules on this repository.
func TestRepoDocs(t *testing.T) {
	for _, err := range Check(repoRoot) {
		t.Error(err)
	}
}

// TestRequirementsIndex checks docs/requirements.md; with -update it rewrites it.
func TestRequirementsIndex(t *testing.T) {
	docs, err := Load(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range docs {
		d.Scalars, d.Lists, d.Body, _ = ParseFrontMatter(d.Raw)
		d.Requirements = d.Lists["requirements"]
	}
	reqs, errs := Requirements(docs)
	for _, err := range errs {
		t.Error(err)
	}
	want := RenderIndex(reqs)
	p := filepath.Join(repoRoot, IndexPath)
	if *update {
		if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(p)
	if err != nil || string(got) != want {
		t.Errorf("%s is stale: run `mise run docs-index`", IndexPath)
	}
}

func TestParseFrontMatter(t *testing.T) {
	good := "---\nid: x\ntitle: \"D1: Go\"\nstatus: draft\nversion: 2\nowner: me\nrequirements: [LUCY-API-001, LUCY-API-002]\n# a comment\n---\n\n# Body\n"
	s, l, body, err := ParseFrontMatter(good)
	if err != nil {
		t.Fatal(err)
	}
	if s["title"] != "D1: Go" || s["version"] != "2" || len(l["requirements"]) != 2 || !strings.HasPrefix(body, "\n# Body") {
		t.Errorf("parsed %v %v %q", s, l, body)
	}
	bad := map[string]string{
		"no front matter":    "# Title\n",
		"not closed":         "---\nid: x\n",
		"unquoted colon":     "---\ntitle: D1: Go\n---\n",
		"nested yaml":        "---\nid:\n  - x\n---\n",
		"repeated key":       "---\nid: a\nid: b\n---\n",
		"multi-line list":    "---\nrequirements: [a,\n---\n",
		"inner quote":        "---\ntitle: \"a\"b\"\n---\n",
		"starts with symbol": "---\ntitle: *anchor\n---\n",
	}
	for name, raw := range bad {
		if _, _, _, err := ParseFrontMatter(raw); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// fixture writes a minimal valid tree and returns its root.
func fixture(t *testing.T, edits map[string]string) string {
	t.Helper()
	fm := func(id, reqs string) string {
		return "---\nid: " + id + "\ntitle: T\nstatus: active\nversion: 1\nowner: me\nrequirements: [" + reqs + "]\n---\n"
	}
	files := map[string]string{
		"CLAUDE.md":                 ClaudeContent,
		"CHANGES.md":                fm("changes", "") + "\n## Unreleased\n",
		"README.md":                 fm("readme", "") + "\nSee [the PRD](docs/prd/core.md) and LUCY-API-001.\n",
		"docs/prd/README.md":        fm("prd", "") + "\n# PRD\n",
		"docs/prd/core.md":          fm("prd-core", "LUCY-API-001, LUCY-API-002") + "\n| ID | Requirement | Milestone | Source |\n|---|---|---|---|\n| LUCY-API-001 | One API | m0 | P1-8 |\n| LUCY-API-002 | Remote API | m1 | D5 |\n",
		"docs/milestones/README.md": fm("milestones", "") + "\n# Milestones\n",
		"docs/milestones/m0.md":     fm("m0", "LUCY-API-001") + "\n# m0\n",
		"docs/milestones/m1.md":     fm("m1", "LUCY-API-002") + "\n# m1\n",
	}
	for k, v := range edits {
		files[k] = v
	}
	root := t.TempDir()
	for name, content := range files {
		if content == "" {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := edits[IndexPath]; !ok {
		docs, _ := Load(root)
		for _, d := range docs {
			d.Scalars, d.Lists, d.Body, _ = ParseFrontMatter(d.Raw)
			d.Requirements = d.Lists["requirements"]
		}
		reqs, _ := Requirements(docs)
		if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, IndexPath), []byte(RenderIndex(reqs)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCheckFixtures(t *testing.T) {
	if errs := Check(fixture(t, nil)); len(errs) > 0 {
		t.Fatalf("valid fixture failed: %v", errs)
	}
	fm := "---\nid: extra\ntitle: T\nstatus: active\nversion: 1\nowner: me\nrequirements: []\n---\n"
	cases := []struct {
		name  string
		edits map[string]string
		want  string
	}{
		{"claude duplicates agents", map[string]string{"CLAUDE.md": "# Instructions\n"}, "must be exactly"},
		{"missing front matter", map[string]string{"docs/x.md": "# X\n"}, "missing front matter"},
		{"missing key", map[string]string{"docs/x.md": "---\nid: x\ntitle: T\nstatus: active\nversion: 1\nrequirements: []\n---\n"}, `missing "owner"`},
		{"bad status", map[string]string{"docs/x.md": strings.Replace(fm, "active", "wip", 1)}, "status"},
		{"bad version", map[string]string{"docs/x.md": strings.Replace(fm, "version: 1", "version: v1", 1)}, "positive integer"},
		{"duplicate id", map[string]string{"docs/x.md": strings.Replace(fm, "id: extra", "id: readme", 1)}, "already used"},
		{"too many lines", map[string]string{"docs/x.md": fm + strings.Repeat("line\n", MaxLines)}, "line cap"},
		{"too many bytes", map[string]string{"docs/x.md": fm + strings.Repeat("x", MaxBytes)}, "byte cap"},
		{"undefined mention", map[string]string{"docs/x.md": fm + "LUCY-API-999\n"}, "no PRD table defines"},
		{"undefined front matter id", map[string]string{"docs/x.md": strings.Replace(fm, "[]", "[LUCY-API-998]", 1)}, "no PRD table defines"},
		{"broken link", map[string]string{"docs/x.md": fm + "[gone](nope.md)\n"}, "does not resolve"},
		{"empty id", map[string]string{"docs/x.md": strings.Replace(fm, "id: extra", "id:", 1)}, "id must not be empty"},
		{"empty title", map[string]string{"docs/x.md": strings.Replace(fm, "title: T", "title:", 1)}, "title must not be empty"},
		{"link to parent of repo", map[string]string{"docs/x.md": fm + "[up](../..)\n"}, "leaves the repository"},
		{"link leaves repo", map[string]string{"docs/x.md": fm + "[up](../../etc/passwd)\n"}, "leaves the repository"},
		{"unscheduled", map[string]string{"docs/milestones/m1.md": strings.Replace(fm, "id: extra", "id: m1", 1)}, "not scheduled"},
		{"wrong milestone", map[string]string{
			"docs/milestones/m0.md": strings.Replace(strings.Replace(fm, "id: extra", "id: m0", 1), "[]", "[LUCY-API-001, LUCY-API-002]", 1),
			"docs/milestones/m1.md": strings.Replace(fm, "id: extra", "id: m1", 1),
		}, "is m1 in"},
		{"scheduled twice", map[string]string{"docs/milestones/m2.md": strings.Replace(strings.Replace(fm, "id: extra", "id: m2", 1), "[]", "[LUCY-API-001]", 1)}, "already scheduled"},
		{"front matter out of sync with table", map[string]string{"docs/prd/core.md": strings.Replace(fm, "id: extra", "id: prd-core", 1) + "| LUCY-API-001 | One API | m0 | P1-8 |\n"}, "exactly the IDs"},
		{"stale index", map[string]string{IndexPath: fm + "old\n"}, "stale"},
		{"changes without unreleased", map[string]string{"CHANGES.md": strings.Replace(fm, "id: extra", "id: changes", 1) + "## v1\n"}, "Unreleased"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := Check(fixture(t, tc.edits))
			for _, err := range errs {
				if strings.Contains(err.Error(), tc.want) {
					return
				}
			}
			t.Errorf("want an error containing %q, got %v", tc.want, errs)
		})
	}
}
