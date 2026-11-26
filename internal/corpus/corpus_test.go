package corpus

import (
	"bufio"
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/sim"
	"github.com/ar4mirez/lucy/internal/tower"
)

var update = flag.Bool("update", false, "rewrite docs/security/conformance.md")

// TestConformance runs corpus v1: every executable scenario as written and with the daemon
// restarted before every step, and every pending one as a skip that names what it waits for.
func TestConformance(t *testing.T) {
	scenarios := V1()
	for _, s := range scenarios {
		t.Run(s.ID, func(t *testing.T) {
			if p := s.Pending; p != nil {
				t.Skipf("pending %s (%s): %s", p.Milestone, strings.Join(p.Requirements, ", "), p.Needs)
			}
			ctx := context.Background()
			var logs [][]sim.Line
			for _, v := range []Variant{AsWritten, RestartEveryStep} {
				res, err := Play(ctx, t.TempDir(), s, v)
				if err != nil {
					t.Fatalf("%s (%s): %s\n%v", s.Title, v, s.Class, err)
				}
				logs = append(logs, res.Log)
			}
			if !slices.Equal(logs[0], logs[1]) {
				t.Fatalf("restarting the daemon before every step changed the event log (%d and %d events)", len(logs[0]), len(logs[1]))
			}
		})
	}
	c := Count(scenarios)
	t.Logf("corpus v1: %d executable, %d pending", c.Executable, c.Pending)
}

var scenarioID = regexp.MustCompile(`^([a-z]+)-[0-9]{2}$`)

// TestCorpusWellFormed checks the corpus's own rules: unique, stable-looking ids whose prefix
// names one class; known sources and severities; executable scenarios with steps and assertions;
// pending ones with nothing to run, a milestone, and requirement IDs the PRD defines, at least one
// of them scheduled in that milestone.
func TestCorpusWellFormed(t *testing.T) {
	milestones := requirementMilestones(t)
	seen := map[string]bool{}
	prefixClass := map[string]Class{}
	for _, s := range V1() {
		m := scenarioID.FindStringSubmatch(s.ID)
		switch {
		case m == nil:
			t.Errorf("%q: ids look like class-NN", s.ID)
			continue
		case seen[s.ID]:
			t.Errorf("%s: duplicate id", s.ID)
		case s.Title == "" || s.Class == "":
			t.Errorf("%s: needs a title and a class", s.ID)
		case s.Source != FromRule && s.Source != FromIncident:
			t.Errorf("%s: unknown source %q", s.ID, s.Source)
		case s.Severity != Critical && s.Severity != High && s.Severity != Medium:
			t.Errorf("%s: unknown severity %q", s.ID, s.Severity)
		}
		seen[s.ID] = true
		if c, ok := prefixClass[m[1]]; ok && c != s.Class {
			t.Errorf("%s: prefix %s is class %s, not %s", s.ID, m[1], c, s.Class)
		}
		prefixClass[m[1]] = s.Class
		if s.Executable() {
			if len(s.Steps) == 0 || len(s.Expect) == 0 {
				t.Errorf("%s: an executable scenario needs steps and assertions", s.ID)
			}
			continue
		}
		p := s.Pending
		if len(s.Steps) > 0 || len(s.Expect) > 0 || len(s.Setup.Flights) > 0 {
			t.Errorf("%s: a pending scenario has nothing to run", s.ID)
		}
		if !regexp.MustCompile(`^m[1-9]$`).MatchString(p.Milestone) || len(p.Requirements) == 0 || p.Needs == "" {
			t.Errorf("%s: pending needs a later milestone, requirements and what it needs: %+v", s.ID, p)
		}
		scheduled := false
		for _, id := range p.Requirements {
			ms, ok := milestones[id]
			if !ok {
				t.Errorf("%s: %s is not a defined requirement", s.ID, id)
			}
			scheduled = scheduled || ms == p.Milestone
		}
		if !scheduled {
			t.Errorf("%s: none of %v is scheduled in %s", s.ID, p.Requirements, p.Milestone)
		}
	}
}

// requirementMilestones reads the generated requirements index: ID -> milestone.
func requirementMilestones(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "docs", "requirements.md"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	row := regexp.MustCompile(`^\| (LUCY-[A-Z]+-[0-9]{3}) \| (m[0-9]) \|`)
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := row.FindStringSubmatch(sc.Text()); m != nil {
			out[m[1]] = m[2]
		}
	}
	if err := sc.Err(); err != nil || len(out) == 0 {
		t.Fatalf("read the requirements index: %v (%d rows)", err, len(out))
	}
	return out
}

// TestCoverageDoc keeps docs/security/conformance.md in step with the corpus; run with -update
// (mise run conformance-doc) to rewrite it.
func TestCoverageDoc(t *testing.T) {
	path := filepath.Join("..", "..", CoveragePath)
	want := RenderCoverage(V1())
	if *update {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s is stale: run mise run conformance-doc", CoveragePath)
	}
}

// The harness must fail a scenario whose assertion does not hold and one that breaks an
// invariant, or a green corpus would prove nothing.
func TestHarnessCatchesViolations(t *testing.T) {
	ctx := context.Background()
	for _, s := range []Scenario{
		{
			ID: "selftest-01", Title: "a wrong assertion", Class: "selftest", Source: FromRule, Severity: Medium,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps:  []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, "")},
		},
		{
			ID: "selftest-02", Title: "a merge as lucy without a Clearance", Class: "selftest", Source: FromRule, Severity: Medium,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps: []Action{Proceed(), Do("merge as lucy behind the Tower's back", func(_ context.Context, e *Env) error {
				return e.World.Forge.MergeAs(Repo, 1, e.World.Forge.Identity)
			}), Proceed()},
			Expect: []Assertion{Merges(1)},
		},
		{
			ID: "selftest-03", Title: "a hook that never fires", Class: "selftest", Source: FromRule, Severity: Medium,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps:  []Action{OnState("f1", tower.StateHolding, Restart()), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, "")},
		},
	} {
		_, err := Play(ctx, t.TempDir(), s, AsWritten)
		if err == nil {
			t.Errorf("%s (%s) passed", s.ID, s.Title)
			continue
		}
		t.Logf("%s failed as it should: %v", s.ID, err)
		if s.ID == "selftest-02" && !strings.Contains(err.Error(), "no cleared decision") {
			t.Errorf("selftest-02 failed for another reason: %v", err)
		}
	}
}
