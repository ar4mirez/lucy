package server

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/runner"
)

// oneShotExec is work that prints two lines, then opens pull request 5.
type oneShotExec struct{ polls int }

func (x *oneShotExec) Poll() runner.Update {
	x.polls++
	if x.polls == 1 {
		return runner.Update{Events: []runner.Event{{Kind: runner.KindOutput, Text: "one"}, {Kind: runner.KindOutput, Text: "two"}}}
	}
	return runner.Update{Result: &runner.Result{Outcome: runner.OutcomeDone, PR: 5}}
}
func (x *oneShotExec) Stop() {}

type oneShotExecutor struct{}

func (oneShotExecutor) Start(context.Context, runner.Lease) (runner.Execution, error) {
	return &oneShotExec{}, nil
}

func withBroker(o *Options) { o.Runs = runner.NewBroker(o.Store, nil) }

// A runner on the far side of the socket leases, streams and finishes a run, and every call is
// recorded as a command before it runs.
func TestRunnerOverTheSocket(t *testing.T) {
	f := start(t, os.Geteuid(), 0, withBroker)
	if err := f.srv.opts.Runs.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{ID: "local", Protocol: runner.Remote{Client: f.client}, Executor: oneShotExecutor{},
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := r.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.client.Runs(ctx, api.RunsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Runs) != 1 || page.More {
		t.Fatalf("runs %+v", page)
	}
	got := page.Runs[0]
	if got.State != "done" || got.PR != 5 || got.Accepted != 2 || got.Runner != "local" || got.Placement != "host" ||
		got.Fence == 0 || !got.ExpiresAt.IsZero() || got.Position == 0 {
		t.Fatalf("run %+v", got)
	}
	// lease, heartbeat (the two lines), finish: each a recorded command; the polls of
	// runner.queued and runs.list are reads and are not.
	evs, err := f.store.ReadStream(ctx, CommandStream, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var methods []string
	for _, e := range evs {
		c, err := eventstore.DecodeAs[CommandEvent](f.store.Registry(), e)
		if err != nil {
			t.Fatal(err)
		}
		methods = append(methods, c.Method)
	}
	if strings.Join(methods, " ") != "runner.lease runner.heartbeat runner.finish" {
		t.Fatalf("recorded commands %v", methods)
	}

	// Calls under a stale fence come back fenced, and the runner side sees ErrFenced.
	_, err = f.client.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "local", Run: "f1#1", Fence: got.Fence})
	wantCode(t, err, api.CodeFenced)
	if _, err := (runner.Remote{Client: f.client}).Heartbeat(ctx, "local", "f1#1", got.Fence, 0, nil, ""); !errors.Is(err, runner.ErrFenced) {
		t.Fatalf("remote heartbeat: %v", err)
	}
	if err := (runner.Remote{Client: f.client}).Finish(ctx, "local", "f1#1", got.Fence, runner.Result{Outcome: runner.OutcomeDone, PR: 5}); !errors.Is(err, runner.ErrFenced) {
		t.Fatalf("remote finish: %v", err)
	}
	if ok, err := f.client.RunnerLease(ctx, api.LeaseParams{Runner: "local"}); err != nil || ok.Leased {
		t.Fatalf("lease from an empty queue: %+v, %v", ok, err)
	}
}

func TestRunnerMethodRefusals(t *testing.T) {
	f := start(t, os.Geteuid(), 0, withBroker)
	_, err := f.client.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "local", Run: "nope", Fence: 1})
	if ae := wantCode(t, err, api.CodeInvalidParams); ae.Data.Field != "run" {
		t.Fatalf("unknown run: %+v", ae)
	}
	_, err = f.client.RunnerLease(ctx, api.LeaseParams{Runner: "Not Valid"})
	wantCode(t, err, api.CodeInvalidParams)
	_, err = f.client.RunnerLease(ctx, api.LeaseParams{Runner: "local", Placement: "moon"})
	wantCode(t, err, api.CodeInvalidParams)
	many := make([]api.RunEvent, 33)
	for i := range many {
		many[i] = api.RunEvent{Seq: int64(i + 1), Kind: "output"}
	}
	_, err = f.client.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "local", Run: "x", Fence: 1, Events: many})
	if ae := wantCode(t, err, api.CodeInvalidParams); ae.Data.Field != "events" {
		t.Fatalf("too many events: %+v", ae)
	}
	_, err = f.client.RunnerFinish(ctx, api.FinishParams{Runner: "local", Run: "x", Outcome: "done"})
	if ae := wantCode(t, err, api.CodeInvalidParams); ae.Data.Field != "fence" {
		t.Fatalf("no fence: %+v", ae)
	}

	// A daemon without a broker serves no runners.
	g := start(t, os.Geteuid(), 0, nil)
	_, err = g.client.RunnerQueued(ctx)
	wantCode(t, err, api.CodeUnavailable)
	_, err = g.client.Runs(ctx, api.RunsParams{})
	wantCode(t, err, api.CodeUnavailable)

	// A caller without the run scope may not lease.
	h := start(t, os.Geteuid(), 0, func(o *Options) { withBroker(o); o.Authorizer = denyAdmin{} })
	_, err = h.client.RunnerLease(ctx, api.LeaseParams{Runner: "local"})
	wantCode(t, err, api.CodeForbidden)
	_, err = h.client.RunnerQueued(ctx)
	wantCode(t, err, api.CodeForbidden)
	if _, err := h.client.Runs(ctx, api.RunsParams{}); err != nil {
		t.Fatal("runs.list is a read:", err)
	}
}
