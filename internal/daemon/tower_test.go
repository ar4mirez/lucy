package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
)

// The daemon hosts the Tower: started with --dispatch, it dispatches the Ready Flights it loads,
// and only those, starting each one's Pilot through the runner protocol; a question the Pilot streams reaches the owner
// through questions.list, and the owner's answer reaches the runner on its next heartbeat. A
// restart does not dispatch a flight again (LUCY-SPEC-012, LUCY-RUN-013).
func TestTheDaemonFliesReadyFlights(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	o.TowerPollInterval = 20 * time.Millisecond
	o.Dispatch = true
	writeFlights(t, o, "spec: {}")
	ctx := context.Background()
	start := func() (*running, *client.Client) {
		r := startDaemon(t, o)
		c := client.New(r.sock, "test", "0")
		t.Cleanup(func() { c.Close() })
		return r, c
	}
	r, c := start()
	runs := func() []string {
		page, err := c.Runs(ctx, api.RunsParams{})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, ri := range page.Runs {
			ids = append(ids, ri.Run)
		}
		return ids
	}
	eventually := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not happen in 10 s", what)
			}
		}
	}
	eventually("the ready flight's Pilot run was queued", func() bool { return strings.Join(runs(), ",") == "fix-login#1" })
	if d := dispatched(t, c, "acme", "fix-login"); d.Status != "True" || d.Reason != ReasonDispatched {
		t.Fatalf("the Dispatched condition: %+v", d)
	}

	l, err := c.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-1", Org: "acme"})
	if err != nil || !l.Leased || l.Run.Run != "fix-login#1" {
		t.Fatalf("lease: %+v, %v", l, err)
	}
	if _, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: l.Run.Run, Fence: l.Run.Fence,
		Events: []api.RunEvent{{Seq: 1, Kind: "asking", Text: "which account?", Question: "q-1"}}}); err != nil {
		t.Fatal(err)
	}
	eventually("the Pilot's question reached lucy questions", func() bool {
		q, err := c.Questions(ctx)
		return err == nil && len(q.Questions) == 1 && q.Questions[0].Flight == "fix-login" && q.Questions[0].Question == "which account?"
	})
	// The question is an open decision of the catalog, and only the owner's answer closes it.
	catalog := func(all bool) []api.CatalogDecision {
		var l api.CatalogList
		if err := c.Call(ctx, api.MethodDecList, api.CatalogParams{All: all}, &l); err != nil {
			t.Fatal(err)
		}
		return l.Decisions
	}
	if open := catalog(false); len(open) != 1 || open[0].Kind != "question" || open[0].Lifecycle != "open" ||
		open[0].Scope != "flight:acme/fix-login" || open[0].Body != "which account?" {
		t.Fatalf("open decisions %+v", open)
	}
	var ae *api.Error
	if err := c.Call(ctx, api.MethodApprove, api.ApproveParams{Flight: "fix-login"}, &api.ApproveResult{}); !errors.As(err, &ae) || ae.Code != api.CodeInvalidParams {
		t.Fatalf("approving a flight that holds for no merge: %v", err)
	}
	if _, err := c.Answer(ctx, api.AnswerParams{Flight: "fix-login", Answer: "use the staging account"}); err != nil {
		t.Fatal(err)
	}
	if open := catalog(false); len(open) != 0 {
		t.Fatalf("open after the answer: %+v", open)
	}
	if all := catalog(true); len(all) != 1 || all[0].Lifecycle != "answered" || all[0].AnsweredBy == "" || all[0].AnsweredBy == "daemon" {
		t.Fatalf("the answered decision %+v", all)
	}
	beat, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: l.Run.Run, Fence: l.Run.Fence})
	if err != nil || beat.Answer != "use the staging account" || beat.Question != "q-1" {
		t.Fatalf("the answer on the heartbeat: %+v, %v", beat, err)
	}

	// Restarted without --dispatch, the daemon flies the same flight on from its log: it is not
	// dispatched again, and its status says it is in flight.
	if _, err := c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Fatal(err)
	}
	o.Dispatch = false
	_, c = start()
	time.Sleep(100 * time.Millisecond) // a few Tower steps
	if got := strings.Join(runs(), ","); got != "fix-login#1" {
		t.Fatalf("runs after a restart: %s", got)
	}
	if d := dispatched(t, c, "acme", "fix-login"); d.Status != "True" || d.Reason != ReasonInFlight {
		t.Fatalf("the Dispatched condition after a restart: %+v", d)
	}
}

// writeFlights writes an airspace, its runway with runwaySpec, a Ready Flight fix-login and a
// draft Flight into o's config directory.
func writeFlights(t *testing.T, o Options, runwaySpec string) {
	t.Helper()
	if err := os.MkdirAll(o.ResourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\nspec: {}\n",
		"runway.yaml":   "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\n" + runwaySpec + "\n",
		"ready.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: fix-login, namespace: acme }\n" +
			"spec: { shape: ship, goal: fix the login page, runway: web, delivery: pull-request, autonomy: owner, acceptance: the login test passes }\n",
		"draft.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: draft, namespace: acme }\nspec: { goal: someday }\n",
	} {
		if err := os.WriteFile(filepath.Join(o.ResourcesDir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// dispatched returns the recorded Dispatched condition of the Flight ns/name.
func dispatched(t *testing.T, c *client.Client, ns, name string) api.ConditionInfo {
	t.Helper()
	page, err := c.Resources(context.Background(), api.ResourcesParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Resources {
		if r.Kind == "Flight" && r.Namespace == ns && r.Name == name {
			for _, cond := range r.Conditions {
				if cond.Type == ConditionDispatched {
					return cond
				}
			}
			t.Fatalf("%s/%s has no Dispatched condition: %+v", ns, name, r.Conditions)
		}
	}
	t.Fatalf("no Flight %s/%s in %+v", ns, name, page.Resources)
	return api.ConditionInfo{}
}

// queued returns the runs the daemon holds, after a few Tower steps.
func queued(t *testing.T, c *client.Client) int {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	page, err := c.Runs(context.Background(), api.RunsParams{})
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Runs)
}

func startClient(t *testing.T, o Options) *client.Client {
	t.Helper()
	r := startDaemon(t, o)
	c := client.New(r.sock, "test", "0")
	t.Cleanup(func() { c.Close() })
	return c
}

// Without --dispatch the daemon dispatches nothing, and each Ready Flight's status says so; a
// draft Flight carries no Dispatched condition.
func TestTheDaemonDispatchesNothingByDefault(t *testing.T) {
	o := opts(shortTempDir(t))
	o.TowerPollInterval = 20 * time.Millisecond
	writeFlights(t, o, "spec: {}")
	c := startClient(t, o)
	if n := queued(t, c); n != 0 {
		t.Fatalf("%d runs queued without --dispatch", n)
	}
	if d := dispatched(t, c, "acme", "fix-login"); d.Status != "False" || d.Reason != ReasonDispatchDisabled {
		t.Fatalf("the Dispatched condition: %+v", d)
	}
	page, err := c.Resources(context.Background(), api.ResourcesParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Resources {
		for _, cond := range r.Conditions {
			if r.Name == "draft" && cond.Type == ConditionDispatched {
				t.Fatalf("a draft Flight carries %+v", cond)
			}
		}
	}
}

// With --dispatch, a Flight whose Runway names a repository on a real remote is refused at the
// takeover gate and nothing is queued; one whose remote is a local fixture in the temp directory
// is dispatched.
func TestTheTakeoverGate(t *testing.T) {
	o := opts(shortTempDir(t))
	o.TowerPollInterval = 20 * time.Millisecond
	o.Dispatch = true
	o.WorktreeRoot = filepath.Join(shortTempDir(t), "worktrees")
	writeFlights(t, o, "spec: { repository: acme/web }")
	c := startClient(t, o)
	if n := queued(t, c); n != 0 {
		t.Fatalf("%d runs queued for a real repository", n)
	}
	if d := dispatched(t, c, "acme", "fix-login"); d.Status != "False" || d.Reason != ReasonTakeoverGate ||
		!strings.Contains(d.Message, "#36") || !strings.Contains(d.Message, "https://github.com/acme/web.git") {
		t.Fatalf("the Dispatched condition: %+v", d)
	}

	o2 := opts(shortTempDir(t))
	o2.TowerPollInterval = 20 * time.Millisecond
	o2.Dispatch = true
	o2.WorktreeRoot = filepath.Join(shortTempDir(t), "worktrees")
	fixture := filepath.Join(shortTempDir(t), "web.git")
	if err := os.MkdirAll(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	o2.WorktreeRemote = func(string) string { return "file://" + fixture }
	writeFlights(t, o2, "spec: { repository: acme/web }")
	c2 := startClient(t, o2)
	if n := queued(t, c2); n != 1 {
		t.Fatalf("%d runs queued for a local fixture, want 1", n)
	}
	if d := dispatched(t, c2, "acme", "fix-login"); d.Status != "True" || d.Reason != ReasonDispatched {
		t.Fatalf("the Dispatched condition: %+v", d)
	}
}

func TestTakeoverGateRemotes(t *testing.T) {
	tmp := t.TempDir()
	fixture := filepath.Join(tmp, "web.git")
	if err := os.MkdirAll(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(tmp, "escape")
	if err := os.Symlink("/", escape); err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{"file://" + fixture, fixture} {
		if err := takeoverGate(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://github.com/acme/web.git", "ssh://git@github.com/acme/web.git", "git@github.com:acme/web.git",
		"file://" + escape, escape, "file:///", "/", "relative/web.git", "file://host" + fixture, filepath.Join(tmp, "missing")} {
		if err := takeoverGate(bad); !errors.Is(err, ErrTakeoverGate) {
			t.Errorf("%s: %v, want ErrTakeoverGate", bad, err)
		}
	}
}

// A Ready Flight whose name a flight of another org holds is not dispatched: its status says the
// name is in use, and nothing more is queued.
func TestAFlightNameInUseIsNotDispatched(t *testing.T) {
	o := opts(shortTempDir(t))
	o.TowerPollInterval = 20 * time.Millisecond
	o.Dispatch = true
	writeFlights(t, o, "spec: {}")
	r := startDaemon(t, o)
	c := client.New(r.sock, "test", "0")
	if n := queued(t, c); n != 1 {
		t.Fatalf("%d runs queued, want 1", n)
	}
	if _, err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := r.wait(t); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: beta, namespace: beta }\nspec: {}\n",
		"runway.yaml":   "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: beta }\nspec: {}\n",
		"ready.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: fix-login, namespace: beta }\n" +
			"spec: { shape: ship, goal: fix the login page, runway: web, delivery: pull-request, autonomy: owner, acceptance: the login test passes }\n",
	} {
		if err := os.WriteFile(filepath.Join(o.ResourcesDir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c = startClient(t, o)
	if n := queued(t, c); n != 1 {
		t.Fatalf("%d runs queued, want the first flight's only", n)
	}
	if d := dispatched(t, c, "beta", "fix-login"); d.Status != "False" || d.Reason != ReasonNameInUse || !strings.Contains(d.Message, "acme") {
		t.Fatalf("the Dispatched condition: %+v", d)
	}
}

// The takeover gate holds where every run gets its checkout: a flight dispatched while its Runway
// named no repository, whose Runway then names a real remote, is leased with the gate as its
// worktree error, after a restart without --dispatch, and no slot is reserved.
func TestTheTakeoverGateHoldsAtLease(t *testing.T) {
	o := opts(shortTempDir(t))
	o.TowerPollInterval = 20 * time.Millisecond
	o.Dispatch = true
	o.WorktreeRoot = filepath.Join(shortTempDir(t), "worktrees")
	writeFlights(t, o, "spec: {}")
	r := startDaemon(t, o)
	c := client.New(r.sock, "test", "0")
	if n := queued(t, c); n != 1 {
		t.Fatalf("%d runs queued, want 1", n)
	}
	if _, err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := r.wait(t); err != nil {
		t.Fatal(err)
	}
	o.Dispatch = false
	writeFlights(t, o, "spec: { repository: acme/web }")
	c = startClient(t, o)
	if d := dispatched(t, c, "acme", "fix-login"); d.Status != "False" || d.Reason != ReasonTakeoverGate {
		t.Fatalf("the Dispatched condition of a flight in the log on a real remote: %+v", d)
	}
	ctx := context.Background()
	l, err := c.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-1", Org: "acme"})
	if err != nil || !l.Leased {
		t.Fatalf("lease: %+v, %v", l, err)
	}
	if !strings.Contains(l.Run.WorktreeError, "takeover gate") || l.Run.Worktree != "" {
		t.Fatalf("the lease's worktree: %q, error %q", l.Run.Worktree, l.Run.WorktreeError)
	}
	slots, err := c.Worktrees(ctx, "")
	if err != nil || len(slots.Slots) != 0 {
		t.Fatalf("slots: %+v, %v", slots, err)
	}
}

// A Ready Flight whose name a finished flight of its org and runway holds is not dispatched, and
// its status says so rather than that the Tower flies it.
func TestAFinishedFlightNameIsNotReused(t *testing.T) {
	o := opts(shortTempDir(t))
	o.TowerPollInterval = 20 * time.Millisecond
	o.Dispatch = true
	writeFlights(t, o, "spec: {}")
	r := startDaemon(t, o)
	c := client.New(r.sock, "test", "0")
	ctx := context.Background()
	var l api.LeaseResult
	for deadline := time.Now().Add(10 * time.Second); !l.Leased; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the flight's run was never leased")
		}
		var err error
		if l, err = c.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-1", Org: "acme"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.RunnerFinish(ctx, api.FinishParams{Runner: "acme-host-1", Run: l.Run.Run, Fence: l.Run.Fence, Outcome: "failed", Reason: "gave up"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // a few Tower steps: the flight is grounded
	if _, err := c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := r.wait(t); err != nil {
		t.Fatal(err)
	}
	c = startClient(t, o)
	if n := queued(t, c); n != 1 {
		t.Fatalf("%d runs, want the first flight's only", n)
	}
	if d := dispatched(t, c, "acme", "fix-login"); d.Status != "False" || d.Reason != ReasonFinished || !strings.Contains(d.Message, "grounded") {
		t.Fatalf("the Dispatched condition: %+v", d)
	}
}
