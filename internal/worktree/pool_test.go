package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// The oracle is LUCY-RUN-007: slots leased in the store, reset on acquire, released only with a
// landed-work proof, so unlanded work is never torn down.

var ctx = context.Background()

type fixture struct {
	t      *testing.T
	dir    string
	store  *eventstore.Store
	pool   *Pool
	repo   Repo
	work   string // a clone of the remote the test pushes from, as a forge's merges would land
	g      git
	stores []*eventstore.Store
}

func open(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir, g: git{home: dir}}
	remote := filepath.Join(dir, "remote.git")
	f.must(f.g.run(ctx, dir, "init", "--quiet", "--bare", "--initial-branch=main", remote))
	f.work = filepath.Join(dir, "work")
	f.must(f.g.run(ctx, dir, "clone", "--quiet", remote, f.work))
	f.commit(f.work, "README.md", "hello\n")
	f.must(f.g.run(ctx, f.work, "push", "--quiet", "origin", "HEAD:main"))
	f.repo = Repo{Org: "acme", Name: "web", Remote: remote}
	f.reopen()
	return f
}

// reopen opens the store again, as a daemon restart does.
func (f *fixture) reopen() {
	f.t.Helper()
	if f.store != nil {
		if err := f.store.Close(); err != nil {
			f.t.Fatal(err)
		}
	}
	reg, err := eventstore.NewRegistry(Payloads()...)
	if err != nil {
		f.t.Fatal(err)
	}
	st, err := eventstore.Open(ctx, filepath.Join(f.dir, "lucy", "lucy.db"), eventstore.Options{Registry: reg,
		Projectors: []eventstore.Projector{Projector{}}, SnapshotDir: filepath.Join(f.dir, "lucy", "state")})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { st.Close() })
	f.store = st
	f.pool = &Pool{Store: st, Root: filepath.Join(f.dir, "pool"), Max: 2}
}

func (f *fixture) must(_ string, err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

// commit writes name in the checkout at dir and commits it, returning the commit.
func (f *fixture) commit(dir, name, content string) string {
	f.t.Helper()
	return f.commitAs(dir, name, content, "change "+name)
}

// commitAs is commit with the message msg.
func (f *fixture) commitAs(dir, name, content, msg string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.must(f.g.run(ctx, dir, "add", "--", name))
	f.must(f.g.run(ctx, dir, "commit", "--quiet", "-m", msg))
	sha, err := f.g.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		f.t.Fatal(err)
	}
	return sha
}

func (f *fixture) head(dir string) string {
	f.t.Helper()
	sha, err := f.g.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		f.t.Fatal(err)
	}
	return sha
}

func (f *fixture) slot(n int) SlotInfo {
	f.t.Helper()
	slots, err := f.pool.Slots(ctx, "acme", "web")
	if err != nil {
		f.t.Fatal(err)
	}
	for _, s := range slots {
		if s.Slot == n {
			return s
		}
	}
	f.t.Fatalf("no slot %d in %+v", n, slots)
	return SlotInfo{}
}

func TestAcquireResetsToTheBase(t *testing.T) {
	f := open(t)
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if l.Slot != 1 || l.Branch != "lucy/f1-1" || l.BaseSHA != f.head(f.work) {
		t.Fatalf("lease %+v", l)
	}
	if b, err := os.ReadFile(filepath.Join(l.Path, "README.md")); err != nil || string(b) != "hello\n" {
		t.Fatalf("the slot's checkout: %q, %v", b, err)
	}
	if br, _ := f.g.run(ctx, l.Path, "branch", "--show-current"); br != "lucy/f1-1" {
		t.Fatalf("branch %q", br)
	}
	if s := f.slot(1); s.State != StateLeased || s.Run != "f1#1" {
		t.Fatalf("slot %+v", s)
	}

	// Given back empty, then leased again after main moved on: whatever the last run left,
	// tracked, untracked or ignored, is gone, and the slot is at the new base on a new branch.
	if err := f.pool.Release(ctx, f.repo, 1, "f1#1", Proof{}); err != nil {
		t.Fatal(err)
	}
	newBase := f.commit(f.work, "NEWS.md", "news\n")
	f.must(f.g.run(ctx, f.work, "push", "--quiet", "origin", "HEAD:main"))
	if err := os.WriteFile(filepath.Join(l.Path, ".gitignore"), []byte("build/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Not left behind in a real run (Release refuses a dirty slot); planted to check the reset.
	if err := os.MkdirAll(filepath.Join(l.Path, "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	l2, err := f.pool.Acquire(ctx, f.repo, "f2#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if l2.Slot != 1 || l2.BaseSHA != newBase || f.head(l2.Path) != newBase {
		t.Fatalf("second lease %+v at %s", l2, f.head(l2.Path))
	}
	for _, gone := range []string{".gitignore", "build"} {
		if _, err := os.Lstat(filepath.Join(l2.Path, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s survived the reset: %v", gone, err)
		}
	}
}

// Work that has not landed keeps its slot: uncommitted changes, and commits on no base branch,
// are refused, and the next run gets another slot. Once the commits are on the base branch, or the
// forge merged exactly the slot's head, the slot is given back.
func TestReleaseNeedsLandedWork(t *testing.T) {
	f := open(t)
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Path, "draft.txt"), []byte("unsaved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.Release(ctx, f.repo, l.Slot, "f1#1", Proof{}); !errors.Is(err, ErrUnlanded) {
		t.Fatalf("uncommitted changes: %v", err)
	}
	head := f.commit(l.Path, "draft.txt", "unsaved")
	if err := f.pool.Release(ctx, f.repo, l.Slot, "f1#1", Proof{}); !errors.Is(err, ErrUnlanded) {
		t.Fatalf("a commit on no base branch: %v", err)
	}
	if s := f.slot(1); s.State != StateLeased || s.Run != "f1#1" {
		t.Fatalf("an unlanded slot was freed: %+v", s)
	}
	other, err := f.pool.Acquire(ctx, f.repo, "f2#1", "main")
	if err != nil || other.Slot != 2 {
		t.Fatalf("the next run: %+v, %v", other, err)
	}
	if _, err := f.pool.Acquire(ctx, f.repo, "f3#1", "main"); !errors.Is(err, ErrBusy) {
		t.Fatalf("beyond the pool's size: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(l.Path, "draft.txt")); err != nil || string(b) != "unsaved" {
		t.Fatalf("the unlanded work was touched: %q, %v", b, err)
	}

	// The forge squash-merged the pull request: a new commit on main, not the slot's own.
	f.must(f.g.run(ctx, f.work, "pull", "--quiet", "--ff-only"))
	squash := f.commitAs(f.work, "draft.txt", "unsaved", "the pull request, squashed")
	if squash == head {
		t.Fatal("the squashed commit is the slot's own")
	}
	f.must(f.g.run(ctx, f.work, "push", "--quiet", "origin", "HEAD:main"))
	if err := f.pool.Release(ctx, f.repo, l.Slot, "f1#1", Proof{Head: squash, Merge: squash}); !errors.Is(err, ErrUnlanded) {
		t.Fatalf("a proof for another head: %v", err)
	}
	if err := f.pool.Release(ctx, f.repo, l.Slot, "f1#1", Proof{Head: head, Merge: "0123456789abcdef0123456789abcdef01234567"}); !errors.Is(err, ErrUnlanded) {
		t.Fatalf("a merge commit not on the base: %v", err)
	}
	if err := f.pool.Release(ctx, f.repo, l.Slot, "f1#1", Proof{Head: head, Merge: squash}); err != nil {
		t.Fatalf("a merged pull request: %v", err)
	}
	if s := f.slot(1); s.State != StateFree || s.Head != head {
		t.Fatalf("slot %+v", s)
	}

	// The other run's commit lands on main directly (a fast-forward): its head is on the base.
	f.must(f.g.run(ctx, other.Path, "fetch", "--quiet", "origin"))
	f.must(f.g.run(ctx, other.Path, "rebase", "--quiet", "refs/remotes/origin/main"))
	pushed := f.commit(other.Path, "feature.txt", "done")
	f.must(f.g.run(ctx, other.Path, "push", "--quiet", "origin", "HEAD:main"))
	if err := f.pool.Release(ctx, f.repo, other.Slot, "f2#1", Proof{}); err != nil {
		t.Fatalf("a head on the base branch: %v", err)
	}
	if err := f.pool.Release(ctx, f.repo, other.Slot, "f2#1", Proof{}); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("a second release: %v", err)
	}
	if s := f.slot(2); s.Head != pushed {
		t.Fatalf("slot %+v", s)
	}
}

// A slot that cannot be made is recorded as broken and never leased; the next run gets another.
func TestBrokenSlotIsNotLeasedAgain(t *testing.T) {
	f := open(t)
	if err := os.MkdirAll(filepath.Join(f.pool.Root, "acme", "web", "slot-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main"); err == nil {
		t.Fatal("a slot over a directory the pool did not make was leased")
	}
	if s := f.slot(1); s.State != StateBroken {
		t.Fatalf("slot %+v", s)
	}
	l, err := f.pool.Acquire(ctx, f.repo, "f2#1", "main")
	if err != nil || l.Slot != 2 {
		t.Fatalf("after a broken slot: %+v, %v", l, err)
	}
}

// The pool's state is the store's: after a restart every slot is as it was, and a rebuild of the
// projection from the log matches.
func TestSlotsSurviveARestart(t *testing.T) {
	f := open(t)
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	before := f.slot(1)
	f.reopen()
	if after := f.slot(1); after != before {
		t.Fatalf("after a restart %+v, before %+v", after, before)
	}
	if err := f.store.Rebuild(ctx, Projector{}.Name()); err != nil {
		t.Fatal(err)
	}
	if rebuilt := f.slot(1); rebuilt != before {
		t.Fatalf("rebuilt %+v, before %+v", rebuilt, before)
	}
	if err := f.pool.Release(ctx, f.repo, l.Slot, "f1#1", Proof{}); err != nil {
		t.Fatal(err)
	}
}

func TestRefusesUnsafeInput(t *testing.T) {
	f := open(t)
	for name, c := range map[string]struct {
		repo      Repo
		run, base string
	}{
		"an ext:: remote":        {Repo{Org: "acme", Name: "web", Remote: "ext::sh -c touch% /tmp/pwned"}, "r#1", "main"},
		"a remote like a flag":   {Repo{Org: "acme", Name: "web", Remote: "--upload-pack=sh"}, "r#1", "main"},
		"a base like a flag":     {f.repo, "r#1", "--orphan"},
		"a base that is no ref":  {f.repo, "r#1", "a..b"},
		"a missing base":         {f.repo, "r#1", "nope"},
		"an org with a slash":    {Repo{Org: "a/b", Name: "web", Remote: f.repo.Remote}, "r#1", "main"},
		"a repository with dots": {Repo{Org: "acme", Name: "..", Remote: f.repo.Remote}, "r#1", "main"},
		"a run naming nothing":   {f.repo, "###", "main"},
	} {
		if _, err := f.pool.Acquire(ctx, c.repo, c.run, c.base); err == nil {
			t.Errorf("%s: leased", name)
		}
	}
	other := f.repo
	other.Remote = filepath.Join(f.dir, "elsewhere.git")
	if _, err := f.pool.Acquire(ctx, f.repo, "r#1", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Acquire(ctx, other, "r#2", "main"); !errors.Is(err, ErrInvalid) {
		t.Errorf("another remote for the same repository: %v", err)
	}
}
