// Package harness is the seam between a runner and the agent harness it drives (LUCY-RUN-002,
// LUCY-RUN-003). A harness runs over its structured protocol, never a screen: an Adapter names the
// program's arguments and environment for one session, checks the program is the pinned version,
// and turns each line the program writes into typed events, tagged at the point they enter lucy.
// Trust, permissions and limits are arguments the adapter passes, not prompts it answers.
//
// A Pilot has no channel to the owner (LUCY-RUN-013): its questions reach the owner only as events
// the runner streams into the Tower's log, and the owner's answer reaches it only through Conn.Answer,
// which the runner calls with what the Tower relays. An adapter must start its harness with no
// other way out to a person: no messaging servers, no user-level hooks.
package harness

import (
	"errors"
	"io"
)

// Kind is what a harness event is. The kinds match the runner protocol's event kinds.
type Kind string

// Event kinds.
const (
	Output   Kind = "output"   // text the Pilot wrote
	Activity Kind = "activity" // the Pilot did something: started, called a tool, was refused one
	Asking   Kind = "asking"   // the Pilot asks a question and waits on the owner's answer
)

// Source tags every event a harness produces: it is the harness's untrusted output, whatever the
// event's text claims to be. The adapter sets it where the text enters lucy, never the model.
type Source string

// Event is one thing the harness reported.
type Event struct {
	Kind   Kind
	Source Source
	Text   string // untrusted
}

// Exit is how a session ended, as the harness's protocol reported it.
type Exit struct {
	OK     bool
	PR     int    // when OK: the pull request the Pilot reported; 0 when none
	Reason string // when not OK: why, untrusted
}

// Session is what one harness session is started with.
type Session struct {
	Goal string // the flight's goal: data for the Pilot to act on
}

// PRDirective starts the line a Pilot writes to report its pull request: "lucy:pr=<number>".
const PRDirective = "lucy:pr="

// Errors.
var (
	// ErrUnpinned refuses a harness that is not the version its adapter is pinned to: an upgrade
	// is held until the adapter's contract tests pass against it and the pin moves.
	ErrUnpinned = errors.New("harness: not the pinned version; the upgrade is held")
	// ErrNotAsking refuses an answer when the session has no question waiting.
	ErrNotAsking = errors.New("harness: the session is not asking")
	// ErrClosed refuses input after the session's input was closed.
	ErrClosed = errors.New("harness: the session's input is closed")
)

// Adapter drives one harness over its structured protocol.
type Adapter interface {
	// Name is the harness's name, as `lucy runner --harness` takes it.
	Name() string
	// Pinned is the one harness version the adapter supports.
	Pinned() string
	// VersionArgs are the arguments that make the program print its version and exit.
	VersionArgs() []string
	// CheckVersion parses what VersionArgs printed and returns the version, or an error wrapping
	// ErrUnpinned when it is not the pinned one.
	CheckVersion(out []byte) (string, error)
	// Args are the program's arguments for a session.
	Args(s Session) []string
	// Env is what the adapter adds to the session's environment, as KEY=value.
	Env() []string
	// Open starts the session's protocol on the program's input, writing the opening message.
	Open(in io.WriteCloser, s Session) (Conn, error)
}

// Conn is one session's protocol. Decode is called with each line the program writes to its
// standard output, in order, from one goroutine; Answer may be called from another.
type Conn interface {
	// Decode turns one line of output into events. exit is set when the protocol reports the
	// session's end; the adapter then closes the program's input. While a question waits, the
	// events end with it, so the last event streamed is always the open question.
	Decode(line []byte) (events []Event, exit *Exit)
	// Asking returns the question the session waits on, as its Asking event's text, if any.
	Asking() (question string, ok bool)
	// Answer replies to the session's waiting question with the owner's answer.
	Answer(text string) error
	// Close ends the program's input.
	Close() error
}
