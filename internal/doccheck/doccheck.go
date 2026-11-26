// Package doccheck enforces lucy's documentation rules (AGENTIC.md, section
// "Documentation rules"): front matter on every doc, a size cap per file,
// stable requirement IDs defined once and scheduled in exactly one milestone,
// a generated requirements index, and relative links that resolve.
package doccheck

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Size cap for every Markdown file. Split by area instead of growing a file.
const (
	MaxLines = 400
	MaxBytes = 24 * 1024
)

// Paths relative to the repository root.
const (
	IndexPath     = "docs/requirements.md"
	PRDDir        = "docs/prd"
	MilestonesDir = "docs/milestones"
	ChangesPath   = "CHANGES.md"
	ClaudePath    = "CLAUDE.md"
	ClaudeContent = "@AGENTS.md\n"
)

var (
	// RequirementID is the stable requirement ID format, for example LUCY-API-001.
	RequirementID = regexp.MustCompile(`^LUCY-[A-Z]{2,5}-[0-9]{3}$`)
	reqInText     = regexp.MustCompile(`\bLUCY-[A-Z]{2,5}-[0-9]{3}\b`)
	docID         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	milestoneName = regexp.MustCompile(`^m[0-9]$`)
	reqRow        = regexp.MustCompile(`^\|\s*(LUCY-[A-Z]{2,5}-[0-9]{3})\s*\|(.*)\|\s*$`)
	mdLink        = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	keyLine       = regexp.MustCompile(`^([a-z][a-z0-9_]*):(?:\s+(.*))?$`)

	requiredKeys = []string{"id", "title", "status", "version", "owner", "requirements"}
	statuses     = []string{"draft", "active", "accepted", "superseded", "deprecated"}
	skipDirs     = []string{".git", ".github", ".claude", ".no-mistakes", "bin", "dist", "node_modules", "testdata"}
)

// Doc is one Markdown file with its parsed front matter.
type Doc struct {
	Path         string // slash-separated, relative to the root
	Raw          string
	Scalars      map[string]string
	Lists        map[string][]string
	Requirements []string
	Body         string
}

// Requirement is one row of a PRD requirement table.
type Requirement struct {
	ID        string
	Text      string
	Milestone string
	File      string // the PRD file that defines it
}

// Load reads every Markdown file under root, skipping tooling directories.
func Load(root string) ([]*Doc, error) {
	var docs []*Doc
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && slices.Contains(skipDirs, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		docs = append(docs, &Doc{Path: filepath.ToSlash(rel), Raw: string(b)})
		return nil
	})
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	return docs, err
}

// ParseFrontMatter parses lucy's front matter: a restricted YAML subset of
// `key: scalar` and `key: [a, b]` lines between two `---` lines. Every file
// it accepts is also valid YAML.
func ParseFrontMatter(raw string) (scalars map[string]string, lists map[string][]string, body string, err error) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---\n") {
		return nil, nil, raw, fmt.Errorf("missing front matter: the file must start with a `---` line")
	}
	end := strings.Index(raw[4:], "\n---\n")
	if end < 0 {
		return nil, nil, raw, fmt.Errorf("front matter is not closed by a `---` line")
	}
	block := raw[4 : 4+end]
	body = raw[4+end+5:]
	scalars, lists = map[string]string{}, map[string][]string{}
	for i, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := keyLine.FindStringSubmatch(line)
		if m == nil {
			return nil, nil, body, fmt.Errorf("front matter line %d: want `key: value` or `key: [a, b]`, got %q", i+2, line)
		}
		key, val := m[1], strings.TrimSpace(m[2])
		if _, dup := scalars[key]; dup {
			return nil, nil, body, fmt.Errorf("front matter key %q repeated", key)
		}
		if _, dup := lists[key]; dup {
			return nil, nil, body, fmt.Errorf("front matter key %q repeated", key)
		}
		if strings.HasPrefix(val, "[") {
			items, err := parseList(val)
			if err != nil {
				return nil, nil, body, fmt.Errorf("front matter key %q: %w", key, err)
			}
			lists[key] = items
			continue
		}
		s, err := parseScalar(val)
		if err != nil {
			return nil, nil, body, fmt.Errorf("front matter key %q: %w", key, err)
		}
		scalars[key] = s
	}
	return scalars, lists, body, nil
}

func parseList(val string) ([]string, error) {
	if !strings.HasSuffix(val, "]") {
		return nil, fmt.Errorf("a list must open and close on one line: %q", val)
	}
	inner := strings.TrimSpace(val[1 : len(val)-1])
	if inner == "" {
		return []string{}, nil
	}
	var items []string
	for _, part := range strings.Split(inner, ",") {
		s, err := parseScalar(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		if s == "" {
			return nil, fmt.Errorf("empty list item in %q", val)
		}
		items = append(items, s)
	}
	return items, nil
}

func parseScalar(val string) (string, error) {
	if strings.HasPrefix(val, `"`) {
		if len(val) < 2 || !strings.HasSuffix(val, `"`) || strings.ContainsAny(val[1:len(val)-1], "\"\\") {
			return "", fmt.Errorf("a quoted value must be one plain double-quoted string: %q", val)
		}
		return val[1 : len(val)-1], nil
	}
	if val != "" && strings.ContainsRune("{}[]&*!|>'%@`#,", rune(val[0])) {
		return "", fmt.Errorf("quote a value that starts with %q: %q", val[0], val)
	}
	if strings.Contains(val, ": ") || strings.Contains(val, " #") || strings.HasSuffix(val, ":") {
		return "", fmt.Errorf("quote a value that contains `: ` or ` #`: %q", val)
	}
	return val, nil
}

// Check runs every documentation rule against the tree at root and returns
// one error per violation.
func Check(root string) []error {
	docs, err := Load(root)
	if err != nil {
		return []error{err}
	}
	var errs []error
	fail := func(p, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", p, fmt.Sprintf(format, args...)))
	}

	ids := map[string]string{}
	for _, d := range docs {
		if n := strings.Count(d.Raw, "\n"); n > MaxLines {
			fail(d.Path, "%d lines, over the %d-line cap: split it by area", n, MaxLines)
		}
		if len(d.Raw) > MaxBytes {
			fail(d.Path, "%d bytes, over the %d-byte cap: split it by area", len(d.Raw), MaxBytes)
		}
		if d.Path == ClaudePath {
			if d.Raw != ClaudeContent {
				fail(d.Path, "must be exactly %q so it imports AGENTS.md without duplicating it", ClaudeContent)
			}
			continue
		}
		scalars, lists, body, err := ParseFrontMatter(d.Raw)
		if err != nil {
			fail(d.Path, "%v", err)
			continue
		}
		d.Scalars, d.Lists, d.Body = scalars, lists, body
		for _, k := range requiredKeys {
			_, s := scalars[k]
			_, l := lists[k]
			if !s && !l {
				fail(d.Path, "front matter is missing %q", k)
			}
		}
		if _, ok := scalars["requirements"]; ok {
			fail(d.Path, "front matter \"requirements\" must be a list, for example `requirements: []`")
		}
		if id := scalars["id"]; id != "" {
			if !docID.MatchString(id) {
				fail(d.Path, "id %q must match %s", id, docID)
			} else if prev, dup := ids[id]; dup {
				fail(d.Path, "id %q is already used by %s", id, prev)
			} else {
				ids[id] = d.Path
			}
		}
		if st, ok := scalars["status"]; ok && !slices.Contains(statuses, st) {
			fail(d.Path, "status %q is not one of %v", st, statuses)
		}
		if v, ok := scalars["version"]; ok {
			if n, err := strconv.Atoi(v); err != nil || n < 1 {
				fail(d.Path, "version %q must be a positive integer", v)
			}
		}
		if o, ok := scalars["owner"]; ok && o == "" {
			fail(d.Path, "owner must not be empty")
		}
		d.Requirements = lists["requirements"]
		for _, r := range d.Requirements {
			if !RequirementID.MatchString(r) {
				fail(d.Path, "requirement %q must match %s", r, RequirementID)
			}
		}
		errs = append(errs, checkLinks(root, d)...)
	}

	reqs, reqErrs := Requirements(docs)
	errs = append(errs, reqErrs...)
	errs = append(errs, checkTraceability(docs, reqs)...)
	errs = append(errs, checkChanges(docs)...)

	want := RenderIndex(reqs)
	if got, err := os.ReadFile(filepath.Join(root, IndexPath)); err != nil {
		fail(IndexPath, "missing: run `mise run docs-index`")
	} else if string(got) != want {
		fail(IndexPath, "stale: run `mise run docs-index` and commit the result")
	}
	return errs
}

// Requirements parses the requirement tables in docs/prd/*.md (except
// README.md). A requirement row is `| LUCY-XXX-NNN | text | mN | source |`.
func Requirements(docs []*Doc) ([]Requirement, []error) {
	var reqs []Requirement
	var errs []error
	seen := map[string]string{}
	for _, d := range docs {
		if path.Dir(d.Path) != PRDDir || path.Base(d.Path) == "README.md" || d.Scalars == nil {
			continue
		}
		var defined []string
		for i, line := range strings.Split(d.Body, "\n") {
			m := reqRow.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			cells := strings.Split(m[2], "|")
			if len(cells) != 3 {
				errs = append(errs, fmt.Errorf("%s: body line %d: a requirement row needs 4 cells (ID, requirement, milestone, source)", d.Path, i+1))
				continue
			}
			r := Requirement{ID: m[1], Text: strings.TrimSpace(cells[0]), Milestone: strings.TrimSpace(cells[1]), File: d.Path}
			if prev, dup := seen[r.ID]; dup {
				errs = append(errs, fmt.Errorf("%s: %s is already defined in %s", d.Path, r.ID, prev))
				continue
			}
			if !milestoneName.MatchString(r.Milestone) {
				errs = append(errs, fmt.Errorf("%s: %s milestone %q must be m0 to m9", d.Path, r.ID, r.Milestone))
			}
			if r.Text == "" {
				errs = append(errs, fmt.Errorf("%s: %s has no requirement text", d.Path, r.ID))
			}
			seen[r.ID] = d.Path
			defined = append(defined, r.ID)
			reqs = append(reqs, r)
		}
		if !sameSet(defined, d.Requirements) {
			errs = append(errs, fmt.Errorf("%s: front matter requirements must list exactly the IDs this file defines: %v", d.Path, defined))
		}
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].ID < reqs[j].ID })
	return reqs, errs
}

// checkTraceability: every requirement is scheduled in exactly one milestone
// brief whose name matches its milestone cell, and every ID mentioned
// anywhere is defined.
func checkTraceability(docs []*Doc, reqs []Requirement) []error {
	var errs []error
	byID := map[string]Requirement{}
	for _, r := range reqs {
		byID[r.ID] = r
	}
	scheduled := map[string]string{}
	for _, d := range docs {
		if d.Scalars == nil {
			continue
		}
		for _, id := range d.Requirements {
			if _, ok := byID[id]; !ok && RequirementID.MatchString(id) {
				errs = append(errs, fmt.Errorf("%s: front matter lists %s, which no PRD table defines", d.Path, id))
			}
		}
		for _, id := range reqInText.FindAllString(d.Body, -1) {
			if _, ok := byID[id]; !ok {
				errs = append(errs, fmt.Errorf("%s: mentions %s, which no PRD table defines", d.Path, id))
			}
		}
		if path.Dir(d.Path) != MilestonesDir || !milestoneName.MatchString(strings.TrimSuffix(path.Base(d.Path), ".md")) {
			continue
		}
		ms := strings.TrimSuffix(path.Base(d.Path), ".md")
		for _, id := range d.Requirements {
			r, ok := byID[id]
			if !ok {
				continue
			}
			if prev, dup := scheduled[id]; dup {
				errs = append(errs, fmt.Errorf("%s: %s is already scheduled in %s", d.Path, id, prev))
				continue
			}
			scheduled[id] = d.Path
			if r.Milestone != ms {
				errs = append(errs, fmt.Errorf("%s: %s is %s in %s", d.Path, id, r.Milestone, r.File))
			}
		}
	}
	for _, r := range reqs {
		if _, ok := scheduled[r.ID]; !ok {
			errs = append(errs, fmt.Errorf("%s: %s is not scheduled in %s/%s.md", r.File, r.ID, MilestonesDir, r.Milestone))
		}
	}
	return errs
}

func checkLinks(root string, d *Doc) []error {
	var errs []error
	inFence := false
	for i, line := range strings.Split(d.Raw, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if inFence {
			continue
		}
		for _, m := range mdLink.FindAllStringSubmatch(line, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			p := path.Join(path.Dir(d.Path), target)
			if strings.HasPrefix(p, "../") {
				errs = append(errs, fmt.Errorf("%s:%d: link %q leaves the repository", d.Path, i+1, m[1]))
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
				errs = append(errs, fmt.Errorf("%s:%d: link %q does not resolve", d.Path, i+1, m[1]))
			}
		}
	}
	return errs
}

func checkChanges(docs []*Doc) []error {
	for _, d := range docs {
		if d.Path != ChangesPath || d.Scalars == nil {
			continue
		}
		for _, line := range strings.Split(d.Body, "\n") {
			if strings.HasPrefix(line, "## ") {
				if line != "## Unreleased" {
					return []error{fmt.Errorf("%s: the first section must be `## Unreleased`, got %q", d.Path, line)}
				}
				return nil
			}
		}
		return []error{fmt.Errorf("%s: missing the `## Unreleased` section", d.Path)}
	}
	return []error{fmt.Errorf("%s: missing", ChangesPath)}
}

// RenderIndex returns the generated content of docs/requirements.md.
func RenderIndex(reqs []Requirement) string {
	var b strings.Builder
	b.WriteString("---\nid: requirements-index\ntitle: Requirements index\nstatus: active\nversion: 1\nowner: ar4mirez\nrequirements: []\ngenerated: true\n---\n\n")
	b.WriteString("# Requirements index\n\n")
	b.WriteString("Generated from the requirement tables in [docs/prd](prd/README.md). Do not edit by hand:\n")
	b.WriteString("run `mise run docs-index` after changing a PRD table. CI fails when this file is stale.\n\n")
	counts := map[string]int{}
	for _, r := range reqs {
		counts[r.Milestone]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", k, counts[k]))
	}
	fmt.Fprintf(&b, "%d requirements (%s). Milestone briefs: [docs/milestones](milestones/README.md).\n", len(reqs), strings.Join(parts, ", "))
	byFile := map[string][]Requirement{}
	var files []string
	for _, r := range reqs {
		if _, ok := byFile[r.File]; !ok {
			files = append(files, r.File)
		}
		byFile[r.File] = append(byFile[r.File], r)
	}
	sort.Strings(files)
	for _, f := range files {
		fmt.Fprintf(&b, "\n## [%s](%s)\n\n| ID | Milestone | Requirement |\n|---|---|---|\n", strings.TrimSuffix(path.Base(f), ".md"), strings.TrimPrefix(f, "docs/"))
		for _, r := range byFile[f] {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", r.ID, r.Milestone, r.Text)
		}
	}
	return b.String()
}

// sameSet reports whether a and b hold the same items, each exactly once.
func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y) && len(slices.Compact(x)) == len(y)
}
