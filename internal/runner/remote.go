package runner

import (
	"context"
	"errors"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/policy"
)

// Remote is the Protocol over the daemon's API: what `lucy runner` speaks, on this machine's
// socket now and, from inside an org container, on the socket berth mounts there
// (TODO(LUCY-RUN-001): the container mount and per-runner credentials, m1 steps 3 and 4).
type Remote struct {
	Client *client.Client
}

var _ Protocol = Remote{}

// remoteError maps the API's fenced and sandbox_busy errors back to ErrFenced and ErrSandboxBusy.
func remoteError(err error) error {
	var ae *api.Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case api.CodeFenced:
			return errors.Join(ErrFenced, err)
		case api.CodeSandboxBusy:
			return errors.Join(ErrSandboxBusy, err)
		}
	}
	return err
}

func seconds(d time.Duration) int { return int(d / time.Second) }

// Queued implements Protocol.
func (r Remote) Queued(ctx context.Context, placement Placement) (int, error) {
	res, err := r.Client.RunnerQueued(ctx, api.QueuedParams{Placement: string(placement)})
	return res.Queued, err
}

// Lease implements Protocol.
func (r Remote) Lease(ctx context.Context, runner string, placement Placement, ttl time.Duration) (Lease, bool, error) {
	res, err := r.Client.RunnerLease(ctx, api.LeaseParams{Runner: runner, Placement: string(placement), TTLSeconds: seconds(ttl)})
	if err != nil || !res.Leased {
		return Lease{}, false, remoteError(err)
	}
	l := res.Run
	return Lease{
		Task: Task{Run: l.Run, Org: l.Org, Project: l.Project, Flight: l.Flight, Generation: l.Generation,
			Goal: l.Goal, Base: l.Base},
		Runner: runner, Fence: l.Fence, Expires: l.ExpiresAt, Capability: l.Capability,
	}, true, nil
}

// Heartbeat implements Protocol.
func (r Remote) Heartbeat(ctx context.Context, runner, run string, fence int64, ttl time.Duration, events []Event, acked string) (Beat, error) {
	p := api.HeartbeatParams{Runner: runner, Run: run, Fence: fence, TTLSeconds: seconds(ttl), Acked: acked}
	for _, e := range events {
		p.Events = append(p.Events, api.RunEvent{Seq: e.Seq, Kind: string(e.Kind), Text: e.Text, Question: e.Question, Hook: e.Hook})
	}
	res, err := r.Client.RunnerHeartbeat(ctx, p)
	if err != nil {
		return Beat{}, remoteError(err)
	}
	return Beat{Expires: res.ExpiresAt, Stop: res.Stop, Accepted: res.Accepted, Answer: res.Answer, Question: res.Question, Asking: res.Asking,
		Capability: res.Capability}, nil
}

// Finish implements Protocol.
func (r Remote) Finish(ctx context.Context, runner, run string, fence int64, res Result) error {
	_, err := r.Client.RunnerFinish(ctx, api.FinishParams{Runner: runner, Run: run, Fence: fence,
		Outcome: string(res.Outcome), PR: res.PR, Reason: capText(res.Reason)})
	return remoteError(err)
}

// Permit implements Permitter.
func (r Remote) Permit(ctx context.Context, runner, run string, fence int64, p PermitRequest) (Permission, error) {
	res, err := r.Client.RunnerPermit(ctx, api.PermitParams{Runner: runner, Run: run, Fence: fence, Tool: p.Tool,
		Kind: string(p.Kind), Resource: p.Resource})
	if err != nil {
		return Permission{}, remoteError(err)
	}
	return Permission{Outcome: policy.Outcome(res.Outcome), Rule: res.Rule, Reason: res.Reason}, nil
}

var (
	_ Permitter = Remote{}
	_ Permitter = (*Broker)(nil)
)
