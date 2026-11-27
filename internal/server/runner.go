package server

import (
	"context"
	"errors"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/capability"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
)

// The runner protocol's methods (LUCY-RUN-001): a thin mapping of the API onto the Broker, which
// records every lease, heartbeat, streamed event and finish in the event store.

func (s *Server) broker() (*runner.Broker, error) {
	if s.opts.Runs == nil {
		return nil, api.NewError(api.CodeUnavailable, "this daemon serves no runners")
	}
	return s.opts.Runs, nil
}

// runnerError maps the broker's refusals onto the API's errors.
func runnerError(err error) error {
	switch {
	case errors.Is(err, runner.ErrFenced):
		return api.NewError(api.CodeFenced, "stop the run's work and do not send under this fence again; lease a new run")
	case errors.Is(err, runner.ErrNoRun):
		return api.FieldError(api.CodeInvalidParams, "run", "no such run")
	case errors.Is(err, runner.ErrSequence):
		return api.FieldError(api.CodeInvalidParams, "events", "number a run's events from 1 without gaps; resend from the accepted number")
	case errors.Is(err, runner.ErrSandboxBusy):
		return api.NewError(api.CodeSandboxBusy, "every queued run's org container is being restarted or stopped; lease again later")
	case errors.Is(err, runner.ErrInvalid), errors.Is(err, runner.ErrExists):
		return api.NewError(api.CodeInvalidParams, "check the runner id, placement, event kinds and outcome")
	}
	return err
}

func (s *Server) runnerQueued(ctx context.Context, p *api.QueuedParams) (api.QueuedResult, error) {
	b, err := s.broker()
	if err != nil {
		return api.QueuedResult{}, err
	}
	n, err := b.Queued(ctx, runner.Placement(p.Placement))
	if err != nil {
		return api.QueuedResult{}, runnerError(err)
	}
	return api.QueuedResult{Queued: n}, nil
}

func (s *Server) runnerLease(ctx context.Context, p *api.LeaseParams) (api.LeaseResult, error) {
	b, err := s.broker()
	if err != nil {
		return api.LeaseResult{}, err
	}
	placement := runner.Placement(p.Placement)
	if placement == "" {
		placement = runner.PlacementHost
	}
	l, ok, err := b.Lease(ctx, p.Runner, placement, time.Duration(p.TTLSeconds)*time.Second)
	if err != nil {
		return api.LeaseResult{}, runnerError(err)
	}
	if !ok {
		return api.LeaseResult{}, nil
	}
	res := api.LeaseResult{Leased: true, Run: api.RunLease{
		Run: l.Run, Org: l.Org, Project: l.Project, Flight: l.Flight, Generation: l.Generation, Goal: l.Goal,
		Base: l.Base, Fence: l.Fence, ExpiresAt: l.Expires,
	}}
	if s.opts.Capabilities != nil {
		// The run's token for its org's gateway, for the org's routes, bound to this lease. If it
		// cannot be issued the lease is not handed out; it expires unrenewed.
		var routes []string
		if s.opts.Gateways != nil {
			rs, _ := s.opts.Gateways.Routes(l.Org)
			for _, r := range rs {
				routes = append(routes, r.Name)
			}
		}
		tok, _, err := s.opts.Capabilities.Issue(ctx, l.Org, l.Run, l.Fence, routes, 0)
		if err != nil {
			return api.LeaseResult{}, err
		}
		res.Run.Capability = tok
	}
	return res, nil
}

func (s *Server) runnerHeartbeat(ctx context.Context, p *api.HeartbeatParams) (api.HeartbeatResult, error) {
	b, err := s.broker()
	if err != nil {
		return api.HeartbeatResult{}, err
	}
	evs := make([]runner.Event, 0, len(p.Events))
	for _, e := range p.Events {
		evs = append(evs, runner.Event{Seq: e.Seq, Kind: runner.EventKind(e.Kind), Text: e.Text, Question: e.Question, Hook: e.Hook})
	}
	beat, err := b.Heartbeat(ctx, p.Runner, p.Run, p.Fence, time.Duration(p.TTLSeconds)*time.Second, evs, p.Acked)
	if err != nil {
		return api.HeartbeatResult{}, runnerError(err)
	}
	res := api.HeartbeatResult{ExpiresAt: beat.Expires, Stop: beat.Stop, Accepted: beat.Accepted, Answer: beat.Answer, Question: beat.Question, Asking: beat.Asking}
	if s.opts.Capabilities == nil {
		return res, nil
	}
	if beat.Stop {
		return res, s.revokeRun(ctx, p.Run, p.Fence, "stopped")
	}
	// A live lease's token is renewed on each heartbeat and the one before it rotated out, up to
	// MaxTTL from the lease's start; a revoked token is not renewed. A lease that ran out gets no
	// heartbeat, and the daemon's expiry timer revokes its tokens.
	tok, _, err := s.opts.Capabilities.Renew(ctx, p.Run, p.Fence)
	switch {
	case err == nil:
		res.Capability = tok
	case errors.Is(err, capability.ErrUnknown), errors.Is(err, capability.ErrRevoked), errors.Is(err, capability.ErrExpired):
	default:
		return api.HeartbeatResult{}, err
	}
	return res, nil
}

// revokeRun revokes run's tokens under fence and every older one, for reason, when this daemon
// issues any.
func (s *Server) revokeRun(ctx context.Context, run string, fence int64, reason string) error {
	if s.opts.Capabilities == nil {
		return nil
	}
	return s.opts.Capabilities.RevokeRun(ctx, run, fence, reason)
}

func (s *Server) runnerFinish(ctx context.Context, p *api.FinishParams) (api.FinishResult, error) {
	b, err := s.broker()
	if err != nil {
		return api.FinishResult{}, err
	}
	res := runner.Result{Outcome: runner.Outcome(p.Outcome), PR: p.PR, Reason: p.Reason}
	if err := b.Finish(ctx, p.Runner, p.Run, p.Fence, res); err != nil {
		return api.FinishResult{}, runnerError(err)
	}
	if err := s.revokeRun(ctx, p.Run, p.Fence, "finished"); err != nil {
		return api.FinishResult{}, err
	}
	ri, err := b.Run(ctx, p.Run)
	if err != nil {
		return api.FinishResult{}, err
	}
	return api.FinishResult{State: string(ri.State)}, nil
}

func (s *Server) runs(ctx context.Context, p *api.RunsParams) (api.RunsPage, error) {
	b, err := s.broker()
	if err != nil {
		return api.RunsPage{}, err
	}
	limit := p.Limit
	if limit == 0 {
		limit = api.DefaultEventsLimit
	}
	rs, err := b.Runs(ctx, p.After, limit+1)
	if err != nil {
		return api.RunsPage{}, err
	}
	page := api.RunsPage{Runs: []api.RunInfo{}, Next: p.After}
	if len(rs) > limit {
		rs, page.More = rs[:limit], true
	}
	for _, r := range rs {
		page.Runs = append(page.Runs, api.RunInfo{
			Run: r.Run, Org: r.Org, Project: r.Project, Flight: r.Flight, Generation: r.Generation,
			State: string(r.State), Runner: r.Runner, Placement: string(r.Placement), Fence: r.Fence,
			ExpiresAt: leasedExpiry(r), Accepted: r.Accepted, Activity: r.Activity, Asking: r.Asking,
			Stop: r.Stop, PR: r.PR, Reason: r.Reason, Position: r.QueuedSeq,
		})
		page.Next = r.QueuedSeq
	}
	return page, nil
}

func leasedExpiry(r runner.RunInfo) time.Time {
	if r.State != runner.StateLeased {
		return time.Time{}
	}
	return r.Expires
}

func (s *Server) runnerPermit(ctx context.Context, p *api.PermitParams) (api.PermitResult, error) {
	b, err := s.broker()
	if err != nil {
		return api.PermitResult{}, err
	}
	perm, err := b.Permit(ctx, p.Runner, p.Run, p.Fence, runner.PermitRequest{Tool: p.Tool, Kind: policy.Kind(p.Kind), Resource: p.Resource})
	if err != nil {
		return api.PermitResult{}, runnerError(err)
	}
	return api.PermitResult{Outcome: string(perm.Outcome), Rule: perm.Rule, Reason: perm.Reason}, nil
}
