package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
)

// The daemon hosts the Tower: it dispatches the Ready Flights it loads, and only those, starting
// each one's Pilot through the runner protocol; a question the Pilot streams reaches the owner
// through questions.list, and the owner's answer reaches the runner on its next heartbeat. A
// restart does not dispatch a flight again (LUCY-SPEC-012, LUCY-RUN-013).
func TestTheDaemonFliesReadyFlights(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	o.TowerPollInterval = 20 * time.Millisecond
	if err := os.MkdirAll(o.ResourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\nspec: {}\n",
		"runway.yaml":   "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: {}\n",
		"ready.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: fix-login, namespace: acme }\n" +
			"spec: { shape: ship, goal: fix the login page, runway: web, delivery: pull-request, autonomy: owner, acceptance: the login test passes }\n",
		"draft.yaml": "apiVersion: lucy/v1alpha1\nkind: Flight\nmetadata: { name: draft, namespace: acme }\nspec: { goal: someday }\n",
	} {
		if err := os.WriteFile(filepath.Join(o.ResourcesDir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
	if _, err := c.Answer(ctx, api.AnswerParams{Flight: "fix-login", Answer: "use the staging account"}); err != nil {
		t.Fatal(err)
	}
	beat, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: l.Run.Run, Fence: l.Run.Fence})
	if err != nil || beat.Answer != "use the staging account" || beat.Question != "q-1" {
		t.Fatalf("the answer on the heartbeat: %+v, %v", beat, err)
	}

	// Restarted, the daemon flies the same flight on: it is not dispatched again.
	if _, err := c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.wait(t); err != nil {
		t.Fatal(err)
	}
	_, c = start()
	time.Sleep(100 * time.Millisecond) // a few Tower steps
	if got := strings.Join(runs(), ","); got != "fix-login#1" {
		t.Fatalf("runs after a restart: %s", got)
	}
}
