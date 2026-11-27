// Package corpus is lucy's conformance corpus (LUCY-CORP-001, LUCY-CORP-002): every safety rule a
// supervisor of agents must keep, and every class of past incident, written as an executable
// scenario that runs against lucy on the simulation harness (internal/sim).
//
// A scenario is data: an id, a title, the rule class it encodes, whether it comes from a standing
// rule or from an incident, a severity, a setup (flights, their scripted Pilots, the forge, the
// filesystem a delete is checked against), the steps the owner, the forge and faults take, and the
// assertions on the event log, the Clearances and the final flight states. A scenario lucy cannot
// run yet is marked pending with the milestone and requirements that will make it executable, so
// the gaps in coverage stay visible.
//
// Every executable scenario runs twice: as written, and with the daemon restarted before every
// Tower step. Both runs must pass every assertion and the corpus invariants (invariants in
// harness.go), and must leave the same event log, byte for byte: a restart is a non-event.
//
// An airspace moves onto lucy only when its corpus is green, so this package is lucy's
// acceptance gate (LUCY-CORP-003, LUCY-CORP-004). docs/security/conformance.md is generated from
// it and lists every scenario with its rule class and status.
package corpus

// Source says where a scenario's rule comes from.
type Source string

// Sources.
const (
	FromRule     Source = "rule"     // a standing safety rule of agent supervision
	FromIncident Source = "incident" // a class of incident that happened, so it must never happen again
)

// Severity is what breaking the rule costs.
type Severity string

// Severities.
const (
	Critical Severity = "critical" // lost work, an unauthorized merge or delete, raised privileges
	High     Severity = "high"     // a wrong record, a misattributed merge, an unbounded wait or retry
	Medium   Severity = "medium"   // a stuck or noisy flight that a person must notice and fix
)

// Class is a rule class: the family of rules a scenario belongs to.
type Class string

// Rule classes.
const (
	ClassMerge       Class = "merge-authority"  // nothing merges without a Clearance within the posture
	ClassChecks      Class = "checks"           // never merge red, pending or unreported checks
	ClassApproval    Class = "approvals"        // an approval binds to one action under the facts the owner saw
	ClassAttribution Class = "attribution"      // merges done outside lucy are recorded as such
	ClassRetry       Class = "bounded-retry"    // retries are bounded and end with the owner
	ClassLiveness    Class = "liveness"         // a busy Pilot is not stuck; a silent one is stopped
	ClassChannel     Class = "owner-channel"    // Pilots reach the owner only through the Tower
	ClassTrust       Class = "untrusted-input"  // outside text never raises privileges
	ClassRoles       Class = "roles"            // each role does only what its role allows
	ClassIsolation   Class = "org-isolation"    // an airspace's agents act only inside it
	ClassDelete      Class = "delete-guard"     // deletes never escape the declared roots
	ClassUnlanded    Class = "unlanded-work"    // unlanded work is never discarded or rewritten
	ClassClosing     Class = "closing-refs"     // partial work never closes the issue it is part of
	ClassRestart     Class = "durability"       // a daemon restart loses nothing and repeats nothing
	ClassSessions    Class = "sessions"         // one live session per role; steering is addressed
	ClassPublic      Class = "public-artifacts" // public text carries no internal material
	ClassValidation  Class = "validation"       // findings and reviews are decided by the right party
)

// Scenario is one entry of the corpus.
type Scenario struct {
	ID       string // stable: "<class prefix>-NN"; never reused
	Title    string // the rule, stated as what must hold
	Class    Class
	Source   Source
	Severity Severity

	// Pending is set when lucy cannot run the scenario yet; it then has no setup, steps or
	// assertions.
	Pending *Pending

	Setup  Setup
	Steps  []Action
	Expect []Assertion
}

// Pending names what a scenario waits for.
type Pending struct {
	Milestone    string   // the milestone that makes it executable: "m1"
	Requirements []string // the requirement IDs whose implementation it needs
	Needs        string   // what is missing today, in a few words
}

// Executable reports whether the scenario runs today.
func (s Scenario) Executable() bool { return s.Pending == nil }

// Counts are the corpus's size by status.
type Counts struct {
	Executable, Pending int
}

// Count returns how many scenarios of the corpus are executable and pending.
func Count(scenarios []Scenario) Counts {
	var c Counts
	for _, s := range scenarios {
		if s.Executable() {
			c.Executable++
		} else {
			c.Pending++
		}
	}
	return c
}

// V1 returns corpus v1, in id order within each rule class.
func V1() []Scenario {
	var out []Scenario
	for _, part := range [][]Scenario{
		mergeScenarios(), checkScenarios(), approvalScenarios(), attributionScenarios(), retryScenarios(),
		livenessScenarios(), channelScenarios(), trustScenarios(), roleScenarios(), isolationScenarios(),
		deleteScenarios(), unlandedScenarios(), closingScenarios(), restartScenarios(), sessionScenarios(),
		publicScenarios(), validationScenarios(),
	} {
		out = append(out, part...)
	}
	return out
}
