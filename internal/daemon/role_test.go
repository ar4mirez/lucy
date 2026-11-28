package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/runner"
)

// An org's worker AgentRole blocks paths: a leased Pilot's prompted write of a blocked path is
// denied by the policy engine, and one of any other path is decided as before (LUCY-SPEC-015).
func TestTheWorkerRoleBlocksPaths(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	if err := os.MkdirAll(o.ResourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := "apiVersion: ai-sdlc.io/v1alpha1\nkind: AgentRole\nmetadata: { name: worker, namespace: acme }\n" +
		"spec: { role: ship worker, goal: land changes, tools: [read, write], constraints: { blockedPaths: [\"**/secrets/**\"] } }\n"
	if err := os.WriteFile(filepath.Join(o.ResourcesDir, "worker.yaml"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	reg, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	store, err := eventstore.Open(ctx, o.DBPath, eventstore.Options{Registry: reg, Projectors: Projectors(), SnapshotDir: o.SnapshotDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.NewBroker(store, nil).Submit(ctx, runner.Task{Run: "f1#1", Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	r := startDaemon(t, o)
	c := client.New(r.sock, "test", "0")
	defer c.Close()
	l, err := c.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-1", Org: "acme"})
	if err != nil || !l.Leased {
		t.Fatalf("lease: %+v, %v", l, err)
	}
	permit := func(path string) api.PermitResult {
		t.Helper()
		res, err := c.RunnerPermit(ctx, api.PermitParams{Runner: "acme-host-1", Run: l.Run.Run, Fence: l.Run.Fence, Tool: "Write", Kind: "write", Resource: path})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := permit("/runs/f1/app/secrets/key.txt"); res.Outcome != "denied" || res.Rule != "role.blocked-paths" {
		t.Fatalf("a blocked write: %+v", res)
	}
	if res := permit("/runs/f1/app/main.go"); res.Rule == "role.blocked-paths" {
		t.Fatalf("an unblocked write was blocked: %+v", res)
	}
}
