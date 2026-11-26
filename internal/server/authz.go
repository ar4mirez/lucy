package server

import (
	"context"

	"github.com/ar4mirez/lucy/internal/api"
)

// Authorizer decides whether a caller may call a method, and which scopes it holds. Every call
// passes it before its params are even decoded (LUCY-API-004). The policy engine (m0 slice 5)
// replaces OwnerOnly with object-level checks on org and task; paired devices (m1) get their scopes
// from their pairing record.
type Authorizer interface {
	// Scopes returns the scopes the caller holds. An empty result means the caller may call nothing.
	Scopes(ctx context.Context, c api.Caller) []api.Scope
	// Authorize returns nil if the caller may call m, else an error with code CodeForbidden.
	Authorize(ctx context.Context, c api.Caller, m api.Method) *api.Error
}

// OwnerOnly grants every scope to the local owner, the user the daemon runs as, and nothing to
// anyone else. The socket's peer check already refuses other users; this is the second check.
type OwnerOnly struct {
	UID int
}

// Scopes implements Authorizer.
func (o OwnerOnly) Scopes(_ context.Context, c api.Caller) []api.Scope {
	if c.Transport != "unix" || c.UID != o.UID {
		return nil
	}
	return append([]api.Scope(nil), api.AllScopes...)
}

// Authorize implements Authorizer.
func (o OwnerOnly) Authorize(ctx context.Context, c api.Caller, m api.Method) *api.Error {
	for _, s := range o.Scopes(ctx, c) {
		if s == m.Scope {
			return nil
		}
	}
	return api.NewError(api.CodeForbidden, "this caller lacks the "+string(m.Scope)+" scope")
}
