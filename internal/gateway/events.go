package gateway

import "github.com/ar4mirez/lucy/internal/eventstore"

// A gateway's requests, on its org's stream "gateway/<org>". Every request is recorded, forwarded
// or denied, and the audit chain covers every event (LUCY-CRED-004). No event ever holds a
// credential, a request body or a query string: the path is recorded redacted, as
// policy.RecordedResource records a fetch.

// StreamPrefix starts each org's gateway stream.
const StreamPrefix = "gateway/"

// Stream is org's gateway stream.
func Stream(org string) string { return StreamPrefix + org }

// Denied records a request the gateway refused without forwarding it.
type Denied struct {
	Route      string `json:"route,omitempty"`
	Method     string `json:"method"`
	Path       string `json:"path"`                 // redacted
	Reason     string `json:"reason"`               // why, for the owner; never a credential
	Run        string `json:"run,omitempty"`        // the run whose capability it named, when known
	Capability string `json:"capability,omitempty"` // that capability's id, never the token
}

// Forwarded records a request the gateway forwarded with its credential, after the policy
// decision that cleared the credential's use.
type Forwarded struct {
	Route       string `json:"route"`
	Method      string `json:"method"`
	Path        string `json:"path"` // redacted
	Upstream    string `json:"upstream"`
	DecisionSeq int64  `json:"decision_seq"`
	Run         string `json:"run"`        // the run the request is for, from its capability
	Capability  string `json:"capability"` // the capability's id, never the token
}

// Completed records how a forwarded request ended: the upstream's status, how many echoed copies
// of the credential were scrubbed from the answer, or the error it failed with.
type Completed struct {
	DecisionSeq int64  `json:"decision_seq"`
	Status      int    `json:"status,omitempty"`
	Scrubbed    int    `json:"scrubbed,omitempty"`
	Error       string `json:"error,omitempty"`
}

// EventType and EventVersion implement eventstore.Payload.
func (Denied) EventType() string    { return "gateway.denied" }
func (Forwarded) EventType() string { return "gateway.forwarded" }
func (Completed) EventType() string { return "gateway.completed" }

func (Denied) EventVersion() int    { return 1 }
func (Forwarded) EventVersion() int { return 1 }
func (Completed) EventVersion() int { return 1 }

// Payloads returns a sample of every event the gateway appends, for an eventstore.Registry. The
// store also needs policy.DecisionRecord.
func Payloads() []eventstore.Payload { return []eventstore.Payload{Denied{}, Forwarded{}, Completed{}} }
