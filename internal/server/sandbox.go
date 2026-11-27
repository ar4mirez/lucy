package server

import (
	"context"
	"errors"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/sandbox"
)

// Containers is the daemon's guarded view of the org containers (LUCY-SBX-003): the operations
// that wait on the owner, and the owner's confirmation of one. *sandbox.Sandbox implements it.
type Containers interface {
	Held(ctx context.Context) ([]sandbox.Held, error)
	Confirm(ctx context.Context, org string, op sandbox.Op, decision int64) (policy.Approval, error)
}

var _ Containers = (*sandbox.Sandbox)(nil)

func (s *Server) sandboxHeld(ctx context.Context, _ *api.NoParams) (api.HeldList, error) {
	out := api.HeldList{Held: []api.HeldOp{}}
	if s.opts.Containers == nil {
		return out, nil // no Sandbox binding open, so nothing can wait on the owner
	}
	held, err := s.opts.Containers.Held(ctx)
	if err != nil {
		return api.HeldList{}, err
	}
	for _, h := range held {
		live := h.LiveSessions
		if live == nil {
			live = []string{}
		}
		out.Held = append(out.Held, api.HeldOp{Org: h.Org, Op: string(h.Op), Decision: h.DecisionSeq,
			Outcome: string(h.Outcome), LiveSessions: live, Since: h.Since.UTC()})
	}
	return out, nil
}

func (s *Server) sandboxConfirm(ctx context.Context, p *api.ConfirmParams) (api.ConfirmResult, error) {
	if s.opts.Containers == nil {
		return api.ConfirmResult{}, api.NewError(api.CodeUnavailable,
			"this daemon has no Sandbox binding open yet, so no container operation waits on the owner; the daemon opens it with m1 slice 3")
	}
	if !sandbox.ValidOrg(p.Org) {
		return api.ConfirmResult{}, api.FieldError(api.CodeInvalidParams, "org", "name an org as `lucy sandbox held` shows it")
	}
	op := sandbox.Op(p.Op)
	if !op.Valid() {
		return api.ConfirmResult{}, api.FieldError(api.CodeInvalidParams, "op", "the operation is up, restart or down")
	}
	a, err := s.opts.Containers.Confirm(ctx, p.Org, op, p.Decision)
	switch {
	case errors.Is(err, sandbox.ErrNothingHeld):
		return api.ConfirmResult{}, api.FieldError(api.CodeInvalidParams, "op",
			"nothing waits on the owner for this operation; run `lucy sandbox held` to see what does")
	case errors.Is(err, sandbox.ErrStale):
		return api.ConfirmResult{}, api.FieldError(api.CodeInvalidParams, "decision",
			"a newer request replaced this decision; run `lucy sandbox held` and confirm the one shown now")
	case err != nil:
		return api.ConfirmResult{}, err
	}
	return api.ConfirmResult{Org: p.Org, Op: p.Op, Approval: a.Decision}, nil
}
