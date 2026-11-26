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
	MethodEntries   = "audit.entries"
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
	{MethodEntries, "Audit entries in the spec's wire format, oldest first.", "query", ScopeRead, false, typeOf[EntriesParams](), typeOf[EntriesPage]()},
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
	Kind     string        `json:"kind" lucy:"required,maxlen=32" doc:"The action: read, write, merge, delete, force_push, branch_delete, history_rewrite or credential_use."`
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
	Resource       string    `json:"resource,omitempty" doc:"The target resource."`
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

// ResourcesPage is one page of resources, ordered by namespace, kind and name.
type ResourcesPage struct {
	Resources []ResourceInfo `json:"resources" doc:"The resources and their current status."`
	Next      int            `json:"next" doc:"Pass as offset to read the next page."`
	More      bool           `json:"more" doc:"Whether resources after this page existed when it was read."`
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

// EntriesParams pages through audit entries.
type EntriesParams struct {
	AfterSeq int64 `json:"after_seq,omitempty" lucy:"min=0" doc:"Return entries for events after this sequence number; 0 starts at the beginning."`
	Limit    int   `json:"limit,omitempty" lucy:"min=0,max=500" doc:"At most this many entries; 0 means 100."`
}

// EntriesPage is one page of the audit chain.
type EntriesPage struct {
	Entries []json.RawMessage `json:"entries" doc:"Audit entries (spec section 12), each in RFC 8785 canonical form, oldest first."`
	Next    int64             `json:"next" doc:"Pass as after_seq to read the next page."`
	More    bool              `json:"more" doc:"Whether entries after this page existed when it was read."`
}
