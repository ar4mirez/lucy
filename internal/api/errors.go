package api

import (
	"errors"
	"fmt"
)

// Code is a JSON-RPC error code. The -32700..-32600 range is JSON-RPC 2.0's own; lucy's codes sit
// in the server range -32099..-32000.
type Code int

// Error codes.
const (
	CodeParseError         Code = -32700 // the body is not valid JSON
	CodeInvalidRequest     Code = -32600 // valid JSON, not a valid lucy request
	CodeMethodNotFound     Code = -32601 // no such method in this API version
	CodeInvalidParams      Code = -32602 // params do not match the method's schema
	CodeInternal           Code = -32603 // the daemon failed; details are only in its log
	CodeForbidden          Code = -32001 // the caller lacks the scope or access to the object
	CodeUnsupportedVersion Code = -32002 // no API version in common
	CodeTooLarge           Code = -32003 // the request exceeds a size limit
	CodeUnavailable        Code = -32004 // the daemon cannot serve now (shutting down, store down)
	CodeTimeout            Code = -32005 // the call ran past its deadline
	CodeFenced             Code = -32006 // a runner's lease no longer holds the run: expired, finished or replaced
	CodeSandboxBusy        Code = -32007 // every queued run's org container is being restarted or stopped; lease again later
	CodeRunnerOrg          Code = -32008 // a host runner named no org
	CodeHostOrg            Code = -32009 // host placement serves another org, or a host run is live
)

// codeInfo is the fixed message and kind of each code. Messages never carry request-specific or
// internal text, so nothing about the daemon's internals leaks through an error.
var codeInfo = map[Code]struct{ kind, message string }{
	CodeParseError:         {"parse_error", "parse error"},
	CodeInvalidRequest:     {"invalid_request", "invalid request"},
	CodeMethodNotFound:     {"method_not_found", "method not found"},
	CodeInvalidParams:      {"invalid_params", "invalid params"},
	CodeInternal:           {"internal", "internal error"},
	CodeForbidden:          {"forbidden", "forbidden"},
	CodeUnsupportedVersion: {"unsupported_version", "unsupported API version"},
	CodeTooLarge:           {"too_large", "request too large"},
	CodeUnavailable:        {"unavailable", "daemon unavailable"},
	CodeTimeout:            {"timeout", "deadline exceeded"},
	CodeFenced:             {"fenced", "lease no longer held"},
	CodeSandboxBusy:        {"sandbox_busy", "org container busy"},
	CodeRunnerOrg:          {"runner_org", "a host runner leases for one org"},
	CodeHostOrg:            {"host_org", "host placement serves another org"},
}

// Codes lists every error code, in a stable order.
var Codes = []Code{
	CodeParseError, CodeInvalidRequest, CodeMethodNotFound, CodeInvalidParams, CodeInternal,
	CodeForbidden, CodeUnsupportedVersion, CodeTooLarge, CodeUnavailable, CodeTimeout, CodeFenced,
	CodeSandboxBusy, CodeRunnerOrg, CodeHostOrg,
}

// Kind is the code's stable machine-readable name, for example "invalid_params".
func (c Code) Kind() string { return codeInfo[c].kind }

// Message is the code's fixed message.
func (c Code) Message() string { return codeInfo[c].message }

// Known reports whether c is one of lucy's codes.
func (c Code) Known() bool { _, ok := codeInfo[c]; return ok }

// Error is a JSON-RPC error object. It is also a Go error, returned by the client.
type Error struct {
	Code    Code       `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

// ErrorData is the typed detail of an error. It only ever describes the caller's own request.
type ErrorData struct {
	Kind      string `json:"kind" doc:"Stable name of the error code, for example invalid_params."`
	Field     string `json:"field,omitempty" doc:"The params field (or request member) at fault."`
	Help      string `json:"help,omitempty" doc:"What to change in the request."`
	Supported []int  `json:"supported,omitempty" doc:"API versions the daemon serves (unsupported_version)."`
	RequestID string `json:"request_id,omitempty" doc:"Correlation id for an internal error; the daemon's log has the details."`
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("lucy api: %s (%d)", e.Message, e.Code)
	if e.Data != nil {
		if e.Data.Field != "" {
			msg += ": " + e.Data.Field
		}
		if e.Data.Help != "" {
			msg += ": " + e.Data.Help
		}
	}
	return msg
}

// Is makes errors.Is(err, &Error{Code: c}) match on the code alone.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// NewError returns an error with the code's fixed message and kind, and optional help.
func NewError(code Code, help string) *Error {
	return &Error{Code: code, Message: code.Message(), Data: &ErrorData{Kind: code.Kind(), Help: help}}
}

// FieldError returns an error naming the request field at fault.
func FieldError(code Code, field, help string) *Error {
	e := NewError(code, help)
	e.Data.Field = field
	return e
}

// IsCode reports whether err is an *Error with the given code.
func IsCode(err error, code Code) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
