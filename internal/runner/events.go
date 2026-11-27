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

// Renewed records a heartbeat: the lease under Fence now expires at ExpiresAt. Answered is the id
// of the question whose answer the runner acknowledged giving its work, if any.
type Renewed struct {
	Fence     int64     `json:"fence"`
	ExpiresAt time.Time `json:"expires_at"`
	Answered  string    `json:"answered,omitempty"`
}

// Streamed records one event the runner streamed under Fence.
type Streamed struct {
	Fence int64     `json:"fence"`
	Seq   int64     `json:"seq"`
	Kind  EventKind `json:"kind"`
	Text  string    `json:"text,omitempty"` // untrusted output of the work, capped
	// Question, when asking, is the question's id. An asking event for a question already
	// answered is recorded as activity, so an answered question never re-opens.
	Question string `json:"question,omitempty"`
	// Hook, on a hook event, is the harness lifecycle hook that fired.
	Hook string `json:"hook,omitempty"`
}

// StopRequested records that the Tower asked for the run to stop. A queued run finishes at once;
// a leased one learns it on its next heartbeat.
type StopRequested struct{}

// Answered records that the Tower relayed the owner's answer to the run's question under Fence.
// Only the answer's digest is kept: its text goes to the runner on each heartbeat until the runner
// acknowledges it (Renewed.Answered), and never into the log.
type Answered struct {
	Fence        int64  `json:"fence"`
	Question     string `json:"question"` // the id of the question answered
	AnswerSHA256 string `json:"answer_sha256"`
}

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

// ContainerHeld records that an operation which stops org's container is under way: until
// ContainerReleased or ExpiresAt, no run of org is leased to a runner placed in a container. It is
// on the stream "container/<org>".
type ContainerHeld struct {
	Org       string    `json:"org"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ContainerReleased records that the operation on org's container ended.
type ContainerReleased struct {
	Org string `json:"org"`
}

// EventType and EventVersion implement eventstore.Payload.

func (Queued) EventType() string        { return "run.queued" }
func (Leased) EventType() string        { return "run.leased" }
func (Renewed) EventType() string       { return "run.renewed" }
func (Streamed) EventType() string      { return "run.event" }
func (StopRequested) EventType() string { return "run.stop_requested" }
func (Answered) EventType() string      { return "run.answered" }
func (Finished) EventType() string      { return "run.finished" }
func (Expired) EventType() string       { return "run.expired" }

func (ContainerHeld) EventType() string     { return "container.held" }
func (ContainerReleased) EventType() string { return "container.released" }

func (Queued) EventVersion() int        { return 1 }
func (Leased) EventVersion() int        { return 1 }
func (Renewed) EventVersion() int       { return 1 }
func (Streamed) EventVersion() int      { return 1 }
func (StopRequested) EventVersion() int { return 1 }
func (Answered) EventVersion() int      { return 1 }
func (Finished) EventVersion() int      { return 1 }
func (Expired) EventVersion() int       { return 1 }

func (ContainerHeld) EventVersion() int     { return 1 }
func (ContainerReleased) EventVersion() int { return 1 }

// Payloads returns the run and container hold events, for the store's registry.
func Payloads() []eventstore.Payload {
	return []eventstore.Payload{Queued{}, Leased{}, Renewed{}, Streamed{}, StopRequested{}, Answered{}, Finished{}, Expired{},
		ContainerHeld{}, ContainerReleased{}}
}

// StreamPrefix starts every run stream.
const StreamPrefix = "run/"

// Stream returns a run's stream.
func Stream(org, run string) string { return StreamPrefix + org + "/" + run }

// ContainerStreamPrefix starts every container hold stream.
const ContainerStreamPrefix = "container/"

// ContainerStream returns the stream of org's container holds.
func ContainerStream(org string) string { return ContainerStreamPrefix + org }
