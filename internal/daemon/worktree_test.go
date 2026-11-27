package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/api/client"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/runner"
)

// gitRun runs git for a test, with no user or system configuration.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@localhost", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@localhost"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// A run of a project whose Runway names a repository is leased its slot of the worktree pool with
// the lease, a checkout of the base on its own branch; once it finishes with nothing that has not
// landed, its slot is given back (LUCY-RUN-007).
func TestLeasedRunsGetTheirWorktree(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	src := filepath.Join(dir, "src")
	remote := filepath.Join(dir, "remote.git")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "--quiet", "--bare", "--initial-branch=main", remote)
	gitRun(t, src, "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(src, "AGENTS.md"), []byte("the project's instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, "add", "AGENTS.md")
	gitRun(t, src, "commit", "--quiet", "-m", "start")
	gitRun(t, src, "push", "--quiet", remote, "HEAD:main")

	if err := os.MkdirAll(o.ResourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, doc := range map[string]string{
		"airspace.yaml": "apiVersion: lucy/v1alpha1\nkind: Airspace\nmetadata: { name: acme, namespace: acme }\nspec: {}\n",
		"runway.yaml":   "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: { repository: acme/web }\n",
	} {
		if err := os.WriteFile(filepath.Join(o.ResourcesDir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	o.WorktreeRoot = filepath.Join(dir, "worktrees")
	o.WorktreeRemote = func(repo string) string {
		if repo == "acme/web" {
			return remote
		}
		return ""
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
	if err != nil || !l.Leased || l.Run.WorktreeError != "" {
		t.Fatalf("lease: %+v, %v", l, err)
	}
	wt := l.Run.Worktree
	if !strings.HasPrefix(wt, o.WorktreeRoot+string(filepath.Separator)) || !strings.HasPrefix(l.Run.Branch, "lucy/f1-1-") {
		t.Fatalf("worktree %q on %q", wt, l.Run.Branch)
	}
	if b, err := os.ReadFile(filepath.Join(wt, "AGENTS.md")); err != nil || string(b) != "the project's instructions\n" {
		t.Fatalf("the run's checkout: %q, %v", b, err)
	}
	if _, err := c.RunnerFinish(ctx, api.FinishParams{Runner: "acme-host-1", Run: "f1#1", Fence: l.Run.Fence, Outcome: "failed", Reason: "nothing to do"}); err != nil {
		t.Fatal(err)
	}
	evs, err := c.Events(ctx, api.EventsParams{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs.Events {
		if e.Stream == "worktree/acme/web" {
			types = append(types, e.Type)
		}
	}
	if got := strings.Join(types, ","); got != "worktree.added,worktree.leased,worktree.released" {
		t.Fatalf("the slot's events: %s", got)
	}
}
