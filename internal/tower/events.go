package tower

import (
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"time"
)

// A flight's events, on its stream "task/<org>/<id>". The log holds the neutral id task for a
// flight (GLOSSARY.md); the Tower's state is a projection of these events, rebuilt on restart.

// Dispatched starts a flight.
type Dispatched struct {
	Org        string             `json:"org"`
	Project    string             `json:"project"`
	Goal       string             `json:"goal"`
	Base       string             `json:"base"`
	Posture    policy.Posture     `json:"posture"`
	Exceptions []policy.Exception `json:"exceptions,omitempty"`
	Delivery   string             `json:"delivery,omitempty"`   // empty for a flight dispatched before readiness was checked
	Acceptance string             `json:"acceptance,omitempty"` // the acceptance test, capped
	// Pipeline is how the flight is flown; nil for a flight dispatched before Pipelines were
	// read, which the Tower flies on its own defaults.
	Pipeline *Pipeline `json:"pipeline,omitempty"`
}

// Pipeline is what the Tower takes from a flight's ai-sdlc Pipeline (LUCY-SPEC-014).
type Pipeline struct {
	Name string `json:"name"` // ship or research, and the namespace it came from: lucy/ship or acme/ship
	// MaxRestarts is how many lost Pilot sessions the Tower replaces before it grounds the flight:
	// the pilot stage's FailurePolicy, retry's maxRetries, 0 for abort.
	MaxRestarts int `json:"max_restarts"`
	// ApprovalRequired holds every merge for the owner, whatever the autonomy posture: the merge
	// stage's ApprovalPolicy required. It can only add an approval, never waive one.
	ApprovalRequired bool `json:"approval_required,omitempty"`
	// ApprovalTimeout bounds how long a merge holds for the owner; 0 for no limit. OnTimeout is
	// abort with a timeout: the flight is grounded. A timeout never approves a merge.
	ApprovalTimeout time.Duration `json:"approval_timeout_ns,omitempty"`
	OnTimeout       string        `json:"on_timeout,omitempty"`
}

// PilotStartFailed records a Pilot start the runtime failed for a reason a retry may change (an
// outage). The flight stays dispatched, and the Tower starts it again after a backoff doubling with
// each consecutive failure, from the recorded times, so a restarted Tower keeps the same schedule
// (LUCY-SPEC-008).
type PilotStartFailed struct {
	Generation int    `json:"generation"`
	Error      string `json:"error"` // the runtime's error, capped
}

// PostureTightened records a flight in flight moved to a stricter posture: its Runway was demoted,
// its own autonomy tightened, or its posture can no longer be resolved from the config (then
// owner). A posture is never loosened in flight: a promotion applies only to flights dispatched
// after it.
type PostureTightened struct {
	From   policy.Posture `json:"from"`
	To     policy.Posture `json:"to"`
	Reason string         `json:"reason"` // why, capped
}

// PilotStarted records a Pilot session started for the flight.
type PilotStarted struct {
	Session    string `json:"session"`
	Generation int    `json:"generation"`
}

// PilotAsked records the Pilot's question; the flight waits on the owner's answer.
type PilotAsked struct {
	Session  string `json:"session"`
	Question string `json:"question"` // untrusted agent output, capped; the Tower's own words when Need is set
	// Need is set when the Tower asks, not the Pilot: the session did not start because its harness
	// needs the owner first (harness_login). The owner's answer then starts the next generation.
	Need string `json:"need,omitempty"`
}

// AnswerProposed records a Supervisor's proposed answer to the Pilot's open question
// (LUCY-RUN-010). It is a suggestion to the owner: it never reaches the Pilot and never closes the
// question, under any posture.
type AnswerProposed struct {
	Session string `json:"session"`
	By      string `json:"by"`     // the supervisor, for example supervisor/rules
	Answer  string `json:"answer"` // the proposed answer, capped: lucy's own text, never the owner's
}

// PilotAnswered records that the owner answered. The answer itself is not kept, only its digest.
type PilotAnswered struct {
	Session      string `json:"session"`
	AnswerSHA256 string `json:"answer_sha256"`
}

// Pilot exits.
const (
	ExitDone    = "done"    // finished with a pull request
	ExitFailed  = "failed"  // the Pilot gave up
	ExitLost    = "lost"    // the session crashed or vanished
	ExitStopped = "stopped" // the Tower stopped it
	// ExitNeedsOwner: the session did not start: its harness needs the owner first.
	ExitNeedsOwner = "needs_owner"
)

// PilotExited records that a Pilot session ended.
type PilotExited struct {
	Session    string `json:"session"`
	Generation int    `json:"generation"`
	Exit       string `json:"exit"`
	PR         int    `json:"pr,omitempty"`
	Reason     string `json:"reason,omitempty"` // untrusted agent output when the Pilot failed, capped
}

// PRObserved records the pull request's facts whenever they change.
type PRObserved struct {
	PullRequest
}

// Approved records the owner's approval of one merge under the facts the owner saw. It binds to
// the action and facts digests (internal/policy), so it lapses when the head or base moves.
type Approved struct {
	policy.Approval
}

// Cleared records the Tower's clearance for a merge: the policy.decision event at DecisionSeq,
// appended in the same transaction.
type Cleared struct {
	DecisionSeq  int64          `json:"decision_seq"`
	Outcome      policy.Outcome `json:"outcome"`
	Rule         string         `json:"rule"`
	HeadSHA      string         `json:"head_sha"` // the head commit the decision was about
	ActionSHA256 string         `json:"action_sha256"`
	FactsSHA256  string         `json:"facts_sha256"`
}

// MergeFailed records a merge the forge refused after a clearance.
type MergeFailed struct {
	PR      int    `json:"pr"`
	HeadSHA string `json:"head_sha"`
	Reason  string `json:"reason"`           // conflict, head_moved or error
	Detail  string `json:"detail,omitempty"` // the forge's error, untrusted text
}

// Takeoff records the merge.
type Takeoff struct {
	PR          int    `json:"pr"`
	HeadSHA     string `json:"head_sha"`
	MergeSHA    string `json:"merge_sha"`
	DecisionSeq int64  `json:"decision_seq"` // the cleared policy.decision it rested on
}

// Grounding reasons.
const (
	GroundStartFailed = "start_failed" // the runtime refused to start a Pilot (ErrStartRefused)
	GroundPilotFailed = "pilot_failed" // the Pilot gave up
	GroundPilotLost   = "pilot_lost"   // the Pilot was lost more often than the Tower restarts it
	GroundTimeout     = "pilot_timeout"
	GroundStalled     = "stalled" // the pull request made no progress toward a clearance
	// GroundApprovalTimeout: a merge held for the owner past its Pipeline's approval timeout, under
	// onTimeout abort. A timeout never approves a merge.
	GroundApprovalTimeout = "approval_timeout"
)

// Who merged a pull request lucy did not merge, as MergedOutside records it.
const (
	AttributionExternal = "external" // the forge reports an account other than lucy's
	AttributionUnknown  = "unknown"  // the forge reports no account
)

// MergedOutside ends a flight whose pull request the forge reports merged by someone other than
// lucy, or by no one it can name. It cites no clearance: lucy did not merge it.
type MergedOutside struct {
	PR          int    `json:"pr"`
	HeadSHA     string `json:"head_sha"`
	MergeSHA    string `json:"merge_sha"`
	MergedBy    string `json:"merged_by,omitempty"` // the forge's account; empty when unknown
	Attribution string `json:"attribution"`         // external or unknown
}

// Grounded ends a flight without a takeoff.
type Grounded struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// EventType and EventVersion implement eventstore.Payload.
func (Dispatched) EventType() string    { return "task.dispatched" }
func (PilotStarted) EventType() string  { return "task.pilot_started" }
func (PilotAsked) EventType() string    { return "task.pilot_asked" }
func (PilotAnswered) EventType() string { return "task.pilot_answered" }
func (PilotExited) EventType() string   { return "task.pilot_exited" }
func (PRObserved) EventType() string    { return "task.pr_observed" }
func (Approved) EventType() string      { return "task.approved" }
func (Cleared) EventType() string       { return "task.clearance" }
func (MergeFailed) EventType() string   { return "task.merge_failed" }
func (Takeoff) EventType() string       { return "task.takeoff" }
func (Grounded) EventType() string      { return "task.grounded" }
func (MergedOutside) EventType() string { return "task.merged_outside" }

func (Dispatched) EventVersion() int       { return 1 }
func (PilotStarted) EventVersion() int     { return 1 }
func (PilotAsked) EventVersion() int       { return 1 }
func (PilotAnswered) EventVersion() int    { return 1 }
func (PilotExited) EventVersion() int      { return 1 }
func (PRObserved) EventVersion() int       { return 1 }
func (Approved) EventVersion() int         { return 1 }
func (Cleared) EventVersion() int          { return 1 }
func (MergeFailed) EventVersion() int      { return 1 }
func (Takeoff) EventVersion() int          { return 1 }
func (Grounded) EventVersion() int         { return 1 }
func (PilotStartFailed) EventType() string { return "task.pilot_start_failed" }
func (PilotStartFailed) EventVersion() int { return 1 }
func (PostureTightened) EventType() string { return "task.posture_tightened" }
func (AnswerProposed) EventType() string   { return "task.answer_proposed" }
func (AnswerProposed) EventVersion() int   { return 1 }
func (PostureTightened) EventVersion() int { return 1 }
func (MergedOutside) EventVersion() int    { return 1 }

// Payloads returns a sample of every event the Tower appends, for an eventstore.Registry. The
// Tower also appends policy.DecisionRecord.
func Payloads() []eventstore.Payload {
	return []eventstore.Payload{
		Dispatched{}, PilotStarted{}, PilotAsked{}, PilotAnswered{}, PilotExited{}, PRObserved{},
		Approved{}, Cleared{}, MergeFailed{}, Takeoff{}, Grounded{}, MergedOutside{}, PilotStartFailed{}, PostureTightened{}, AnswerProposed{},
	}
}
