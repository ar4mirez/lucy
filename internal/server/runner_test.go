package server

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
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
	r, err := runner.New(runner.Config{ID: "local", Org: "acme", Protocol: runner.Remote{Client: f.client}, Executor: oneShotExecutor{},
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
	if ok, err := f.client.RunnerLease(ctx, api.LeaseParams{Runner: "local", Org: "acme"}); err != nil || ok.Leased {
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
	// A runner on the host leases for one org, which it names.
	_, err = f.client.RunnerLease(ctx, api.LeaseParams{Runner: "local"})
	if ae := wantCode(t, err, api.CodeRunnerOrg); ae.Data.Field != "org" {
		t.Fatalf("a host lease without an org: %+v", ae)
	}
	if _, _, err := (runner.Remote{Client: f.client}).Lease(ctx, "local", runner.PlacementHost, "", 0); !errors.Is(err, runner.ErrRunnerOrg) {
		t.Fatalf("a remote host lease without an org: %v, want ErrRunnerOrg", err)
	}
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
	_, err = g.client.RunnerQueued(ctx, api.QueuedParams{})
	wantCode(t, err, api.CodeUnavailable)
	_, err = g.client.Runs(ctx, api.RunsParams{})
	wantCode(t, err, api.CodeUnavailable)

	// A caller without the run scope may not lease.
	h := start(t, os.Geteuid(), 0, func(o *Options) { withBroker(o); o.Authorizer = denyAdmin{} })
	_, err = h.client.RunnerLease(ctx, api.LeaseParams{Runner: "local"})
	wantCode(t, err, api.CodeForbidden)
	_, err = h.client.RunnerQueued(ctx, api.QueuedParams{})
	wantCode(t, err, api.CodeForbidden)
	if _, err := h.client.Runs(ctx, api.RunsParams{}); err != nil {
		t.Fatal("runs.list is a read:", err)
	}
}

// Host placement serves one org over the API: another org's host lease is refused with host_org,
// and runner.host_org, an admin command, changes it only while no host run is live.
func TestHostOrgOverTheSocket(t *testing.T) {
	f := start(t, os.Geteuid(), 0, withBroker)
	for _, task := range []runner.Task{{Run: "a#1", Org: "acme", Project: "web", Flight: "a", Generation: 1, Goal: "g"},
		{Run: "b#1", Org: "beta", Project: "web", Flight: "b", Generation: 1, Goal: "g"}} {
		if err := f.srv.opts.Runs.Submit(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	l, err := f.client.RunnerLease(ctx, api.LeaseParams{Runner: "a", Org: "acme"})
	if err != nil || !l.Leased {
		t.Fatalf("acme's lease %+v, %v", l, err)
	}
	_, err = f.client.RunnerLease(ctx, api.LeaseParams{Runner: "b", Org: "beta"})
	if ae := wantCode(t, err, api.CodeHostOrg); ae.Data.Field != "org" || !strings.Contains(ae.Data.Help, "container placement") {
		t.Fatalf("beta's host lease: %+v", ae)
	}
	if _, _, err := (runner.Remote{Client: f.client}).Lease(ctx, "b", runner.PlacementHost, "beta", 0); !errors.Is(err, runner.ErrHostOrg) {
		t.Fatalf("a remote host lease for beta: %v, want ErrHostOrg", err)
	}
	_, err = f.client.RunnerHostOrg(ctx, api.HostOrgParams{Org: "beta"})
	wantCode(t, err, api.CodeHostOrg)
	if _, err := f.client.RunnerFinish(ctx, api.FinishParams{Runner: "a", Run: "a#1", Fence: l.Run.Fence, Outcome: "done"}); err != nil {
		t.Fatal(err)
	}
	if res, err := f.client.RunnerHostOrg(ctx, api.HostOrgParams{Org: "beta"}); err != nil || res.Org != "beta" {
		t.Fatalf("switch to beta: %+v, %v", res, err)
	}
	if l, err := f.client.RunnerLease(ctx, api.LeaseParams{Runner: "b", Org: "beta"}); err != nil || !l.Leased || l.Run.Run != "b#1" {
		t.Fatalf("beta's lease after the switch: %+v, %v", l, err)
	}
	h := start(t, os.Geteuid(), 0, func(o *Options) { withBroker(o); o.Authorizer = denyAdmin{} })
	_, err = h.client.RunnerHostOrg(ctx, api.HostOrgParams{Org: "beta"})
	wantCode(t, err, api.CodeForbidden)
}

// A runner asks the policy engine about its Pilot's prompted tool through runner.permit: the
// Pilot is decided as a worker of the run's org, and a stale fence gets nothing.
func TestRunnerPermitOverTheSocket(t *testing.T) {
	f := start(t, os.Geteuid(), 0, withBroker)
	if err := f.srv.opts.Runs.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	l, err := f.client.RunnerLease(ctx, api.LeaseParams{Runner: "local", Org: "acme"})
	if err != nil || !l.Leased {
		t.Fatalf("lease %+v, %v", l, err)
	}
	perm, err := (runner.Remote{Client: f.client}).Permit(ctx, "local", "f1#1", l.Run.Fence,
		runner.PermitRequest{Tool: "Edit", Kind: policy.KindWrite, Resource: "/scratch/run-1/a.go"})
	if err != nil || perm.Outcome != policy.Cleared || perm.Rule != "write.worker-only" {
		t.Fatalf("permit %+v, %v", perm, err)
	}
	_, err = (runner.Remote{Client: f.client}).Permit(ctx, "local", "f1#1", l.Run.Fence+1, runner.PermitRequest{Tool: "Edit", Kind: policy.KindWrite})
	if !errors.Is(err, runner.ErrFenced) {
		t.Fatalf("stale permit: %v", err)
	}
	_, err = f.client.RunnerPermit(ctx, api.PermitParams{Runner: "local", Run: "f1#1", Fence: l.Run.Fence, Tool: "Bash", Kind: "delete"})
	wantCode(t, err, api.CodeInvalidParams)
}

// A Pilot's shell command or fetch is decided on as written, but neither the stored record, the
// api.command event nor policy.decisions holds the secrets in it.
func TestRunnerPermitRedactsExecAndFetch(t *testing.T) {
	f := start(t, os.Geteuid(), 0, withBroker)
	if err := f.srv.opts.Runs.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	l, err := f.client.RunnerLease(ctx, api.LeaseParams{Runner: "local", Org: "acme"})
	if err != nil || !l.Leased {
		t.Fatalf("lease %+v, %v", l, err)
	}
	secrets := []string{"abc.bearer.value", "hunter2pw", "tok123value", "plainvalue1", "ghp_0123456789abcdefABCDEF0123", "AKIAIOSFODNN7EXAMPLE"}
	reqs := []runner.PermitRequest{
		{Tool: "Bash", Kind: policy.KindExec, Resource: `curl -H 'Authorization: Bearer abc.bearer.value' https://alice:hunter2pw@example.com`},
		{Tool: "Bash", Kind: policy.KindExec, Resource: `export GITHUB_TOKEN=plainvalue1 && echo ghp_0123456789abcdefABCDEF0123 AKIAIOSFODNN7EXAMPLE`},
		{Tool: "WebFetch", Kind: policy.KindFetch, Resource: "https://example.com/x?token=tok123value"},
	}
	for _, r := range reqs {
		perm, err := (runner.Remote{Client: f.client}).Permit(ctx, "local", "f1#1", l.Run.Fence, r)
		if err != nil || perm.Outcome != policy.Denied || perm.Rule != "default.deny" {
			t.Fatalf("permit %+v, %v", perm, err)
		}
	}
	page, err := f.client.Decisions(ctx, api.DecisionsParams{})
	if err != nil || len(page.Decisions) != len(reqs) {
		t.Fatalf("decisions %+v, %v", page, err)
	}
	for i, d := range page.Decisions {
		if d.Resource != policy.RecordedResource(reqs[i].Kind, reqs[i].Resource) || !strings.Contains(d.Resource, policy.Redacted) {
			t.Errorf("decision %d resource %q", i, d.Resource)
		}
	}
	evs, err := f.store.ReadAfter(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		for _, s := range secrets {
			if strings.Contains(string(e.Payload), s) {
				t.Errorf("%s event holds %q: %s", e.Type, s, e.Payload)
			}
		}
	}
}

// A runner placed in a container, on the far side of the socket, is told the org's container is
// busy while it is held, takes it as lease again later, and leases once the hold is released.
func TestContainerRunnerWaitsOutAHeldContainer(t *testing.T) {
	f := start(t, os.Geteuid(), 0, withBroker)
	b := f.srv.opts.Runs
	if err := b.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if held, err := b.HoldContainer(ctx, "acme", nil, time.Minute); err != nil || !held {
		t.Fatalf("hold: %v, %v", held, err)
	}
	remote := runner.Remote{Client: f.client}
	if _, _, err := remote.Lease(ctx, "box", runner.PlacementContainer, "", time.Minute); !errors.Is(err, runner.ErrSandboxBusy) {
		t.Fatalf("a container lease while acme is held: %v, want ErrSandboxBusy", err)
	}
	r, err := runner.New(runner.Config{ID: "box", Placement: runner.PlacementContainer, Protocol: remote, Executor: oneShotExecutor{},
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Step(ctx); err != nil {
		t.Fatalf("a step while acme is held: %v", err)
	}
	if ri, err := b.Run(ctx, "f1#1"); err != nil || ri.State != runner.StateQueued {
		t.Fatalf("run while held: %+v, %v", ri, err)
	}
	if err := b.ReleaseContainer(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := r.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if ri, err := b.Run(ctx, "f1#1"); err != nil || ri.State != runner.StateLeased || ri.Runner != "box" {
		t.Fatalf("run once released: %+v, %v", ri, err)
	}
}

// While an org's container is held, runner.queued counts none of its runs for a runner placed in a
// container, so that runner's polling never leases and adds nothing to the log: no api.command,
// no audit record. A host runner, and an omitted placement, are still counted them.
func TestHeldContainerRunnerPollsQuietly(t *testing.T) {
	f := start(t, os.Geteuid(), 0, withBroker)
	b := f.srv.opts.Runs
	if err := b.Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if held, err := b.HoldContainer(ctx, "acme", nil, time.Minute); err != nil || !held {
		t.Fatalf("hold: %v, %v", held, err)
	}
	for _, c := range []struct {
		placement string
		want      int
	}{{"", 1}, {"host", 1}, {"container", 0}} {
		if q, err := f.client.RunnerQueued(ctx, api.QueuedParams{Placement: c.placement}); err != nil || q.Queued != c.want {
			t.Errorf("runner.queued placement %q = %+v, %v; want %d", c.placement, q, err, c.want)
		}
	}
	before, err := f.store.ReadAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{ID: "box", Placement: runner.PlacementContainer, Protocol: runner.Remote{Client: f.client},
		Executor: oneShotExecutor{}, Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := r.Step(ctx); err != nil {
			t.Fatalf("a step while acme is held: %v", err)
		}
	}
	after, err := f.store.ReadAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after[len(before):] {
		t.Errorf("polling while held recorded %s", e.Type)
	}
	if ri, err := b.Run(ctx, "f1#1"); err != nil || ri.State != runner.StateQueued {
		t.Fatalf("run while held: %+v, %v", ri, err)
	}
}
