package runner

import (
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// A run's events, on its stream "run/<org>/<run>". The broker's state is the runner_runs
// projection of them, so a daemon restarted mid-run knows every lease, fence and expiry.

// Queued records a run waiting for a runner.
type Queued struct {
	Org        string `json:"org"`
	Project    string `json:"project"`
	Flight     string `json:"flight"`
	Generation int    `json:"generation"`
	Goal       string `json:"goal"`
	Base       string `json:"base"`
}

// Leased records a runner taking the run. The event's sequence number is the lease's fence.
type Leased struct {
	Runner    string    `json:"runner"`
	Placement Placement `json:"placement"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Renewed records a heartbeat: the lease under Fence now expires at ExpiresAt.
type Renewed struct {
	Fence     int64     `json:"fence"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Streamed records one event the runner streamed under Fence.
type Streamed struct {
	Fence int64     `json:"fence"`
	Seq   int64     `json:"seq"`
	Kind  EventKind `json:"kind"`
	Text  string    `json:"text,omitempty"` // untrusted output of the work, capped
}

// StopRequested records that the Tower asked for the run to stop. A queued run finishes at once;
// a leased one learns it on its next heartbeat.
type StopRequested struct{}

// Finished ends a run. Fence is 0 when no runner held it (a queued run stopped).
type Finished struct {
	Fence   int64   `json:"fence"`
	Outcome Outcome `json:"outcome"`
	PR      int     `json:"pr,omitempty"`
	Reason  string  `json:"reason,omitempty"` // untrusted text, capped
}

// Expired ends a run whose lease under Fence was not renewed by ExpiresAt. The run is lost.
type Expired struct {
	Fence     int64     `json:"fence"`
	ExpiresAt time.Time `json:"expires_at"`
}

// EventType and EventVersion implement eventstore.Payload.

func (Queued) EventType() string        { return "run.queued" }
func (Leased) EventType() string        { return "run.leased" }
func (Renewed) EventType() string       { return "run.renewed" }
func (Streamed) EventType() string      { return "run.event" }
func (StopRequested) EventType() string { return "run.stop_requested" }
func (Finished) EventType() string      { return "run.finished" }
func (Expired) EventType() string       { return "run.expired" }

func (Queued) EventVersion() int        { return 1 }
func (Leased) EventVersion() int        { return 1 }
func (Renewed) EventVersion() int       { return 1 }
func (Streamed) EventVersion() int      { return 1 }
func (StopRequested) EventVersion() int { return 1 }
func (Finished) EventVersion() int      { return 1 }
func (Expired) EventVersion() int       { return 1 }

// Payloads returns the run events, for the store's registry.
func Payloads() []eventstore.Payload {
	return []eventstore.Payload{Queued{}, Leased{}, Renewed{}, Streamed{}, StopRequested{}, Finished{}, Expired{}}
}

// StreamPrefix starts every run stream.
const StreamPrefix = "run/"

// Stream returns a run's stream.
func Stream(org, run string) string { return StreamPrefix + org + "/" + run }
