package tower

import (
	"context"
	"errors"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
)

// The seams below are what the Tower drives a flight through. Real adapters implement them in m1
// (a harness adapter for Runtime, the GitHub client for Forge, the system clock for Clock); the
// simulation harness (internal/sim) implements them with fakes, so the same state machine runs in
// tests without agents, network or wall-clock time (LUCY-SIM-001).

// Clock is the Tower's only source of time. The Tower never sleeps: it reads the clock on each
// Step, and a driver decides when to call Step next (NextWake).
type Clock interface {
	Now() time.Time
}

// PilotState is what a Pilot session reports.
type PilotState string

// Pilot states.
const (
	PilotRunning PilotState = "running" // working
	PilotAsking  PilotState = "asking"  // waiting on an answer to its question
	PilotDone    PilotState = "done"    // finished; its pull request is open
	PilotFailed  PilotState = "failed"  // gave up, with a reason
	PilotLost    PilotState = "lost"    // the session is gone: the process crashed or the runner lost it
	// PilotNeedsOwner: the session did not start because its harness needs the owner first (Need,
	// a known code, never agent text), so the Tower holds the flight for the owner (LUCY-RUN-008).
	PilotNeedsOwner PilotState = "needs_owner"
)

// PilotRequest is what a Pilot session is started with.
type PilotRequest struct {
	Flight     string // the flight's id
	Org        string
	Project    string
	Goal       string
	Base       string // the branch the pull request targets
	Generation int    // 1 for the first session of a flight, +1 for each restart
}

// PilotReport is a Pilot session's state, as the runtime last saw it.
type PilotReport struct {
	State    PilotState
	Activity time.Time // when the session last did something (output, a tool call); zero when unknown
	Question string    // when asking: the question, untrusted agent output
	PR       int       // when done: the pull request the Pilot opened
	Reason   string    // when failed: why, untrusted agent output
	Need     string    // when needs_owner: what for (harness_login)
	Runner   string    // when needs_owner: the runner whose org home needs it, as the lease names it
}

// Runtime starts and watches Pilot sessions: a harness adapter on a runner (LUCY-RUN-001, m1).
// Session ids are opaque to the Tower.
type Runtime interface {
	Start(ctx context.Context, req PilotRequest) (session string, err error)
	Poll(ctx context.Context, session string) (PilotReport, error)
	Answer(ctx context.Context, session, answer string) error
	Stop(ctx context.Context, session string) error
}

// PullRequest is a pull request as the forge's authenticated API reports it. These are the facts a
// merge clearance rests on.
type PullRequest struct {
	Number             int               `json:"number"`
	HeadSHA            string            `json:"head_sha"`
	BaseRef            string            `json:"base_ref"`
	BaseSHA            string            `json:"base_sha"`
	Checks             policy.CheckState `json:"checks"`                        // "" when no check has reported
	RequiredUnreported bool              `json:"required_unreported,omitempty"` // a required check has not reported
	Conflict           bool              `json:"conflict,omitempty"`            // the head does not merge cleanly into the base
	Merged             bool              `json:"merged,omitempty"`
	MergeSHA           string            `json:"merge_sha,omitempty"` // when merged
	MergedBy           string            `json:"merged_by,omitempty"` // when merged: the forge account that merged it; empty when the forge cannot say
}

// Forge errors a Merge returns.
var (
	ErrConflict  = errors.New("forge: the pull request does not merge cleanly")
	ErrHeadMoved = errors.New("forge: the pull request's head is not the commit that was cleared")
)

// Forge reads pull requests and merges them: the GitHub client (m1). repo is "org/project".
// Merge merges only if the pull request's head is still head, so a push after the clearance can
// never land unreviewed code; it returns ErrHeadMoved or ErrConflict otherwise.
type Forge interface {
	PullRequest(ctx context.Context, repo string, number int) (PullRequest, error)
	Merge(ctx context.Context, repo string, number int, head string) (mergeSHA string, err error)
}

// Supervisor proposes how to settle what the Tower's rules leave to a person (LUCY-RUN-010). It
// proposes only and never acts: the Tower records every proposal and shows it beside the question,
// and under every posture only the owner's words answer it and close the decision (corpus rule
// channel-05). The owner may accept a proposal in one step, which is then the owner's answer.
type Supervisor interface {
	// ProposeAnswer proposes an answer to a Pilot's question, or returns false to leave it to the
	// owner. The question is untrusted Pilot output.
	ProposeAnswer(ctx context.Context, q Escalation) (Proposal, bool, error)
}

// Escalation is what a Supervisor is asked about: one flight's open question.
type Escalation struct {
	Org, Flight, Project string
	Goal                 string         // the flight's goal
	Posture              policy.Posture // the flight's posture
	Question             string         // the Pilot's question, untrusted
}

// Proposal is a Supervisor's proposed answer.
type Proposal struct {
	By     string // who proposed it: "supervisor/" and the supervisor's name, for example supervisor/rules
	Answer string
}
