package corpus

import (
	"fmt"
	"strings"
)

// CoveragePath is the generated coverage table, relative to the repository root.
const CoveragePath = "docs/security/conformance.md"

// RenderCoverage returns the generated content of docs/security/conformance.md: every scenario of
// the corpus with its rule class and status, and the counts by class.
func RenderCoverage(scenarios []Scenario) string {
	var b strings.Builder
	b.WriteString("---\nid: conformance-coverage\ntitle: Conformance corpus coverage\nstatus: active\nversion: 1\n" +
		"owner: ar4mirez\nrequirements: [LUCY-CORP-001, LUCY-CORP-002]\ngenerated: true\n---\n\n")
	b.WriteString("# Conformance corpus coverage\n\n")
	b.WriteString("Generated from `internal/corpus` by `mise run conformance-doc`; do not edit by hand. Every safety rule\n" +
		"an agent supervisor must keep, and every class of past incident, is a scenario that runs against\n" +
		"lucy on the simulation harness (LUCY-CORP-001, LUCY-CORP-002). An airspace moves onto lucy only\n" +
		"when its corpus is green. `mise run conformance` runs it, and CI runs it on every change.\n\n")
	c := Count(scenarios)
	fmt.Fprintf(&b, "Corpus v1: %d scenarios, **%d executable** (green: CI fails on any red one) and **%d pending** on\n"+
		"the milestone named in their row. Each executable scenario runs twice, as written and with the\n"+
		"daemon restarted before every step, and both runs must leave the same event log.\n\n", len(scenarios), c.Executable, c.Pending)
	b.WriteString("## By rule class\n\n| Rule class | Executable | Pending |\n|---|---|---|\n")
	var order []Class
	byClass := map[Class]*Counts{}
	for _, s := range scenarios {
		if byClass[s.Class] == nil {
			byClass[s.Class] = &Counts{}
			order = append(order, s.Class)
		}
		if s.Executable() {
			byClass[s.Class].Executable++
		} else {
			byClass[s.Class].Pending++
		}
	}
	for _, cl := range order {
		fmt.Fprintf(&b, "| %s | %d | %d |\n", cl, byClass[cl].Executable, byClass[cl].Pending)
	}
	b.WriteString("\n## Scenarios\n\nSource is `rule` for a standing safety rule and `incident` for a class of incident\n" +
		"that happened. Status `executable` means the scenario runs today; `pending` names the milestone and\n" +
		"requirements that make it executable.\n\n")
	b.WriteString("| ID | Rule class | Source | Severity | Rule | Status |\n|---|---|---|---|---|---|\n")
	for _, s := range scenarios {
		status := "executable"
		if p := s.Pending; p != nil {
			status = fmt.Sprintf("pending: %s (%s)", p.Milestone, strings.Join(p.Requirements, ", "))
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", s.ID, s.Class, s.Source, s.Severity,
			strings.ReplaceAll(s.Title, "|", `\|`), status)
	}
	return b.String()
}
