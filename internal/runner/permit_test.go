package runner

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/harness"
	"github.com/ar4mirez/lucy/internal/policy"
)

// TestPermit: the policy engine decides a Pilot's prompted file tool as a ship worker of the run's
// org, under the run's lease, and the decision is recorded before it is returned.
func TestPermit(t *testing.T) {
	f := open(t)
	ctx := context.Background()
	f.submit("f1#1")
	l := f.lease("r1", 0)
	for _, kind := range []policy.Kind{policy.KindWrite, policy.KindRead} {
		perm, err := f.b.Permit(ctx, "r1", "f1#1", l.Fence, PermitRequest{Tool: "Edit", Kind: kind, Path: "/scratch/run-1/main.go"})
		if err != nil || perm.Outcome != policy.Cleared {
			t.Fatalf("%s: %+v, %v", kind, perm, err)
		}
	}
	evs, err := f.store.ReadStream(ctx, policy.Stream, 0, 10)
	if err != nil || len(evs) != 2 {
		t.Fatalf("decisions %d, %v", len(evs), err)
	}
	rec, err := eventstore.DecodeAs[policy.DecisionRecord](f.store.Registry(), evs[0])
	if err != nil {
		t.Fatal(err)
	}
	if rec.Actor.Role != policy.RoleWorker || rec.Actor.Org != "acme" || rec.Target.Project != "web" ||
		rec.Rule != "write.worker-only" || !rec.Tainted || evs[0].Actor != "runner/r1" {
		t.Fatalf("record %+v by %s", rec, evs[0].Actor)
	}
	for _, c := range []struct {
		runner string
		fence  int64
		req    PermitRequest
		want   error
	}{
		{"r9", l.Fence, PermitRequest{Tool: "Edit", Kind: policy.KindWrite}, ErrFenced},
		{"r1", l.Fence + 1, PermitRequest{Tool: "Edit", Kind: policy.KindWrite}, ErrFenced},
		{"r1", l.Fence, PermitRequest{Tool: "Bash", Kind: policy.KindDelete}, ErrInvalid},
		{"r1", l.Fence, PermitRequest{Kind: policy.KindWrite}, ErrInvalid},
	} {
		if _, err := f.b.Permit(ctx, c.runner, "f1#1", c.fence, c.req); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c, err, c.want)
		}
	}
	if evs, _ := f.store.ReadStream(ctx, policy.Stream, 0, 10); len(evs) != 2 {
		t.Errorf("a refused permit was recorded as a decision: %d", len(evs))
	}
}

type fakePermitter struct {
	asked []PermitRequest
	perm  Permission
	err   error
}

func (p *fakePermitter) Permit(_ context.Context, _, _ string, _ int64, r PermitRequest) (Permission, error) {
	p.asked = append(p.asked, r)
	return p.perm, p.err
}

// TestPermitForStaysInsideTheRun: a path outside the run's directory, also through a symlink, is
// refused without asking the engine; one inside is asked about, resolved; the engine's denial and
// an unreachable engine both refuse.
func TestPermitForStaysInsideTheRun(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	l := Lease{Task: Task{Run: "f1#1"}, Runner: "r1", Fence: 3}
	pm := &fakePermitter{perm: Permission{Outcome: policy.Cleared}}
	permit := permitFor(dir, l, pm, slog.New(slog.DiscardHandler))
	for _, p := range []string{"/etc/hosts", filepath.Join(dir, "..", "x"), filepath.Join(dir, "escape", "x"), "escape/x"} {
		if ok, why := permit("Write", harness.Write, p); ok || !strings.Contains(why, "outside") {
			t.Errorf("%s: %v %q", p, ok, why)
		}
	}
	if len(pm.asked) != 0 {
		t.Fatalf("the engine was asked about outside paths: %+v", pm.asked)
	}
	if ok, why := permit("Edit", harness.Write, "src/main.go"); !ok {
		t.Fatalf("inside path refused: %q", why)
	}
	if ok, _ := permit("Grep", harness.Read, ""); !ok {
		t.Fatal("a pathless read was refused")
	}
	want := PermitRequest{Tool: "Edit", Kind: policy.KindWrite, Path: filepath.Join(dir, "src", "main.go")}
	if len(pm.asked) != 2 || pm.asked[0] != want || pm.asked[1].Kind != policy.KindRead {
		t.Fatalf("asked %+v", pm.asked)
	}
	pm.perm = Permission{Outcome: policy.Denied, Reason: "no"}
	if ok, why := permit("Edit", harness.Write, "a"); ok || why != "no" {
		t.Errorf("denied: %v %q", ok, why)
	}
	pm.err = errors.New("socket gone")
	if ok, _ := permit("Edit", harness.Write, "a"); ok {
		t.Error("an unreachable engine allowed the tool")
	}
}
