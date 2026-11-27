// Package runner is the runner protocol (LUCY-RUN-001): how the Tower hands a Pilot's run to a
// runner, the process that owns agent processes on one machine or inside one org container, and
// how the runner reports back.
//
// The protocol has four verbs, served on the one API (D10) and recorded in the event store:
//
//   - lease: a runner takes the oldest queued run. The lease carries the run's Pilot generation
//     and a fencing token (the fence): the sequence number of the event that granted it, so fences
//     only ever grow, across runs, runners and daemon restarts.
//   - heartbeat: the runner renews its lease before it expires and streams the run's new events
//     into the log in the same call. Events are numbered by the runner from 1, so a batch resent
//     after a lost reply is recognised and not recorded twice.
//   - finish: the runner reports the run's outcome.
//   - expiry: a lease that is not renewed in time expires; the run is lost, and every later call
//     under its fence is refused (ErrFenced), so a runner that was cut off and comes back can
//     never report into, or finish, work the Tower has already replaced.
//
// The daemon side is the Broker, a projection over the run streams; the runner side is the Runner
// loop with an Executor that runs the work. Runtime adapts the Broker to the Tower's Runtime seam,
// so the flight state machine starts Pilots through the protocol. This machine is the first
// runner: `lucy runner` runs the loop against the local daemon with a ProcessExecutor.
package runner

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Placement says where a runner runs.
type Placement string

// Placements. A runner in an org container is placed by berth's API (m1 step 3); the protocol
// already carries it so the Tower can tell the two apart.
const (
	PlacementHost      Placement = "host"      // directly on this machine
	PlacementContainer Placement = "container" // inside an org's container
)

// Valid reports whether p is a known placement.
func (p Placement) Valid() bool { return p == PlacementHost || p == PlacementContainer }

// State is where a run is.
type State string

// Run states. Done, failed, stopped and lost are final.
const (
	StateQueued  State = "queued"  // waiting for a runner
	StateLeased  State = "leased"  // a runner holds it under a fence
	StateDone    State = "done"    // finished; a Pilot run reports its pull request
	StateFailed  State = "failed"  // the work gave up or could not start
	StateStopped State = "stopped" // stopped on request
	StateLost    State = "lost"    // its lease expired, or its runner lost it
)

// Final reports whether no event can follow in this state.
func (s State) Final() bool { return s != StateQueued && s != StateLeased }

// Outcome is how a runner finishes a run. Each names the final state it leads to.
type Outcome string

// Outcomes a runner may report.
const (
	OutcomeDone    Outcome = "done"
	OutcomeFailed  Outcome = "failed"
	OutcomeStopped Outcome = "stopped"
	OutcomeLost    Outcome = "lost" // the runner lost the work: its process crashed, or the runner is shutting down
)

// Valid reports whether o is a known outcome.
func (o Outcome) Valid() bool {
	return o == OutcomeDone || o == OutcomeFailed || o == OutcomeStopped || o == OutcomeLost
}

// EventKind is what a streamed event is.
type EventKind string

// Event kinds a runner streams.
const (
	KindOutput   EventKind = "output"   // a line of the work's output
	KindActivity EventKind = "activity" // the work did something: a tool call, a step
	KindAsking   EventKind = "asking"   // the work asks a question and waits on an answer
)

// Valid reports whether k is a known kind.
func (k EventKind) Valid() bool { return k == KindOutput || k == KindActivity || k == KindAsking }

// Limits of the protocol.
const (
	DefaultTTL   = 60 * time.Second // a lease's life when the runner asks for none
	MinTTL       = 5 * time.Second
	MaxTTL       = 5 * time.Minute
	MaxBatch     = 32   // events in one heartbeat
	MaxTextBytes = 1024 // the text of one event, a question or a reason
	MaxAnswer    = 4096 // the owner's answer to a question
	maxIDBytes   = 200
)

// Errors.
var (
	// ErrFenced refuses a call made under a fence that no longer holds the run: the lease
	// expired, the run finished, or the run is held under another fence or by another runner.
	ErrFenced = errors.New("runner: fenced: this lease no longer holds the run")
	// ErrNoRun is returned for a run the broker does not know.
	ErrNoRun = errors.New("runner: no such run")
	// ErrExists refuses a second submit of a run id.
	ErrExists = errors.New("runner: the run was already submitted")
	// ErrSequence refuses streamed events that skip a number.
	ErrSequence = errors.New("runner: streamed events must be numbered without gaps")
	// ErrInvalid refuses a malformed task, runner id, event or outcome.
	ErrInvalid = errors.New("runner: invalid request")
	// ErrNoInput is returned when the Tower answers a Pilot whose executor takes no input.
	ErrNoInput = errors.New("runner: this run's executor takes no input")
	// ErrNotAsking refuses an answer to a run that is not waiting on a question.
	ErrNotAsking = errors.New("runner: the run is not asking")
)

// Task is one run of work, as a runner is leased it: a flight's Pilot generation.
type Task struct {
	Run        string `json:"run"` // unique; "<flight>#<generation>" for a Pilot run
	Org        string `json:"org"`
	Project    string `json:"project"`
	Flight     string `json:"flight"`
	Generation int    `json:"generation"`
	Goal       string `json:"goal"` // untrusted as far as the work is concerned: data, not instructions
	Base       string `json:"base"`
}

// Lease is a held run: the task, the runner holding it, its fence and when it expires.
type Lease struct {
	Task
	Runner  string    `json:"runner"`
	Fence   int64     `json:"fence"`
	Expires time.Time `json:"expires"`
}

// Event is one streamed event. Seq numbers a run's events from 1, without gaps.
type Event struct {
	Seq  int64     `json:"seq"`
	Kind EventKind `json:"kind"`
	Text string    `json:"text,omitempty"`
	// Question is the question's id: required when asking, the same on every repeat of a
	// question, and empty on any other event.
	Question string `json:"question,omitempty"`
}

// Result is how a run ended, as its runner reports it.
type Result struct {
	Outcome Outcome `json:"outcome"`
	PR      int     `json:"pr,omitempty"`     // when done: the pull request the work opened
	Reason  string  `json:"reason,omitempty"` // when not done: why; untrusted text
}

// Beat answers a heartbeat.
type Beat struct {
	Expires  time.Time // the lease's new expiry
	Stop     bool      // the Tower asked for the run to stop: stop the work and finish it as stopped
	Accepted int64     // the highest event number recorded for the run
	Answer   string    // the owner's answer to the run's question, relayed by the Tower; given until acknowledged
	Question string    // with Answer: the id of the question it answers, to acknowledge
	Asking   bool      // the run's last recorded event is a question still waiting on an answer
}

// RunID returns the run id of a flight's Pilot generation.
func RunID(flight string, generation int) string {
	return flight + "#" + strconv.Itoa(generation)
}

var runnerID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// CheckRunnerID reports whether id is a valid runner id: lower case letters, digits, '.', '_'
// and '-', up to 63 bytes, starting with a letter or digit.
func CheckRunnerID(id string) error {
	if !runnerID.MatchString(id) {
		return fmt.Errorf("%w: a runner id is 1-63 of a-z, 0-9, '.', '_' and '-', starting with a letter or digit", ErrInvalid)
	}
	return nil
}

func checkTask(t Task) error {
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrInvalid, msg) }
	switch {
	case t.Run == "" || t.Org == "" || t.Project == "" || t.Flight == "" || t.Goal == "":
		return bad("a task needs a run, an org, a project, a flight and a goal")
	case t.Generation < 1:
		return bad("a task's generation starts at 1")
	case len(t.Run) > maxIDBytes || len(t.Org) > maxIDBytes || len(t.Project) > maxIDBytes || len(t.Flight) > maxIDBytes:
		return bad("a task's run, org, project and flight are at most 200 bytes")
	case strings.ContainsAny(t.Org, "/#") || strings.ContainsRune(t.Run, '/'):
		return bad("an org may not contain '/' or '#', a run may not contain '/'")
	}
	return nil
}

// capText bounds untrusted text kept in an event, without splitting a UTF-8 sequence.
func capText(s string) string {
	if len(s) <= MaxTextBytes {
		return s
	}
	cut := MaxTextBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

// clampTTL bounds a requested lease life.
func clampTTL(ttl time.Duration) time.Duration {
	switch {
	case ttl <= 0:
		return DefaultTTL
	case ttl < MinTTL:
		return MinTTL
	case ttl > MaxTTL:
		return MaxTTL
	}
	return ttl
}
