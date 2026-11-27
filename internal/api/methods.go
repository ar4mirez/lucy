package api

import (
	"encoding/json"
	"reflect"
	"time"
)

// Method declares one API method: its name, what it needs and what it takes and returns. The table
// below is the API's single declaration; the server, the client and openrpc.json all follow it.
type Method struct {
	Name     string
	Summary  string
	Group    string // the design's message family (LUCY-API-002): hello, query or command
	Scope    Scope  // the scope a caller needs
	Mutating bool   // a mutating call is recorded as an api.command event before it runs (LUCY-API-005)
	Params   reflect.Type
	Result   reflect.Type
}

func typeOf[T any]() reflect.Type { return reflect.TypeFor[T]() }

// Method names.
const (
	MethodDiscover  = "rpc.discover"
	MethodHello     = "hello"
	MethodHealth    = "health"
	MethodStatus    = "status.get"
	MethodEvents    = "events.list"
	MethodShutdown  = "daemon.shutdown"
	MethodEvaluate  = "policy.evaluate"
	MethodDecisions = "policy.decisions"
	MethodResources = "resources.list"
	MethodAudit     = "audit.verify"
	MethodQueued    = "runner.queued"
	MethodLease     = "runner.lease"
	MethodHeartbeat = "runner.heartbeat"
	MethodFinish    = "runner.finish"
	MethodPermit    = "runner.permit"
	MethodQuestions = "questions.list"
	MethodAnswer    = "questions.answer"
	MethodRuns      = "runs.list"
	MethodHeld      = "sandbox.held"
	MethodConfirm   = "sandbox.confirm"
)

var methods = []Method{
	{MethodDiscover, "The API's OpenRPC document: every method, its params, result and errors.", "query", ScopeRead, false, typeOf[NoParams](), typeOf[json.RawMessage]()},
	{MethodHello, "Negotiate the API version and learn the caller's identity, scopes and the methods it may call.", "hello", ScopeRead, false, typeOf[HelloParams](), typeOf[Welcome]()},
	{MethodHealth, "Liveness: the daemon is up and its event store answers.", "query", ScopeRead, false, typeOf[NoParams](), typeOf[Health]()},
	{MethodStatus, "The daemon's version, uptime and event log head.", "query", ScopeRead, false, typeOf[NoParams](), typeOf[Status]()},
	{MethodEvents, "Events from the log after a sequence number, oldest first.", "query", ScopeRead, false, typeOf[EventsParams](), typeOf[EventsPage]()},
	{MethodShutdown, "Stop the daemon gracefully after answering.", "command", ScopeAdmin, true, typeOf[NoParams](), typeOf[ShutdownResult]()},
	{MethodEvaluate, "Ask the policy engine to decide an action; the decision is recorded before it is returned.", "command", ScopeDecide, true, typeOf[EvaluateParams](), typeOf[PolicyDecision]()},
	{MethodDecisions, "Recorded policy decisions, oldest first.", "query", ScopeRead, false, typeOf[DecisionsParams](), typeOf[DecisionsPage]()},
	{MethodResources, "The config resources the daemon loaded and their status, as conditions.", "query", ScopeRead, false, typeOf[ResourcesParams](), typeOf[ResourcesPage]()},
	{MethodAudit, "Verify the audit hash chain against the event log.", "query", ScopeRead, false, typeOf[NoParams](), typeOf[AuditReport]()},
	{MethodQueued, "How many runs wait for a runner placed at placement. A runner polls this and leases only when it is not zero, so an idle runner adds nothing to the log. A runner placed in a container is not counted the runs of an org whose container is being restarted or stopped.", "query", ScopeRun, false, typeOf[QueuedParams](), typeOf[QueuedResult]()},
	{MethodLease, "Lease the oldest queued run under a new fencing token. A runner placed in a container is never leased a run whose org's container is being restarted or stopped; when every queued run is such a run, it is refused with sandbox_busy and leases again later.", "command", ScopeRun, true, typeOf[LeaseParams](), typeOf[LeaseResult]()},
	{MethodHeartbeat, "Renew a lease and record the run's newly streamed events, in one transaction.", "command", ScopeRun, true, typeOf[HeartbeatParams](), typeOf[HeartbeatResult]()},
	{MethodFinish, "Finish a leased run with its outcome.", "command", ScopeRun, true, typeOf[FinishParams](), typeOf[FinishResult]()},
	{MethodPermit, "Ask the policy engine whether a leased run's Pilot may use a prompted file tool; the decision is recorded before it is returned.", "command", ScopeRun, true, typeOf[PermitParams](), typeOf[PermitResult]()},
	{MethodQuestions, "The Pilots' open questions, as the Tower records them: each flight that is asking, oldest first.", "query", ScopeRead, false, typeOf[NoParams](), typeOf[QuestionsList]()},
	{MethodAnswer, "Answer a flight's open question; the Tower relays the answer to its Pilot and records only its digest.", "command", ScopeDecide, true, typeOf[AnswerParams](), typeOf[AnswerResult]()},
	{MethodRuns, "Runs and their leases, in the order they were queued.", "query", ScopeRead, false, typeOf[RunsParams](), typeOf[RunsPage]()},
	{MethodHeld, "Operations on an org's container that wait on the owner: each would stop the sessions live in it, oldest first.", "query", ScopeRead, false, typeOf[NoParams](), typeOf[HeldList]()},
	{MethodConfirm, "Confirm stopping the live sessions a waiting container operation names; the daemon's next request for it is then cleared, while those sessions are unchanged.", "command", ScopeDecide, true, typeOf[ConfirmParams](), typeOf[ConfirmResult]()},
}

// NewParams returns a pointer to a new, zero params value for the method, ready for DecodeParams.
func (m Method) NewParams() any { return reflect.New(m.Params).Interface() }

// Methods returns the method table, in a stable order.
func Methods() []Method { return append([]Method(nil), methods...) }

// Lookup returns the method with the given name.
func Lookup(name string) (Method, bool) {
	for _, m := range methods {
		if m.Name == name {
			return m, true
		}
	}
	return Method{}, false
}

// NoParams is the params of a method that takes none: any member is refused.
type NoParams struct{}

// HelloParams opens a session of calls.
type HelloParams struct {
	Client        string `json:"client" lucy:"required,maxlen=64" doc:"The calling surface, for example lucy-cli."`
	ClientVersion string `json:"client_version,omitempty" lucy:"maxlen=64" doc:"The calling surface's version."`
	APIVersions   []int  `json:"api_versions" lucy:"required,maxitems=8,itemmin=1,itemmax=1000" doc:"API major versions the client speaks."`
}

// Welcome answers hello.
type Welcome struct {
	APIVersion    int        `json:"api_version" doc:"The API version chosen: the highest both sides speak."`
	ServerVersion string     `json:"server_version" doc:"The daemon's lucy version."`
	Caller        CallerInfo `json:"caller" doc:"Who the daemon thinks is calling."`
	Methods       []string   `json:"methods" doc:"The methods this caller's scopes allow."`
}

// CallerInfo is the caller as the daemon sees it.
type CallerInfo struct {
	Actor     string   `json:"actor" doc:"The caller's name in the event log."`
	Transport string   `json:"transport" doc:"unix, or https once remote control lands."`
	UID       int      `json:"uid" doc:"The peer's user id on the unix socket."`
	PID       int      `json:"pid,omitempty" doc:"The peer's process id, when the OS reports it."`
	Device    string   `json:"device" doc:"local for the socket; a paired device's id remotely."`
	Scopes    []string `json:"scopes" doc:"The scopes granted to this caller."`
}

// Health answers health.
type Health struct {
	Status string `json:"status" doc:"ok when the daemon and its event store answer."`
}

// Status answers status.get.
type Status struct {
	ServerVersion      string    `json:"server_version" doc:"The daemon's lucy version."`
	APIVersion         int       `json:"api_version" doc:"The API version served."`
	PID                int       `json:"pid" doc:"The daemon's process id."`
	StartedAt          time.Time `json:"started_at" doc:"When the daemon started, UTC."`
	UptimeSeconds      int64     `json:"uptime_seconds" doc:"Seconds since the daemon started."`
	EventHead          int64     `json:"event_head" doc:"Sequence number of the last event in the log; 0 when empty."`
	RefusedConnections int64     `json:"refused_connections" doc:"Socket connections refused since start (another user, or no peer credentials)."`
}

// Event list bounds.
const (
	DefaultEventsLimit = 100
	MaxEventsLimit     = 500
)

// EventsParams pages through the event log.
type EventsParams struct {
	AfterSeq int64 `json:"after_seq,omitempty" lucy:"min=0" doc:"Return events with a sequence number greater than this; 0 starts at the beginning."`
	Limit    int   `json:"limit,omitempty" lucy:"min=0,max=500" doc:"At most this many events; 0 means 100."`
}

// EventsPage is one page of the event log.
type EventsPage struct {
	Events []EventInfo `json:"events" doc:"The events, oldest first."`
	Head   int64       `json:"head" doc:"The log's head when the page was read."`
	Next   int64       `json:"next" doc:"Pass as after_seq to read the next page."`
	More   bool        `json:"more" doc:"Whether events after this page existed when it was read."`
}

// EventInfo is one event as the API returns it.
type EventInfo struct {
	Seq           int64           `json:"seq" doc:"Position in the whole log."`
	Stream        string          `json:"stream" doc:"The stream the event belongs to."`
	StreamVersion int64           `json:"stream_version" doc:"Position in its stream."`
	Type          string          `json:"type" doc:"Event type name."`
	TypeVersion   int             `json:"type_version" doc:"Version of the payload schema."`
	Time          time.Time       `json:"time" doc:"When it was appended, UTC."`
	Actor         string          `json:"actor" doc:"Who caused it."`
	Payload       json.RawMessage `json:"payload" doc:"The typed payload as stored; events never carry secrets."`
}

// ShutdownResult answers daemon.shutdown.
type ShutdownResult struct {
	Stopping bool `json:"stopping" doc:"True: the daemon stops after this response."`
}

// EvaluateParams asks the policy engine about one action. The actor is the caller, as the daemon
// identified it; the facts (posture, checks, approvals, resolved paths) are the daemon's own and
// cannot be supplied here.
type EvaluateParams struct {
	Kind     string        `json:"kind" lucy:"required,maxlen=32" doc:"The action: read, write, merge, delete, force_push, branch_delete, history_rewrite, credential_use, exec, fetch or container."`
	Org      string        `json:"org" lucy:"required,maxlen=128" doc:"The org the target belongs to."`
	Project  string        `json:"project,omitempty" lucy:"maxlen=128" doc:"The project (repository), when the action has one."`
	Resource string        `json:"resource,omitempty" lucy:"maxlen=4096" doc:"What the action is on: a PR (pr/12), an absolute path, a branch or a credential name."`
	Inputs   []PolicyInput `json:"inputs,omitempty" lucy:"maxitems=31" doc:"What the request is based on, each with its source. The caller's own request is added as an owner input."`
}

// PolicyInput is one input behind a request: where it came from and a reference to it.
type PolicyInput struct {
	Source string `json:"source" lucy:"required,maxlen=32" doc:"owner, config, daemon, task, agent, tool_output, repo_content, issue, pr_body, pr_comment, web or email."`
	Ref    string `json:"ref,omitempty" lucy:"maxlen=128" doc:"A reference to the input: an event, URL or comment id. Never the text itself."`
}

// PolicyDecision is one recorded policy decision.
type PolicyDecision struct {
	Seq            int64     `json:"seq" doc:"The decision event's position in the whole log."`
	ID             int64     `json:"id" doc:"The decision's position in the policy stream; pass as after to page."`
	Time           time.Time `json:"time" doc:"When it was recorded, UTC."`
	Outcome        string    `json:"outcome" doc:"cleared, denied or holding (waiting on the owner)."`
	Rule           string    `json:"rule" doc:"The rule that decided."`
	Reason         string    `json:"reason" doc:"Why, in one line. {owner} stands for the owner; render it with the configured display title."`
	RuleSetVersion int       `json:"ruleset_version" doc:"The version of the rule set that decided."`
	RuleSetSHA256  string    `json:"ruleset_sha256" doc:"The SHA-256 of that rule set."`
	ActionSHA256   string    `json:"action_sha256" doc:"The digest of the action (actor, kind, target) an approval must name."`
	FactsSHA256    string    `json:"facts_sha256" doc:"The digest of the facts the approver saw (a merge's head SHA, base ref and base SHA; a delete's resolved path; a credential's scope; the default branch for force-push, branch delete and history rewrite) an approval must also name; an approval for the same action under other facts is denied."`
	Tainted        bool      `json:"tainted" doc:"Whether any input came from an untrusted source."`
	ApprovalsUsed  []string  `json:"approvals_used,omitempty" doc:"The approval decisions a clearance rested on, in the order the rules used them (a fixed-list owner approval before a relayed client confirmation); empty when denied or holding."`
	Actor          string    `json:"actor" doc:"Who asked."`
	Role           string    `json:"role" doc:"The actor's role."`
	Kind           string    `json:"kind" doc:"The action."`
	Org            string    `json:"org" doc:"The target's org."`
	Project        string    `json:"project,omitempty" doc:"The target's project."`
	Resource       string    `json:"resource,omitempty" doc:"The target resource. A shell command, URL or search (exec, fetch) is redacted and capped, followed by sha256:<hex> of the original."`
}

// DecisionsParams pages through recorded decisions.
type DecisionsParams struct {
	After int64 `json:"after,omitempty" lucy:"min=0" doc:"Return decisions with an id greater than this; 0 starts at the beginning."`
	Limit int   `json:"limit,omitempty" lucy:"min=0,max=500" doc:"At most this many decisions; 0 means 100."`
}

// DecisionsPage is one page of recorded decisions.
type DecisionsPage struct {
	Decisions []PolicyDecision `json:"decisions" doc:"The decisions, oldest first."`
	Next      int64            `json:"next" doc:"Pass as after to read the next page."`
	More      bool             `json:"more" doc:"Whether decisions after this page existed when it was read."`
}

// ResourcesParams pages through the loaded resources.
type ResourcesParams struct {
	Offset int `json:"offset,omitempty" lucy:"min=0" doc:"Skip this many resources; 0 starts at the beginning."`
	Limit  int `json:"limit,omitempty" lucy:"min=0,max=500" doc:"At most this many resources; 0 means 100."`
}

// ResourcesPage is one page of resources, ordered by namespace, kind and name. The first page also
// carries the latest load's refusals that name no resource of their own.
type ResourcesPage struct {
	Resources        []ResourceInfo `json:"resources" doc:"The resources and their current status."`
	Refused          []RefusedInfo  `json:"refused" doc:"Files and documents the latest load refused that name no resource: a parse error, a missing or invalid kind, name or namespace, or a duplicate. On the first page (offset 0) only; empty on the others."`
	RefusedTruncated int            `json:"refused_truncated,omitempty" doc:"Refusals the latest load left out of refused, beyond the 32 it records."`
	Next             int            `json:"next" doc:"Pass as offset to read the next page."`
	More             bool           `json:"more" doc:"Whether resources after this page existed when it was read."`
}

// RefusedInfo is one refused file or document that has no identity to carry status.
type RefusedInfo struct {
	Source     string          `json:"source" doc:"The file it came from, relative to the config directory, and the document's position in it."`
	Reason     string          `json:"reason" doc:"Why it was refused, the Valid condition's reason, for example MissingNamespace."`
	Conditions []ConditionInfo `json:"conditions" doc:"Its status, as conditions."`
}

// ResourceInfo is one config resource and its observed state. The spec is in the config file;
// the status is lucy's, recorded in its event store.
type ResourceInfo struct {
	APIVersion string          `json:"api_version" doc:"The apiVersion the file declares."`
	Kind       string          `json:"kind" doc:"The resource kind."`
	Namespace  string          `json:"namespace" doc:"The airspace (org) the resource belongs to."`
	Name       string          `json:"name" doc:"The resource's name, unique per namespace and kind."`
	Source     string          `json:"source" doc:"The file it came from, relative to the config directory, and the document's position in it."`
	SpecSHA256 string          `json:"spec_sha256,omitempty" doc:"SHA-256 of the canonical spec, defaults applied; empty when the resource was refused."`
	Conditions []ConditionInfo `json:"conditions" doc:"The resource's status, as conditions."`
}

// ConditionInfo is one aspect of a resource's observed state (spec section 6).
type ConditionInfo struct {
	Type               string `json:"type" doc:"The aspect, for example Valid."`
	Status             string `json:"status" doc:"True, False or Unknown."`
	Reason             string `json:"reason,omitempty" doc:"A machine-readable reason, for example SchemaViolation."`
	Message            string `json:"message,omitempty" doc:"What a person needs to know."`
	LastTransitionTime string `json:"last_transition_time,omitempty" doc:"When the status last changed, RFC 3339 UTC."`
}

// AuditReport answers audit.verify.
type AuditReport struct {
	Verified    bool   `json:"verified" doc:"True when every entry hashes correctly, links to the one before it and matches its event."`
	Entries     int    `json:"entries" doc:"Entries verified; when broken, the entries before the break."`
	Head        string `json:"head,omitempty" doc:"The last entry's hash, when verified."`
	BrokenAtSeq int64  `json:"broken_at_seq,omitempty" doc:"The event whose entry failed, when broken."`
	Reason      string `json:"reason,omitempty" doc:"Why the chain is broken, when it is."`
}

// QueuedParams asks runner.queued.
type QueuedParams struct {
	Placement string `json:"placement,omitempty" lucy:"maxlen=16" doc:"The asking runner's placement: host (this machine; the default) or container (an org's container)."`
}

// QueuedResult answers runner.queued.
type QueuedResult struct {
	Queued int `json:"queued" doc:"Runs waiting for a runner."`
}

// LeaseParams asks for a run.
type LeaseParams struct {
	Runner     string `json:"runner" lucy:"required,maxlen=63" doc:"The runner's id: 1-63 of a-z, 0-9, '.', '_' and '-', starting with a letter or digit."`
	Placement  string `json:"placement,omitempty" lucy:"maxlen=16" doc:"host (this machine; the default) or container (an org's container)."`
	TTLSeconds int    `json:"ttl_seconds,omitempty" lucy:"min=0,max=300" doc:"The lease's life; 0 means 60, and values below 5 mean 5."`
}

// LeaseResult answers runner.lease.
type LeaseResult struct {
	Leased bool     `json:"leased" doc:"False when no run was queued; run is then empty."`
	Run    RunLease `json:"run" doc:"The leased run."`
}

// RunLease is a leased run: its task, its fencing token and its expiry.
type RunLease struct {
	Run        string    `json:"run" doc:"The run's id; a Pilot run is <flight>#<generation>."`
	Org        string    `json:"org" doc:"The org (airspace) the run belongs to."`
	Project    string    `json:"project" doc:"The project (runway)."`
	Flight     string    `json:"flight" doc:"The flight (task) the run is for."`
	Generation int       `json:"generation" doc:"The flight's Pilot generation: 1, then +1 for each replacement."`
	Goal       string    `json:"goal" doc:"What the run is for. Data for the work, never instructions to lucy."`
	Base       string    `json:"base" doc:"The branch the work targets."`
	Fence      int64     `json:"fence" doc:"The fencing token: name it on every heartbeat and finish. Fences only grow; an old one is refused."`
	ExpiresAt  time.Time `json:"expires_at" doc:"When the lease expires unless renewed, UTC."`
}

// HeartbeatParams renews a lease and streams events.
type HeartbeatParams struct {
	Runner     string     `json:"runner" lucy:"required,maxlen=63" doc:"The runner holding the lease."`
	Run        string     `json:"run" lucy:"required,maxlen=256" doc:"The leased run."`
	Fence      int64      `json:"fence" lucy:"min=1" doc:"The lease's fencing token."`
	TTLSeconds int        `json:"ttl_seconds,omitempty" lucy:"min=0,max=300" doc:"The renewed life; 0 means 60, and values below 5 mean 5."`
	Events     []RunEvent `json:"events,omitempty" lucy:"maxitems=32" doc:"The run's new events, numbered from 1 without gaps; a resent event is skipped."`
	Acked      string     `json:"acked,omitempty" lucy:"maxlen=200" doc:"The id of the question whose answer, from an earlier heartbeat's result, the runner gave its work. Until it is acknowledged the answer comes again in every result. An id the owner did not answer under this lease is ignored."`
}

// RunEvent is one event a runner streams.
type RunEvent struct {
	Seq      int64  `json:"seq" lucy:"min=1" doc:"The event's number in its run, from 1."`
	Kind     string `json:"kind" lucy:"required,maxlen=16" doc:"output, activity, asking or hook."`
	Text     string `json:"text,omitempty" lucy:"maxlen=1024" doc:"The event's text: a line of output, a summary, or the question. Untrusted."`
	Question string `json:"question,omitempty" lucy:"maxlen=200" doc:"The question's id: required on an asking event and the same on every repeat of the question, refused on any other. An asking event for an answered question is recorded as activity."`
	Hook     string `json:"hook,omitempty" lucy:"maxlen=32" doc:"The harness lifecycle hook that fired: required on a hook event, refused on any other. SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Notification, Stop, SubagentStop, PreCompact or SessionEnd."`
}

// HeartbeatResult answers runner.heartbeat.
type HeartbeatResult struct {
	ExpiresAt time.Time `json:"expires_at" doc:"The lease's new expiry, UTC."`
	Stop      bool      `json:"stop" doc:"The Tower asked for the run to stop: stop the work and finish the run as stopped."`
	Accepted  int64     `json:"accepted" doc:"The highest event number recorded for the run."`
	Answer    string    `json:"answer,omitempty" doc:"The owner's answer to the run's question, relayed by the Tower. Give it to the work once, then acknowledge it; it comes again in every result until acknowledged."`
	Question  string    `json:"question,omitempty" doc:"With answer: the id of the question it answers, to send back as acked."`
	Asking    bool      `json:"asking" doc:"The run's last recorded event is a question still waiting on an answer. False with no answer after the runner asked means the answer was lost: ask again."`
}

// FinishParams ends a run.
type FinishParams struct {
	Runner  string `json:"runner" lucy:"required,maxlen=63" doc:"The runner holding the lease."`
	Run     string `json:"run" lucy:"required,maxlen=256" doc:"The leased run."`
	Fence   int64  `json:"fence" lucy:"min=1" doc:"The lease's fencing token."`
	Outcome string `json:"outcome" lucy:"required,maxlen=16" doc:"done, failed, stopped or lost."`
	PR      int    `json:"pr,omitempty" lucy:"min=0" doc:"When done: the pull request the work opened."`
	Reason  string `json:"reason,omitempty" lucy:"maxlen=1024" doc:"When not done: why. Untrusted."`
}

// FinishResult answers runner.finish.
type FinishResult struct {
	State string `json:"state" doc:"The run's final state: done, failed, stopped or lost."`
}

// PermitParams asks about a Pilot's prompted file tool. The actor, org and project come from the
// lease, never from these params.
type PermitParams struct {
	Runner   string `json:"runner" lucy:"required,maxlen=63" doc:"The runner holding the lease."`
	Run      string `json:"run" lucy:"required,maxlen=256" doc:"The leased run."`
	Fence    int64  `json:"fence" lucy:"min=1" doc:"The lease's fencing token."`
	Tool     string `json:"tool" lucy:"required,maxlen=64" doc:"The prompted tool's name, as the harness gave it. Untrusted."`
	Kind     string `json:"kind" lucy:"required,maxlen=16" doc:"read, write, exec or fetch."`
	Resource string `json:"resource,omitempty" lucy:"maxlen=4096" doc:"For a read or write, the absolute path inside the run's directory, empty when the tool names none; for an exec, the shell command; for a fetch, the URL or search. Untrusted text: the engine decides on it as written, and an exec or fetch is recorded redacted, with its SHA-256."`
}

// PermitResult answers runner.permit.
type PermitResult struct {
	Outcome string `json:"outcome" doc:"cleared, denied or holding. Only cleared lets the tool run."`
	Rule    string `json:"rule" doc:"The rule that decided."`
	Reason  string `json:"reason" doc:"Why, in words."`
}

// RunsParams pages through runs.
type RunsParams struct {
	After int64 `json:"after,omitempty" lucy:"min=0" doc:"Return runs queued after this position; 0 starts at the beginning."`
	Limit int   `json:"limit,omitempty" lucy:"min=0,max=500" doc:"At most this many runs; 0 means 100."`
}

// RunsPage is one page of runs.
type RunsPage struct {
	Runs []RunInfo `json:"runs" doc:"The runs, in the order they were queued."`
	Next int64     `json:"next" doc:"Pass as after to read the next page."`
	More bool      `json:"more" doc:"Whether runs after this page existed when it was read."`
}

// RunInfo is one run and its lease, as the daemon's records hold it.
type RunInfo struct {
	Run        string    `json:"run" doc:"The run's id."`
	Org        string    `json:"org" doc:"The org."`
	Project    string    `json:"project" doc:"The project."`
	Flight     string    `json:"flight" doc:"The flight the run is for."`
	Generation int       `json:"generation" doc:"The flight's Pilot generation."`
	State      string    `json:"state" doc:"queued, leased, done, failed, stopped or lost."`
	Runner     string    `json:"runner,omitempty" doc:"The runner that holds or held it."`
	Placement  string    `json:"placement,omitempty" doc:"host or container."`
	Fence      int64     `json:"fence,omitempty" doc:"The current or last lease's fencing token."`
	ExpiresAt  time.Time `json:"expires_at,omitempty,omitzero" doc:"When leased: the lease's expiry, UTC."`
	Accepted   int64     `json:"accepted" doc:"Events recorded for the run."`
	Activity   time.Time `json:"activity" doc:"When the run last did something, UTC."`
	Asking     bool      `json:"asking,omitempty" doc:"The run's last event was a question."`
	Stop       bool      `json:"stop,omitempty" doc:"A stop was requested."`
	PR         int       `json:"pr,omitempty" doc:"When done: the pull request."`
	Reason     string    `json:"reason,omitempty" doc:"When failed, stopped or lost: why. Untrusted."`
	Position   int64     `json:"position" doc:"The run's place in the queue; pass as after to page."`
}

// QuestionsList answers questions.list.
type QuestionsList struct {
	Questions []Question `json:"questions" doc:"Every flight that is asking, oldest question first; empty when none is."`
}

// Question is one flight's open question.
type Question struct {
	Flight   string    `json:"flight" doc:"The flight that is asking."`
	Org      string    `json:"org" doc:"The flight's airspace."`
	Project  string    `json:"project" doc:"The flight's runway."`
	Question string    `json:"question" doc:"The Pilot's question, as the Tower recorded it. Untrusted."`
	Since    time.Time `json:"since" doc:"When the flight started asking, UTC."`
}

// AnswerParams answers one flight's question.
type AnswerParams struct {
	Flight string `json:"flight" lucy:"required,maxlen=200" doc:"The flight that is asking."`
	Answer string `json:"answer" lucy:"required,maxlen=4096" doc:"The owner's answer. It reaches the Pilot through the Tower; the log keeps only its digest."`
}

// AnswerResult answers questions.answer.
type AnswerResult struct {
	Flight string `json:"flight" doc:"The flight answered."`
	State  string `json:"state" doc:"The flight's state after the answer, normally flying."`
}

// HeldList answers sandbox.held.
type HeldList struct {
	Held []HeldOp `json:"held" doc:"Every container operation that waits on the owner, oldest first; empty when none does."`
}

// HeldOp is one container operation that waits on the owner.
type HeldOp struct {
	Org          string    `json:"org" doc:"The org whose container it is."`
	Op           string    `json:"op" doc:"up, restart or down: each stops the work running in the container."`
	Decision     int64     `json:"decision" doc:"The policy decision that waits; a confirmation names it."`
	Outcome      string    `json:"outcome" doc:"holding, or denied when an earlier confirmation lapsed because the live sessions changed."`
	LiveSessions []string  `json:"live_sessions" doc:"The runs live in the container, which the operation would stop."`
	Since        time.Time `json:"since" doc:"When it was decided, UTC."`
}

// ConfirmParams confirms one waiting container operation.
type ConfirmParams struct {
	Org      string `json:"org" lucy:"required,maxlen=128" doc:"The org whose container it is."`
	Op       string `json:"op" lucy:"required,maxlen=16" doc:"up, restart or down."`
	Decision int64  `json:"decision" lucy:"min=1" doc:"The waiting decision, as sandbox.held shows it. A newer request replaces it, and a confirmation of the old one is refused."`
}

// ConfirmResult answers sandbox.confirm.
type ConfirmResult struct {
	Org      string `json:"org" doc:"The org confirmed."`
	Op       string `json:"op" doc:"The operation confirmed."`
	Approval string `json:"approval" doc:"The confirmation's own decision id, which the cleared request will name."`
}
