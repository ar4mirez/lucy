// Package api defines lucy's one typed API (LUCY-API-001): the methods every surface calls, their
// typed parameters and results, the error model, caller identity and the wire format.
//
// The wire format is JSON-RPC 2.0, restricted for safety (ADR D10):
//
//   - one request per HTTP POST to /rpc/v1; batches and notifications are refused;
//   - params are by name (an object), decoded strictly: unknown fields, duplicate keys, trailing
//     data and values outside the declared bounds are errors (LUCY-API-006);
//   - error messages are fixed per code; details about the caller's own input go in error.data,
//     and internal error text never leaves the daemon.
//
// The method table in methods.go is the single declaration of the API. The server binds handlers to
// it, the client wraps it, and the OpenRPC document in openrpc.json is generated from it
// (`mise run api-schema`), so other surfaces (TUI, dashboard, MCP) can generate their clients from
// the schema.
package api

import (
	"context"
	"fmt"
	"strconv"
)

// Version is the API's major version. A breaking change is a new major version, served at its own
// path alongside the old one; additive changes (new methods, new optional result fields) keep it.
const Version = 1

// SchemaRevision is the minor part of the OpenRPC document's version (Version.SchemaRevision.0). It is
// bumped whenever the schema changes within a major version: by additive changes once a release
// serves v1 (ADR D10), and until then also by renamed values. Revision 1 renamed the policy
// outcomes to cleared, denied and holding, and the human role and input source to owner. Revision 2
// added the runner protocol: the run scope, the fenced error and the runner and runs methods.
// Revision 3 added the owner's relayed answer and the asking state to runner.heartbeat's result.
// Revision 4 added runner.permit: the policy engine's decision on a Pilot's prompted file tool.
// Revision 5 added the exec and fetch action kinds and renamed runner.permit's path to resource.
// Revision 6 added hook events: the hook kind and its name on runner.heartbeat's events.
// Revision 7 added questions.list and questions.answer.
// Revision 8 added the container action kind: starting, restarting or stopping an org's container,
// the sandbox_busy error runner.lease answers while it is, and runner.queued's placement.
// Revision 9 added sandbox.held and sandbox.confirm.
// Revision 10 added the gateway scope and gateway.routes, gateway.use, gateway.deny and
// gateway.complete.
// Revision 11 added capability tokens: runner.lease's and runner.heartbeat's capability,
// gateway.use's capability, gateway.verify, capabilities.list and capability.revoke.
// Revision 12 added vault.record.
// Revision 13 bound host placement to one org: runner.lease's and runner.queued's org, the
// runner_org, host_org and placement_proof errors, runner.lease's placement_proof, and
// runner.host_org.
// Revision 14 added the worktree pool: runner.lease's worktree, branch, worktree_state and
// worktree_error, runner.heartbeat's worktree_state and worktree_error, worktree.list and
// worktree.discard.
// Revision 15 added runner.slot: a read-only query of a held run's worktree state.
// Revision 16 added decisions.list (the decision catalog) and flights.approve.
// Revision 17 added the delete action kind to runner.permit.
// Revision 18 added flights.list.
// Revision 19 added flights.list's step_error and retry_at.
// Revision 20: flights.list's autonomy is the posture a Flight is flown under: the one its
// dispatch recorded, or else its Runway's from an AutonomyPolicy level, tightened by its own.
// Revision 21 added config.reload.
// Revision 22 added runner.finish's needs_owner outcome and its need.
// Revision 23 added token use and cost: runner.heartbeat's usage events and tokens, runner.finish's
// cost_micros, runs.list's and flights.list's tokens and cost.
// Revision 24 added a supervisor's proposed answer: questions.list's and decisions.list's proposal_id,
// proposed_by and proposed, questions.list's question_sha256, and questions.answer's accept_proposal
// and question_sha256.
const SchemaRevision = 24

// Path is the HTTP path the API is served at, over the unix socket and, later, over HTTPS.
const Path = "/rpc/v1"

// SupportedVersions lists every API version this build serves.
var SupportedVersions = []int{Version}

// Limits at the API boundary. They bound the work one request can make the daemon do.
const (
	MaxRequestBytes = 64 << 10 // the HTTP body of one request
	MaxMethodBytes  = 64       // a method name
	MaxIDBytes      = 128      // a string request id
	MaxJSONDepth    = 32       // nesting depth of a request
)

// Scope is a permission a caller holds. Every method needs one (LUCY-API-004).
type Scope string

// Scopes. The local owner holds all of them; paired devices (m1) hold a subset.
const (
	ScopeRead        Scope = "read"
	ScopeDecide      Scope = "decide"
	ScopeSteer       Scope = "steer"
	ScopeAttachView  Scope = "attach-view"
	ScopeAttachInput Scope = "attach-input"
	ScopeAdmin       Scope = "admin"
	ScopeRun         Scope = "run"     // a runner: lease runs, heartbeat, stream their events, finish them
	ScopeGateway     Scope = "gateway" // an org's credential gateway: read its routes, record its requests
)

// AllScopes lists every scope, in a stable order.
var AllScopes = []Scope{ScopeRead, ScopeDecide, ScopeSteer, ScopeAttachView, ScopeAttachInput, ScopeAdmin, ScopeRun, ScopeGateway}

// Caller is who sent a request, as established by the transport: the kernel's peer credentials on
// the unix socket, and (m1) a paired device's credential over HTTPS. It rides on the request
// context so every handler and the authorizer see it (LUCY-API-004).
type Caller struct {
	Transport string  // "unix"; "https" in m1
	UID       int     // the peer's user id (unix socket)
	GID       int     // the peer's group id (unix socket)
	PID       int     // the peer's process id; 0 when the OS does not report it
	Device    string  // "local" for the socket; the paired device's id in m1
	Scopes    []Scope // granted scopes; filled in by the authorizer
}

// Actor is the caller's name in the event log, for example "local/uid/1000".
func (c Caller) Actor() string {
	if c.Transport == "unix" {
		return "local/uid/" + strconv.Itoa(c.UID)
	}
	return fmt.Sprintf("%s/device/%s", c.Transport, c.Device)
}

// HasScope reports whether the caller holds s.
func (c Caller) HasScope(s Scope) bool {
	for _, have := range c.Scopes {
		if have == s {
			return true
		}
	}
	return false
}

type callerKey struct{}

// WithCaller returns ctx carrying the caller.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the caller on ctx, if any.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}
