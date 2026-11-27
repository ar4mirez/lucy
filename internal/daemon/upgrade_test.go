package daemon

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
)

// restoreFixture builds a store from a SQL dump in testdata/upgrade at path, 0600 in a 0700
// directory, as the release that wrote it left it.
func restoreFixture(t *testing.T, name, path string) {
	t.Helper()
	dump, err := os.ReadFile(filepath.Join("testdata", "upgrade", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An in-place upgrade from v0.6.0: this daemon starts on the store a v0.6.0 daemon left with a
// run leased, and the runner that kept working, still speaking v0.6.0's heartbeat and finish,
// carries on under its fence and finishes the run; the audit chain the old daemon wrote verifies
// with the new one's records on top. A v0.6.0 runner names no org, so it can lease nothing new
// until it is restarted (runner_org); a runner that names its org leases the queued run
// (LUCY-OPS-005).
func TestUpgradeFromV060(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	restoreFixture(t, "v0.6.0.sql", o.DBPath)
	r := startDaemon(t, o)
	c := client.New(r.sock, "lucy-runner", "0.6.0")
	defer c.Close()
	ctx := context.Background()

	// The fixture's lease ran out long ago, while no daemon ran: it was moved by the outage.
	beat, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: "f1#1", Fence: 4,
		Events: []api.RunEvent{{Seq: 3, Kind: "activity", Text: "opened the pull request"}}})
	if err != nil || beat.Accepted != 3 || !beat.ExpiresAt.After(time.Now()) {
		t.Fatalf("the v0.6.0 runner's heartbeat after the upgrade: %+v, %v", beat, err)
	}
	if _, err := c.RunnerFinish(ctx, api.FinishParams{Runner: "acme-host-1", Run: "f1#1", Fence: 4, Outcome: "done", PR: 7}); err != nil {
		t.Fatalf("the v0.6.0 runner's finish: %v", err)
	}

	var ae *api.Error
	if _, err := c.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-1"}); !errors.As(err, &ae) || ae.Code != api.CodeRunnerOrg {
		t.Fatalf("a lease without an org, as v0.6.0 sends it: %v", err)
	}
	l, err := c.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-2", Org: "acme"})
	if err != nil || !l.Leased || l.Run.Run != "f2#1" {
		t.Fatalf("a restarted runner's lease: %+v, %v", l, err)
	}

	page, err := c.Runs(ctx, api.RunsParams{})
	if err != nil || len(page.Runs) != 2 {
		t.Fatalf("runs: %+v, %v", page, err)
	}
	if f1 := page.Runs[0]; f1.Run != "f1#1" || f1.State != "done" || f1.PR != 7 || f1.Accepted != 3 {
		t.Fatalf("the run held across the upgrade: %+v", f1)
	}
	rep, err := c.VerifyAudit(ctx)
	if err != nil || !rep.Verified || rep.Entries < 8 {
		t.Fatalf("the audit chain across the upgrade: %+v, %v", rep, err)
	}
	evs, err := c.Events(ctx, api.EventsParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs.Events {
		if e.Stream == "run/acme/f1#1" {
			types = append(types, e.Type)
		}
	}
	want := []string{"run.queued", "run.leased", "run.event", "run.event", "run.renewed", "run.resumed", "run.event", "run.renewed", "run.finished"}
	if len(types) != len(want) {
		t.Fatalf("f1#1's events %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("f1#1's events %v, want %v", types, want)
		}
	}
}
