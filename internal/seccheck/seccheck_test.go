package seccheck

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	repoRoot     = "../.."
	baselinePath = "docs/security/reviews/sast-baseline.md"
	workflowPath = ".github/workflows/security.yml"
	misePath     = "mise.toml"
)

var (
	// anyNosec finds every gosec suppression comment, well formed or not.
	anyNosec = regexp.MustCompile(`//\s*#nosec\b.*`)
	// goodNosec is the only accepted form: one rule, then " -- " and a reason.
	goodNosec = regexp.MustCompile(`^//\s*#nosec (G\d{3}) -- \S.*$`)
	// nolintGosec is golangci-lint's way to silence gosec, which would bypass the baseline.
	nolintGosec = regexp.MustCompile(`//\s*nolint:[^/]*\bgosec\b`)
	// baselineRow is one entry of the reviewed baseline's table.
	baselineRow = regexp.MustCompile("^\\| `([^`]+)` \\| (G\\d{3}) \\| (.+) \\|$")
)

// suppression is one reviewed exception: a rule silenced in a file.
type suppression struct{ file, rule string }

func (s suppression) String() string { return s.file + " " + s.rule }

// scanCode returns every suppression in the Go files under root, and every malformed one.
func scanCode(root string) (found []suppression, problems []string, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (d.Name() == ".git" || d.Name() == "bin" || d.Name() == "dist") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if nolintGosec.MatchString(line) {
				problems = append(problems, fmt.Sprintf("%s:%d: silences gosec through nolint; use a reviewed nosec annotation", rel, i+1))
			}
			c := anyNosec.FindString(line)
			if c == "" {
				continue
			}
			m := goodNosec.FindStringSubmatch(c)
			if m == nil {
				problems = append(problems, fmt.Sprintf("%s:%d: %q needs one rule and a reason: `#nosec G<nnn> -- <reason>`", rel, i+1, c))
				continue
			}
			found = append(found, suppression{rel, m[1]})
		}
		return nil
	})
	return found, problems, err
}

// readBaseline returns the suppressions the reviewed baseline lists.
func readBaseline(path string) ([]suppression, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var listed []suppression
	for _, line := range strings.Split(string(src), "\n") {
		if m := baselineRow.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			listed = append(listed, suppression{m[1], m[2]})
		}
	}
	return listed, nil
}

// diff returns the entries of a missing from b, counting duplicates.
func diff(a, b []suppression) []string {
	left := map[suppression]int{}
	for _, s := range b {
		left[s]++
	}
	var out []string
	for _, s := range a {
		if left[s] > 0 {
			left[s]--
			continue
		}
		out = append(out, s.String())
	}
	sort.Strings(out)
	return out
}

func TestSuppressionsAreReviewed(t *testing.T) {
	found, problems, err := scanCode(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	listed, err := readBaseline(filepath.Join(repoRoot, baselinePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range diff(found, listed) {
		t.Errorf("%s: suppressed in the code but not in %s; review it and add a row", s, baselinePath)
	}
	for _, s := range diff(listed, found) {
		t.Errorf("%s: listed in %s but not suppressed in the code; remove the row", s, baselinePath)
	}
}

// scanner names a tool CI's security workflow installs: its env var there and its package path.
type scanner struct{ env, pkg string }

var scanners = []scanner{
	{"GOVULNCHECK_VERSION", "golang.org/x/vuln/cmd/govulncheck"},
	{"GOSEC_VERSION", "github.com/securego/gosec/v2/cmd/gosec"},
	{"GITLEAKS_VERSION", "github.com/zricethezav/gitleaks/v8"},
}

type securityWorkflow struct {
	Env  map[string]string `yaml:"env"`
	Jobs map[string]struct {
		Steps []struct {
			Run string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestScannersPinnedAndUnfiltered(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot, workflowPath))
	if err != nil {
		t.Fatal(err)
	}
	var wf securityWorkflow
	if err := yaml.Unmarshal(src, &wf); err != nil {
		t.Fatal(err)
	}
	mise, err := os.ReadFile(filepath.Join(repoRoot, misePath))
	if err != nil {
		t.Fatal(err)
	}
	semver := regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	for _, s := range scanners {
		v := wf.Env[s.env]
		if !semver.MatchString(v) {
			t.Errorf("%s: %s = %q, want an exact vX.Y.Z", workflowPath, s.env, v)
			continue
		}
		pinned := regexp.MustCompile(regexp.QuoteMeta(s.pkg)+`@(\S+?)["\s]`).FindAllStringSubmatch(string(mise), -1)
		if len(pinned) == 0 {
			t.Errorf("%s: no %s@<version> in the security task", misePath, s.pkg)
		}
		for _, m := range pinned {
			if m[1] != v {
				t.Errorf("%s: %s@%s, but %s pins %s", misePath, s.pkg, m[1], workflowPath, v)
			}
		}
	}

	// The scanners run on the whole module with no flags that drop rules, severities or paths.
	want := map[string]string{"govulncheck": "govulncheck ./...", "gosec": "gosec -quiet ./..."}
	seen := map[string]bool{}
	for id, j := range wf.Jobs {
		for _, st := range j.Steps {
			for _, line := range strings.Split(st.Run, "\n") {
				line = strings.TrimSpace(line)
				for tool, cmd := range want {
					if strings.HasPrefix(line, tool+" ") {
						seen[tool] = true
						if line != cmd {
							t.Errorf("%s: job %s runs %q, want exactly %q", workflowPath, id, line, cmd)
						}
					}
				}
			}
		}
	}
	for _, tool := range slices.Sorted(maps.Keys(want)) {
		if !seen[tool] {
			t.Errorf("%s: no step runs %s", workflowPath, tool)
		}
	}
}

func TestCheckCatchesBadSuppressions(t *testing.T) {
	dir := t.TempDir()
	// NOSEC and NOLINT stand in for the real tags, so this file holds no suppression of its own.
	write := func(name, src string) {
		t.Helper()
		src = strings.NewReplacer("NOSEC", "#"+"nosec", "NOLINT", "no"+"lint").Replace(src)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("ok.go", "package x\n\nvar a = f() // NOSEC G304 -- a reason\n")
	write("blanket.go", "package x\n\nvar b = f() // NOSEC\n")
	write("noreason.go", "package x\n\nvar c = f() // NOSEC G304\n")
	write("tworules.go", "package x\n\nvar d = f() // NOSEC G304 G101 -- a reason\n")
	write("nolint.go", "package x\n\nvar e = f() //NOLINT:errcheck,gosec // a reason\n")

	found, problems, err := scanCode(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []suppression{{"ok.go", "G304"}}; !slices.Equal(found, want) {
		t.Errorf("found %v, want %v", found, want)
	}
	joined := strings.Join(problems, "\n")
	for _, f := range []string{"blanket.go", "noreason.go", "tworules.go", "nolint.go"} {
		if !strings.Contains(joined, f) {
			t.Errorf("%s was not reported; problems:\n%s", f, joined)
		}
	}

	listed := []suppression{{"gone.go", "G304"}}
	if got := diff(found, listed); !slices.Equal(got, []string{"ok.go G304"}) {
		t.Errorf("unlisted: %v", got)
	}
	if got := diff(listed, found); !slices.Equal(got, []string{"gone.go G304"}) {
		t.Errorf("stale: %v", got)
	}
}
