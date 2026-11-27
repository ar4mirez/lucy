package vault

import "github.com/ar4mirez/lucy/internal/eventstore"

// A vault change is recorded in the daemon's log, on the org's stream "vault/<org>", by name
// only: never a value, a key or a path. `lucy vault set` and `rm` record it twice, through the
// daemon, before the vault is touched (requested) and after (applied, or failed with why), so a
// change that is not recorded does not happen (LUCY-CRED-002, LUCY-CRED-004).

// StreamPrefix starts each org's vault stream.
const StreamPrefix = "vault/"

// Stream is org's vault stream.
func Stream(org string) string { return StreamPrefix + org }

// Change operations and phases.
const (
	OpSet    = "set"
	OpRemove = "remove"

	PhaseRequested = "requested"
	PhaseApplied   = "applied"
	PhaseFailed    = "failed"
)

// Changed records one phase of a change to an org's credential.
type Changed struct {
	Name  string `json:"name"`
	Op    string `json:"op"`              // set or remove
	Phase string `json:"phase"`           // requested, applied or failed
	Error string `json:"error,omitempty"` // why it failed; never a value
}

// EventType and EventVersion implement eventstore.Payload.
func (Changed) EventType() string { return "vault.changed" }
func (Changed) EventVersion() int { return 1 }

// Payloads returns a sample of every event recorded for the vault, for an eventstore.Registry.
func Payloads() []eventstore.Payload { return []eventstore.Payload{Changed{}} }
