package server

import (
	"context"
	"errors"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
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
	case errors.Is(err, runner.ErrInvalid), errors.Is(err, runner.ErrExists):
		return api.NewError(api.CodeInvalidParams, "check the runner id, placement, event kinds and outcome")
	}
	return err
}

func (s *Server) runnerQueued(ctx context.Context, _ *api.NoParams) (api.QueuedResult, error) {
	b, err := s.broker()
	if err != nil {
		return api.QueuedResult{}, err
	}
	n, err := b.Queued(ctx)
	return api.QueuedResult{Queued: n}, err
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
	return api.LeaseResult{Leased: true, Run: api.RunLease{
		Run: l.Run, Org: l.Org, Project: l.Project, Flight: l.Flight, Generation: l.Generation, Goal: l.Goal,
		Base: l.Base, Fence: l.Fence, ExpiresAt: l.Expires,
	}}, nil
}

func (s *Server) runnerHeartbeat(ctx context.Context, p *api.HeartbeatParams) (api.HeartbeatResult, error) {
	b, err := s.broker()
	if err != nil {
		return api.HeartbeatResult{}, err
	}
	evs := make([]runner.Event, 0, len(p.Events))
	for _, e := range p.Events {
		evs = append(evs, runner.Event{Seq: e.Seq, Kind: runner.EventKind(e.Kind), Text: e.Text})
	}
	beat, err := b.Heartbeat(ctx, p.Runner, p.Run, p.Fence, time.Duration(p.TTLSeconds)*time.Second, evs)
	if err != nil {
		return api.HeartbeatResult{}, runnerError(err)
	}
	return api.HeartbeatResult{ExpiresAt: beat.Expires, Stop: beat.Stop, Accepted: beat.Accepted}, nil
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
