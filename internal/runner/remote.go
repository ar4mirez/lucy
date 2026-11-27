package runner

import (
	"context"
	"errors"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
)

// Remote is the Protocol over the daemon's API: what `lucy runner` speaks, on this machine's
// socket now and, from inside an org container, on the socket berth mounts there
// (TODO(LUCY-RUN-001): the container mount and per-runner credentials, m1 steps 3 and 4).
type Remote struct {
	Client *client.Client
}

var _ Protocol = Remote{}

// remoteError maps the API's fenced error back to ErrFenced.
func remoteError(err error) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Code == api.CodeFenced {
		return errors.Join(ErrFenced, err)
	}
	return err
}

func seconds(d time.Duration) int { return int(d / time.Second) }

// Queued implements Protocol.
func (r Remote) Queued(ctx context.Context) (int, error) {
	res, err := r.Client.RunnerQueued(ctx)
	return res.Queued, err
}

// Lease implements Protocol.
func (r Remote) Lease(ctx context.Context, runner string, placement Placement, ttl time.Duration) (Lease, bool, error) {
	res, err := r.Client.RunnerLease(ctx, api.LeaseParams{Runner: runner, Placement: string(placement), TTLSeconds: seconds(ttl)})
	if err != nil || !res.Leased {
		return Lease{}, false, err
	}
	l := res.Run
	return Lease{
		Task: Task{Run: l.Run, Org: l.Org, Project: l.Project, Flight: l.Flight, Generation: l.Generation,
			Goal: l.Goal, Base: l.Base},
		Runner: runner, Fence: l.Fence, Expires: l.ExpiresAt,
	}, true, nil
}

// Heartbeat implements Protocol.
func (r Remote) Heartbeat(ctx context.Context, runner, run string, fence int64, ttl time.Duration, events []Event) (Beat, error) {
	p := api.HeartbeatParams{Runner: runner, Run: run, Fence: fence, TTLSeconds: seconds(ttl)}
	for _, e := range events {
		p.Events = append(p.Events, api.RunEvent{Seq: e.Seq, Kind: string(e.Kind), Text: e.Text})
	}
	res, err := r.Client.RunnerHeartbeat(ctx, p)
	if err != nil {
		return Beat{}, remoteError(err)
	}
	return Beat{Expires: res.ExpiresAt, Stop: res.Stop, Accepted: res.Accepted, Answer: res.Answer, Asking: res.Asking}, nil
}

// Finish implements Protocol.
func (r Remote) Finish(ctx context.Context, runner, run string, fence int64, res Result) error {
	_, err := r.Client.RunnerFinish(ctx, api.FinishParams{Runner: runner, Run: run, Fence: fence,
		Outcome: string(res.Outcome), PR: res.PR, Reason: capText(res.Reason)})
	return remoteError(err)
}
