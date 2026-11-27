package vault

import (
	"errors"
	"slices"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// A vault change is recorded in the daemon's log, on the org's stream "vault/<org>", by name
// only: never a value, a key or a path. `lucy vault set` and `rm` record it twice, through the
// daemon, before the vault is touched (requested) and after (applied, or failed with one of the
// fixed FailureReasons, never the error's own text), so a change that is not recorded does not
// happen (LUCY-CRED-002, LUCY-CRED-004).

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
	Error string `json:"error,omitempty"` // why it failed: one of FailureReasons
}

// Why a change failed, as recorded: by the kind of error only, so a record holds no path, value or
// error text.
const (
	ReasonNotFound = "no such credential"
	ReasonCorrupt  = "a key or sealed credential does not open"
	ReasonInsecure = "an insecure file or directory"
	ReasonInvalid  = "an invalid org, credential name or value"
	ReasonFailed   = "the vault operation failed"
)

// FailureReasons is every reason a failed change records.
var FailureReasons = []string{ReasonNotFound, ReasonCorrupt, ReasonInsecure, ReasonInvalid, ReasonFailed}

// IsFailureReason reports whether s is one of FailureReasons.
func IsFailureReason(s string) bool { return slices.Contains(FailureReasons, s) }

// FailureReason is the reason recorded for a change that failed with err.
func FailureReason(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return ReasonNotFound
	case errors.Is(err, ErrCorrupt):
		return ReasonCorrupt
	case errors.Is(err, ErrInsecure):
		return ReasonInsecure
	case errors.Is(err, ErrInvalid):
		return ReasonInvalid
	}
	return ReasonFailed
}

// EventType and EventVersion implement eventstore.Payload.
func (Changed) EventType() string { return "vault.changed" }
func (Changed) EventVersion() int { return 1 }

// Payloads returns a sample of every event recorded for the vault, for an eventstore.Registry.
func Payloads() []eventstore.Payload { return []eventstore.Payload{Changed{}} }
