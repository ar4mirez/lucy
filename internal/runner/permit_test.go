package runner

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/harness"
	"github.com/ar4mirez/lucy/internal/harness/claudecode"
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
// refused without asking the engine; one inside is asked about, resolved, and a pathless tool is
// asked about as the run's directory; the engine's denial and an unreachable engine both refuse.
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
	permit := permitFor(dir, nil, l, pm, slog.New(slog.DiscardHandler))
	for _, p := range []string{"/etc/hosts", filepath.Join(dir, "..", "x"), filepath.Join(dir, "escape", "x"), "escape/x", "~/.ssh/id_rsa", "../x"} {
		if ok, why := permit("Write", harness.Write, p); ok || !strings.Contains(why, "outside") {
			t.Errorf("%s: %v %q", p, ok, why)
		}
	}
	for _, p := range []string{"$HOME/x", "${HOME}/x", "$(pwd)/x", "src/$X", "$HOME.bak/../.ssh/x", "$TMPDIR-1/x"} {
		if ok, why := permit("Write", harness.Write, p); ok || !strings.Contains(why, "variable reference") {
			t.Errorf("%s: %v %q", p, ok, why)
		}
	}
	if len(pm.asked) != 0 {
		t.Fatalf("the engine was asked about outside paths: %+v", pm.asked)
	}
	for _, p := range []string{"app/routes/$postId.tsx", "app/routes/$slug", "routes/$_index.tsx"} {
		if ok, why := permit("Edit", harness.Write, p); !ok {
			t.Errorf("%s refused: %q", p, why)
		}
	}
	if ok, why := permit("Edit", harness.Write, "app/routes/posts.$slug.tsx"); !ok {
		t.Fatalf("a $ inside a name refused: %q", why)
	}
	pm.asked = nil
	if ok, why := permit("Edit", harness.Write, "src/main.go"); !ok {
		t.Fatalf("inside path refused: %q", why)
	}
	if ok, why := permit("Glob", harness.Read, ""); !ok {
		t.Fatalf("a pathless read was refused: %q", why)
	}
	want := []PermitRequest{
		{Tool: "Edit", Kind: policy.KindWrite, Path: filepath.Join(dir, "src", "main.go")},
		{Tool: "Glob", Kind: policy.KindRead, Path: dir},
	}
	if !slices.Equal(pm.asked, want) {
		t.Fatalf("asked %+v, want %+v", pm.asked, want)
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

// TestPermitForSearchGlobs: through the Claude Code adapter, a path or glob that may reach past the
// run's directory, by climbing, "~", "$" or braces, is refused without asking the engine; a
// relative Glob with no path is asked about with the run's directory as its path, and an absolute
// one inside the run with its static part.
func TestPermitForSearchGlobs(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pm := &fakePermitter{perm: Permission{Outcome: policy.Cleared}}
	session := harness.Session{Permit: permitFor(dir, claudecode.Adapter{}.Protected(), Lease{Task: Task{Run: "f1#1"}, Runner: "r1", Fence: 3}, pm, slog.New(slog.DiscardHandler))}
	for _, c := range []struct {
		tool, input string
		allowed     bool
	}{
		{"Glob", `{"pattern":"/home/someone/.ssh/*"}`, false},
		{"Grep", `{"pattern":"secret","glob":"../../*"}`, false},
		{"Read", `{"file_path":"~/.ssh/id_rsa"}`, false},
		{"Glob", `{"pattern":"~/.ssh/*"}`, false},
		{"Glob", `{"pattern":"{/etc,src}/*"}`, false},
		{"Glob", `{"pattern":"{..,x}/*"}`, false},
		{"Grep", `{"pattern":"secret","glob":"$HOME/*"}`, false},
		{"Glob", `{"pattern":"src/**/*.go"}`, true},
		{"Glob", `{"pattern":"` + dir + `/src/*.go"}`, true},
	} {
		in := &recorder{}
		conn, err := claudecode.Adapter{}.Open(in, session)
		if err != nil {
			t.Fatal(err)
		}
		evs, _ := conn.Decode([]byte(`{"type":"control_request","request_id":"req_1","request":{"subtype":"can_use_tool","tool_name":"` +
			c.tool + `","input":` + c.input + `}}`))
		if len(evs) != 1 || strings.HasPrefix(evs[0].Text, "allowed tool") != c.allowed || strings.Contains(in.String(), `"allow"`) != c.allowed {
			t.Errorf("%s %s: %+v, wrote %s", c.tool, c.input, evs, in.String())
		}
	}
	want := []PermitRequest{{Tool: "Glob", Kind: policy.KindRead, Path: dir}, {Tool: "Glob", Kind: policy.KindRead, Path: filepath.Join(dir, "src")}}
	if !slices.Equal(pm.asked, want) {
		t.Fatalf("asked %+v, want %+v", pm.asked, want)
	}
}

type recorder struct{ strings.Builder }

func (*recorder) Close() error { return nil }

// TestPermitForProtectsHarnessConfig: a write to the harness's own configuration inside the run, in
// any spelling and at any depth, is refused without asking the engine; a read of it and a write to
// a file merely named like it are asked about.
func TestPermitForProtectsHarnessConfig(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pm := &fakePermitter{perm: Permission{Outcome: policy.Cleared}}
	permit := permitFor(dir, claudecode.Adapter{}.Protected(), Lease{Task: Task{Run: "f1#1"}, Runner: "r1", Fence: 3}, pm, slog.New(slog.DiscardHandler))
	for _, c := range []struct{ tool, path string }{
		{"Write", ".claude/settings.json"},
		{"Edit", ".CLAUDE/settings.local.json"},
		{"Write", "sub/.Claude/hooks.json"},
		{"NotebookEdit", filepath.Join(dir, ".claude", "agents", "a.md")},
	} {
		if ok, why := permit(c.tool, harness.Write, c.path); ok || !strings.Contains(why, "configuration") {
			t.Errorf("%s %s: %v %q", c.tool, c.path, ok, why)
		}
	}
	if len(pm.asked) != 0 {
		t.Fatalf("the engine was asked about the harness's configuration: %+v", pm.asked)
	}
	if ok, why := permit("Write", harness.Write, "src/claude.go"); !ok {
		t.Fatalf("src/claude.go refused: %q", why)
	}
	if ok, why := permit("Read", harness.Read, ".claude/settings.json"); !ok {
		t.Fatalf("a read of the configuration refused: %q", why)
	}
	want := []PermitRequest{
		{Tool: "Write", Kind: policy.KindWrite, Path: filepath.Join(dir, "src", "claude.go")},
		{Tool: "Read", Kind: policy.KindRead, Path: filepath.Join(dir, ".claude", "settings.json")},
	}
	if !slices.Equal(pm.asked, want) {
		t.Fatalf("asked %+v, want %+v", pm.asked, want)
	}
}
