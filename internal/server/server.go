// Package server serves lucy's typed API (package api) over HTTP/1.1: today on the daemon's unix
// socket, behind a PeerListener that admits only the owner; from m1 the same handler serves HTTPS
// for paired devices, behind their own authentication. The handler never trusts the transport
// alone: every call is authorized against the caller on its context (LUCY-API-004), decoded
// strictly (LUCY-API-006), and, if it mutates anything, recorded as an api.command event before it
// runs (LUCY-API-005).
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"reflect"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// Timeouts. They bound how long one slow or stuck client can hold a connection or a handler.
const (
	DefaultCallTimeout = 10 * time.Second
	ReadHeaderTimeout  = 5 * time.Second
	ReadTimeout        = 15 * time.Second
	WriteTimeout       = 30 * time.Second
	IdleTimeout        = 60 * time.Second
	MaxHeaderBytes     = 8 << 10
)

// CommandStream is the event stream api.command events are appended to.
const CommandStream = "api"

// CommandEvent records one mutating call, appended before the call runs (LUCY-API-005). The params
// are bound by digest: a command's own events carry whatever of its input must be kept.
type CommandEvent struct {
	Method       string `json:"method"`
	RequestID    string `json:"request_id"` // the JSON-RPC id, as sent
	Transport    string `json:"transport"`
	UID          int    `json:"uid"`
	PID          int    `json:"pid,omitempty"`
	Device       string `json:"device"`
	ParamsSHA256 string `json:"params_sha256"`
}

// EventType implements eventstore.Payload.
func (CommandEvent) EventType() string { return "api.command" }

// EventVersion implements eventstore.Payload.
func (CommandEvent) EventVersion() int { return 1 }

// Options configures a Server.
type Options struct {
	Store       *eventstore.Store // required; its registry must include CommandEvent and policy.DecisionRecord
	Authorizer  Authorizer        // OwnerOnly for the current user when nil
	Version     string            // lucy's version, reported by hello and status.get
	Log         *slog.Logger      // slog.Default when nil
	Now         func() time.Time  // time.Now when nil
	CallTimeout time.Duration     // DefaultCallTimeout when zero
	OnShutdown  func()            // called by daemon.shutdown once it is recorded; must not block
	Refused     func() int64      // the socket's refused-connection count, for status.get
	// PolicyFacts gathers the facts policy.evaluate decides on, from the daemon's trusted sources.
	// Nil means no facts are known, so every privileged action is denied or escalated.
	PolicyFacts func(ctx context.Context, req policy.Request) (policy.Facts, error)
}

// Server is the API's HTTP handler.
type Server struct {
	opts     Options
	started  time.Time
	handlers map[string]handler
}

type handler struct {
	params reflect.Type
	call   func(ctx context.Context, params any) (any, error)
}

func bind[P, R any](fn func(context.Context, *P) (R, error)) handler {
	return handler{
		params: reflect.TypeFor[P](),
		call:   func(ctx context.Context, p any) (any, error) { return fn(ctx, p.(*P)) },
	}
}

// New returns a Server. It fails if a method in the API's table has no handler, or a handler's
// params type differs from the table's.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("server: Options.Store is required")
	}
	if opts.Authorizer == nil {
		opts.Authorizer = OwnerOnly{UID: os.Getuid()}
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = DefaultCallTimeout
	}
	s := &Server{opts: opts, started: opts.Now()}
	s.handlers = s.bindAll()
	for _, m := range api.Methods() {
		h, ok := s.handlers[m.Name]
		if !ok {
			return nil, errors.New("server: no handler for " + m.Name)
		}
		if h.params != m.Params {
			return nil, errors.New("server: handler params for " + m.Name + " differ from the API table")
		}
	}
	if len(s.handlers) != len(api.Methods()) {
		return nil, errors.New("server: a handler is bound to a method the API table does not declare")
	}
	return s, nil
}

// HTTPServer returns an http.Server for this handler with lucy's timeouts and size limits. Serve it
// on a PeerListener so each connection carries its caller.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Handler:           s,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(s.opts.Log.Handler(), slog.LevelWarn),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if cc, ok := c.(*callerConn); ok {
				return api.WithCaller(ctx, cc.caller)
			}
			return ctx // no caller: every call is refused
		},
	}
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	nullID := json.RawMessage("null")
	if r.URL.Path != api.Path {
		s.write(w, http.StatusNotFound, api.NewErrorResponse(nullID, api.NewError(api.CodeMethodNotFound, "POST requests to "+api.Path)))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.write(w, http.StatusMethodNotAllowed, api.NewErrorResponse(nullID, api.NewError(api.CodeInvalidRequest, "use POST")))
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		s.write(w, http.StatusUnsupportedMediaType, api.NewErrorResponse(nullID, api.NewError(api.CodeInvalidRequest, "Content-Type must be application/json")))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, api.MaxRequestBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.write(w, http.StatusRequestEntityTooLarge, api.NewErrorResponse(nullID, api.NewError(api.CodeTooLarge, "requests are limited to 64 KiB")))
			return
		}
		s.write(w, http.StatusBadRequest, api.NewErrorResponse(nullID, api.NewError(api.CodeParseError, "the body could not be read")))
		return
	}
	id, req, derr := api.DecodeRequest(body)
	if derr != nil {
		s.write(w, http.StatusOK, api.NewErrorResponse(id, derr))
		return
	}
	s.write(w, http.StatusOK, s.dispatch(r.Context(), req))
}

// dispatch runs one decoded request: authorize, decode params, record if mutating, call.
func (s *Server) dispatch(ctx context.Context, req *api.Request) *api.Response {
	fail := func(e *api.Error) *api.Response { return api.NewErrorResponse(req.ID, e) }
	caller, ok := api.CallerFrom(ctx)
	if !ok {
		return fail(api.NewError(api.CodeForbidden, "the connection carries no caller identity"))
	}
	m, ok := api.Lookup(req.Method)
	if !ok {
		return fail(api.FieldError(api.CodeMethodNotFound, "method", "call rpc.discover for the method list"))
	}
	if e := s.opts.Authorizer.Authorize(ctx, caller, m); e != nil {
		s.opts.Log.Warn("api call refused", "method", m.Name, "actor", caller.Actor(), "pid", caller.PID)
		return fail(e)
	}
	caller.Scopes = s.opts.Authorizer.Scopes(ctx, caller)
	ctx = api.WithCaller(ctx, caller)
	params := m.NewParams()
	if e := api.DecodeParams(req.Params, params); e != nil {
		return fail(e)
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.CallTimeout)
	defer cancel()
	if m.Mutating {
		if err := s.record(ctx, caller, m, req); err != nil {
			return fail(s.internal(m, err))
		}
	}
	result, err := s.call(ctx, m, params)
	if err != nil {
		var ae *api.Error
		switch {
		case errors.As(err, &ae):
			return fail(ae)
		case errors.Is(err, context.DeadlineExceeded):
			return fail(api.NewError(api.CodeTimeout, "the call took longer than its deadline"))
		}
		return fail(s.internal(m, err))
	}
	resp, err := api.NewResult(req.ID, result)
	if err != nil {
		return fail(s.internal(m, err))
	}
	return resp
}

// call runs a handler, turning a panic into an internal error so one bad call cannot take the
// daemon down.
func (s *Server) call(ctx context.Context, m api.Method, params any) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("panic in handler")
			s.opts.Log.Error("api handler panicked", "method", m.Name, "panic", r)
		}
	}()
	return s.handlers[m.Name].call(ctx, params)
}

// record appends the api.command event for a mutating call. If it cannot be recorded, the call
// does not run.
func (s *Server) record(ctx context.Context, c api.Caller, m api.Method, req *api.Request) error {
	sum := sha256.Sum256(req.Params)
	ev := CommandEvent{
		Method: m.Name, RequestID: string(req.ID), Transport: c.Transport, UID: c.UID, PID: c.PID,
		Device: c.Device, ParamsSHA256: hex.EncodeToString(sum[:]),
	}
	_, err := s.opts.Store.Append(ctx, CommandStream, eventstore.AnyVersion, c.Actor(), ev)
	return err
}

// internal logs err with a fresh correlation id and returns an error that carries only the id.
func (s *Server) internal(m api.Method, err error) *api.Error {
	var b [8]byte
	_, _ = rand.Read(b[:])
	rid := hex.EncodeToString(b[:])
	s.opts.Log.Error("api call failed", "method", m.Name, "request_id", rid, "err", err)
	e := api.NewError(api.CodeInternal, "the daemon's log has the details under request_id")
	e.Data.RequestID = rid
	return e
}

func (s *Server) write(w http.ResponseWriter, status int, resp *api.Response) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.opts.Log.Error("api response encoding failed", "err", err)
		data = []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}
