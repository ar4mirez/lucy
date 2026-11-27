package worktree

import (
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
// it, at the Runway's default branch; one naming none gets no checkout; and a finished run's slot
// is given back if its work landed, else kept with the refusal recorded.
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
	if err != nil || !ok || l.Slot != 1 || l.BaseSHA != f.head(f.work) {
		t.Fatalf("acquire: %+v, %v, %v", l, ok, err)
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

	// Unlanded: the slot is kept, and the refusal recorded.
	f.commit(l.Path, "work.txt", "work")
	if err := src.Release(ctx, "acme", "web", "f1#1"); err != nil {
		t.Fatal(err)
	}
	if s := f.slot(1); s.State != StateLeased {
		t.Fatalf("an unlanded run's slot was given back: %+v", s)
	}
	evs, err := f.store.ReadStream(ctx, Stream("acme", "web"), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if last := evs[len(evs)-1].Type; last != (ReleaseRefused{}).EventType() {
		t.Fatalf("the last event is %s, want the refusal", last)
	}
	// Landed: pushed onto the base branch, the slot is given back.
	f.must(f.g.run(ctx, l.Path, "push", "--quiet", "origin", "HEAD:main"))
	if err := src.Release(ctx, "acme", "web", "f1#1"); err != nil {
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
}
