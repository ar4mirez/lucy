package sandbox

import (
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// An org's container operations, on its stream "sandbox/<org>". The policy decision behind each
// request is on the policy stream; these events tie it to the operation, the sessions it was
// decided on and what the provider did.

// Requested records a request for an operation and the policy engine's decision on it.
type Requested struct {
	Op            Op             `json:"op"`
	Outcome       policy.Outcome `json:"outcome"`
	Rule          string         `json:"rule"`
	DecisionSeq   int64          `json:"decision_seq"`
	LiveSessions  []string       `json:"live_sessions,omitempty"` // the sessions the decision was made on
	ActionSHA256  string         `json:"action_sha256"`
	FactsSHA256   string         `json:"facts_sha256"`
	ApprovalsUsed []string       `json:"approvals_used,omitempty"`
	NeedsOwner    bool           `json:"needs_owner,omitempty"` // live sessions stop it until the owner confirms them
}

// Confirmed records the owner's confirmation of a holding request: an approval bound to that
// request's action and to the sessions it named.
type Confirmed struct {
	Op          Op              `json:"op"`
	DecisionSeq int64           `json:"decision_seq"` // the holding decision confirmed
	Approval    policy.Approval `json:"approval"`
}

// Acted records that the provider ran a cleared operation, or the error it ended with.
type Acted struct {
	Op          Op     `json:"op"`
	DecisionSeq int64  `json:"decision_seq"` // the clearance it ran on
	Error       string `json:"error,omitempty"`
}

// EventType and EventVersion implement eventstore.Payload.
func (Requested) EventType() string { return "sandbox.requested" }
func (Confirmed) EventType() string { return "sandbox.confirmed" }
func (Acted) EventType() string     { return "sandbox.acted" }

func (Requested) EventVersion() int { return 1 }
func (Confirmed) EventVersion() int { return 1 }
func (Acted) EventVersion() int     { return 1 }

// Payloads returns a sample of every event the Sandbox appends, for an eventstore.Registry. The
// store also needs policy.DecisionRecord.
func Payloads() []eventstore.Payload { return []eventstore.Payload{Requested{}, Confirmed{}, Acted{}} }
