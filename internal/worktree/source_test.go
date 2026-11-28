package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/spec"
)

func runwayRes(org, project, repo string) spec.Resource {
	sp := map[string]any{}
	if repo != "" {
		sp["repository"] = repo
	}
	return spec.Resource{APIVersion: spec.LucyAPIVersion, Kind: "Runway",
		Metadata: spec.Metadata{Name: project, Namespace: org}, Spec: sp}
}

// The Source places a run by its project's Runway: a project naming a repository gets a slot of
// it, at the Runway's default branch, reserved at once and prepared in the background; one naming
// none gets no checkout; and a finished run's slot is given back, in the background, if its work
// landed, else quarantined.
func TestSourcePlacesRunsByTheirRunway(t *testing.T) {
	f := open(t)
	src := &Source{Pool: f.pool, Remote: func(repo string) string {
		if repo == "acme/web" {
			return f.repo.Remote
		}
		return "file:///nowhere/" + repo
	}}
	src.SetRunways([]spec.Resource{
		runwayRes("acme", "web", "acme/web"),
		runwayRes("acme", "docs", ""),
		runwayRes("acme", "bad", "not-a-repository"),
		{APIVersion: spec.LucyAPIVersion, Kind: "Airspace", Metadata: spec.Metadata{Name: "acme", Namespace: "acme"}},
	})

	l, ok, err := src.Acquire(ctx, "acme", "web", "f1#1", "")
	if err != nil || !ok || l.Slot != 1 || l.BaseSHA != "" {
		t.Fatalf("acquire: %+v, %v, %v", l, ok, err)
	}
	src.Wait()
	if st, why, err := src.State(ctx, "acme", "web", "f1#1"); st != PrepReady || why != "" || err != nil {
		t.Fatalf("the prepared slot: %q, %q, %v", st, why, err)
	}
	if s := f.slot(1); s.BaseSHA != f.head(f.work) || f.head(l.Path) != s.BaseSHA {
		t.Fatalf("slot %+v", s)
	}
	if _, ok, err := src.Acquire(ctx, "acme", "docs", "f2#1", ""); ok || err != nil {
		t.Fatalf("a project with no repository: %v, %v", ok, err)
	}
	if _, ok, err := src.Acquire(ctx, "beta", "web", "f3#1", ""); ok || err != nil {
		t.Fatalf("another org's project of the same name: %v, %v", ok, err)
	}
	if _, ok, err := src.Acquire(ctx, "acme", "bad", "f4#1", ""); !ok || err == nil {
		t.Fatalf("a repository that is not owner/name: %v, %v", ok, err)
	}
	if st, _, err := src.State(ctx, "acme", "docs", "f2#1"); st != "" || err != nil {
		t.Fatalf("a run with no checkout: %q, %v", st, err)
	}

	// Unlanded: the slot is quarantined, its work kept.
	f.commit(l.Path, "work.txt", "work")
	if err := src.Release(ctx, "acme", "web", "f1#1"); err != nil {
		t.Fatal(err)
	}
	src.Wait()
	if s := f.slot(1); s.State != StateQuarantined || s.Reason != QuarantinedUnpushed {
		t.Fatalf("an unlanded run's slot: %+v", s)
	}
	// Landed: pushed onto the base branch, a later release gives the slot back.
	f.must(f.g.run(ctx, l.Path, "push", "--quiet", "origin", "HEAD:main"))
	if err := f.pool.Release(ctx, f.repo, 1, "f1#1", Proof{}); err != nil {
		t.Fatal(err)
	}
	if s := f.slot(1); s.State != StateFree {
		t.Fatalf("a landed run's slot was kept: %+v", s)
	}
	// A run that holds no slot, or whose project names no repository, releases nothing.
	for _, c := range [][3]string{{"acme", "web", "f9#1"}, {"acme", "docs", "f2#1"}} {
		if err := src.Release(ctx, c[0], c[1], c[2]); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
	}
	src.Wait()

	// A preparation that fails for a reason not the slot's own (here, a base the remote lacks)
	// reports failed with why, and frees the slot.
	if _, _, err := src.Acquire(ctx, "acme", "web", "f5#1", "nope"); err != nil {
		t.Fatal(err)
	}
	src.Wait()
	if st, why, _ := src.State(ctx, "acme", "web", "f5#1"); st != PrepFailed || !strings.Contains(why, "not a branch of the remote") {
		t.Fatalf("a failed preparation: %q, %q", st, why)
	}
	if s := f.slot(1); s.State != StateFree {
		t.Fatalf("a failed preparation's slot: %+v", s)
	}
}

// A restart settles what a stop cut short: a slot left preparing is recorded as stopped, so its
// run sees it failed and the slot is free again, and a finished run's leased slot is released.
func TestSourceRecoversAfterARestart(t *testing.T) {
	f := open(t)
	src := &Source{Pool: f.pool, Remote: func(string) string { return f.repo.Remote }}
	src.SetRunways([]spec.Resource{runwayRes("acme", "web", "acme/web")})
	if _, err := f.pool.Acquire(ctx, f.repo, "done#1", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Acquire(ctx, f.repo, "live#1", "main"); err != nil {
		t.Fatal(err)
	}
	f.pool.Max = 3
	if _, err := f.pool.Reserve(ctx, f.repo, "cut#1", "main"); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	f.pool.Max = 3
	src.Pool = f.pool
	finished := func(_ context.Context, run string) (bool, error) { return run == "done#1", nil }
	if err := src.Recover(ctx, finished); err != nil {
		t.Fatal(err)
	}
	src.Wait()
	if st, why, _ := src.State(ctx, "acme", "web", "cut#1"); st != PrepFailed || why != PrepareStopped {
		t.Fatalf("the cut-short preparation: %q, %q", st, why)
	}
	for n, want := range map[int]string{1: StateFree, 2: StateLeased, 3: StateFree} {
		if s := f.slot(n); s.State != want {
			t.Fatalf("slot %d: %+v, want %s", n, s, want)
		}
	}
}

// A run's checkout is reported with every symlink resolved, as the runner resolves a Pilot's tool
// paths, so blocked paths relative to it match even when the pool's root is reached through a
// symlink (LUCY-SPEC-015).
func TestSourceCheckoutResolvesSymlinks(t *testing.T) {
	f := open(t)
	real := filepath.Join(f.dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(f.dir, "link")); err != nil {
		t.Fatal(err)
	}
	f.pool.Root = filepath.Join(f.dir, "link", "pool")
	src := &Source{Pool: f.pool, Remote: func(string) string { return f.repo.Remote }}
	src.SetRunways([]spec.Resource{runwayRes("acme", "web", "acme/web")})
	l, ok, err := src.Acquire(ctx, "acme", "web", "f1#1", "")
	if err != nil || !ok {
		t.Fatalf("acquire: %v, %v", ok, err)
	}
	src.Wait()
	want, err := filepath.EvalSymlinks(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := src.Checkout(ctx, "acme", "web", "f1#1"); !ok || got != want || got == l.Path {
		t.Fatalf("checkout %q, %v; want %q, not the symlinked %q", got, ok, want, l.Path)
	}
	if got, ok := src.Checkout(ctx, "acme", "web", "f9#1"); ok {
		t.Fatalf("a run that holds no slot has checkout %q", got)
	}
}

// A reload replaces the Runways while runs are placed: SetRunways and every reader of the Runways
// may overlap, and each reader sees one whole set, the old or the new (run under -race).
func TestSetRunwaysOverlapsReads(t *testing.T) {
	src := &Source{}
	src.SetRunways([]spec.Resource{runwayRes("acme", "web", "acme/web")})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			repo := "acme/web"
			if i%2 == 1 {
				repo = "acme/site"
			}
			src.SetRunways([]spec.Resource{runwayRes("acme", "web", repo)})
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		remote, ok, err := src.RemoteOf("acme", "web")
		if err != nil || !ok || (!strings.HasSuffix(remote, "/acme/web.git") && !strings.HasSuffix(remote, "/acme/site.git")) {
			t.Fatalf("RemoteOf: %q, %v, %v", remote, ok, err)
		}
		src.repoNamed("acme", "web")
	}
}
