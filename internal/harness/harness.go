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
	"strings"
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
	Kind     Kind
	Source   Source
	Text     string // untrusted
	Question string // when asking: the question's id, the same on every repeat of it; untrusted
}

// Exit is how a session ended, as the harness's protocol reported it.
type Exit struct {
	OK     bool
	PR     int    // when OK: the pull request the Pilot reported; 0 when none
	Reason string // when not OK: why, untrusted
}

// Session is what one harness session is started with.
type Session struct {
	Goal   string // the flight's goal: data for the Pilot to act on
	Permit Permit // decides the session's prompted file tools; nil refuses them all
	// Instructions are the repository's agent instructions, as Instructions read them from the
	// run's directory: repository text the Pilot follows, never the owner's authority.
	Instructions string
}

// Access is what a prompted tool asks to do with files.
type Access string

// Accesses.
const (
	Read  Access = "read"
	Write Access = "write"
	Exec  Access = "exec"  // run a shell command
	Fetch Access = "fetch" // fetch a URL or search the network
)

// Permit asks the policy engine whether the Pilot may run a prompted tool. For Read and Write,
// resource is the path it reads or writes (empty when the tool names none); for Exec the shell
// command and for Fetch the URL or search, as the Pilot wrote them. It returns whether the tool may
// run and, when not, why. An adapter refuses, without asking, every prompted tool it cannot map to
// an Access.
type Permit func(tool string, access Access, resource string) (allowed bool, why string)

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
	// Protected are the path elements, compared case-insensitively, that name the harness's own
	// configuration; a prompted write to a path inside the run holding one is refused.
	Protected() []string
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
	// Asking returns the Asking event of the question the session waits on, if any.
	Asking() (Event, bool)
	// Answer replies to the session's waiting question with the owner's answer.
	Answer(text string) error
	// Close ends the program's input.
	Close() error
}

// VarRef reports whether p holds a "$" a shell or the harness could read as a variable or command
// reference: "${", "$(", or a path element that starts with "$" and an environment-style name, the
// longest run of letters, digits and underscores after the "$" starting with an upper-case letter
// or underscore and holding no lower-case letter, whatever follows it ($HOME, $TMPDIR-1, $HOME.bak).
// Claude Code passes file-tool paths literally, so any other "$", as in the route files
// posts.$slug.tsx, $postId.tsx and $_index.tsx, is a plain character.
func VarRef(p string) bool {
	if strings.Contains(p, "${") || strings.Contains(p, "$(") {
		return true
	}
	for _, e := range strings.Split(p, "/") {
		if envName(e) {
			return true
		}
	}
	return false
}

// envName reports whether e starts with "$" and an environment-style variable name.
func envName(e string) bool {
	if len(e) < 2 || e[0] != '$' || !(e[1] == '_' || 'A' <= e[1] && e[1] <= 'Z') {
		return false
	}
	for _, c := range e[2:] {
		switch {
		case 'a' <= c && c <= 'z':
			return false
		case c != '_' && !('A' <= c && c <= 'Z') && !('0' <= c && c <= '9'):
			return true
		}
	}
	return true
}
