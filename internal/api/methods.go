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
	MethodDiscover = "rpc.discover"
	MethodHello    = "hello"
	MethodHealth   = "health"
	MethodStatus   = "status.get"
	MethodEvents   = "events.list"
	MethodShutdown = "daemon.shutdown"
)

var methods = []Method{
	{MethodDiscover, "The API's OpenRPC document: every method, its params, result and errors.", "query", ScopeRead, false, typeOf[NoParams](), typeOf[json.RawMessage]()},
	{MethodHello, "Negotiate the API version and learn the caller's identity, scopes and the methods it may call.", "hello", ScopeRead, false, typeOf[HelloParams](), typeOf[Welcome]()},
	{MethodHealth, "Liveness: the daemon is up and its event store answers.", "query", ScopeRead, false, typeOf[NoParams](), typeOf[Health]()},
	{MethodStatus, "The daemon's version, uptime and event log head.", "query", ScopeRead, false, typeOf[NoParams](), typeOf[Status]()},
	{MethodEvents, "Events from the log after a sequence number, oldest first.", "query", ScopeRead, false, typeOf[EventsParams](), typeOf[EventsPage]()},
	{MethodShutdown, "Stop the daemon gracefully after answering.", "command", ScopeAdmin, true, typeOf[NoParams](), typeOf[ShutdownResult]()},
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
