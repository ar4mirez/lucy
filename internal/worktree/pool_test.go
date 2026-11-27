package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

// branch is the branch the pool gives run, whose id sanitizes to name.
func branch(name, run string) string {
	sum := sha256.Sum256([]byte(run))
	return "lucy/" + name + "-" + hex.EncodeToString(sum[:4])
}

// quarantines returns the worktree.quarantined events on the repository's stream, in order.
func (f *fixture) quarantines() []Quarantined {
	f.t.Helper()
	events, err := f.store.ReadStream(ctx, Stream("acme", "web"), 0, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []Quarantined
	for _, e := range events {
		if e.Type != (Quarantined{}).EventType() {
			continue
		}
		var q Quarantined
		if err := json.Unmarshal(e.Payload, &q); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, q)
	}
	return out
}

// types returns the types of the events on the repository's stream, in order.
func (f *fixture) types() []string {
	f.t.Helper()
	events, err := f.store.ReadStream(ctx, Stream("acme", "web"), 0, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
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
	if l.Slot != 1 || l.Branch != branch("f1-1", "f1#1") || l.BaseSHA != f.head(f.work) {
		t.Fatalf("lease %+v", l)
	}
	if b, err := os.ReadFile(filepath.Join(l.Path, "README.md")); err != nil || string(b) != "hello\n" {
		t.Fatalf("the slot's checkout: %q, %v", b, err)
	}
	if br, _ := f.g.run(ctx, l.Path, "branch", "--show-current"); br != l.Branch {
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

// Work that has not landed is quarantined: uncommitted changes, and commits on no ref of the
// remote, keep the slot with its work untouched, and it no longer counts against the pool's size,
// so the next runs get new slots up to the size. Once the commits are on the base branch, or the
// forge merged exactly the slot's head, a later release gives the slot back.
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
	if s := f.slot(1); s.State != StateQuarantined || s.Reason != QuarantinedUncommitted {
		t.Fatalf("a dirty slot: %+v", s)
	}
	head := f.commit(l.Path, "draft.txt", "unsaved")
	if err := f.pool.Release(ctx, f.repo, l.Slot, "f1#1", Proof{}); !errors.Is(err, ErrUnlanded) {
		t.Fatalf("a commit on no remote ref: %v", err)
	}
	if s := f.slot(1); s.State != StateQuarantined || s.Run != "f1#1" || s.Head != head || s.Reason != QuarantinedUnpushed {
		t.Fatalf("an unlanded slot: %+v", s)
	}
	// The quarantined slot does not count: with a pool of two, two more runs get slots.
	other, err := f.pool.Acquire(ctx, f.repo, "f2#1", "main")
	if err != nil || other.Slot != 2 {
		t.Fatalf("the next run: %+v, %v", other, err)
	}
	third, err := f.pool.Acquire(ctx, f.repo, "f3#1", "main")
	if err != nil || third.Slot != 3 {
		t.Fatalf("a run beside a quarantined slot: %+v, %v", third, err)
	}
	if _, err := f.pool.Acquire(ctx, f.repo, "f4#1", "main"); !errors.Is(err, ErrBusy) {
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
	want := []Quarantined{
		{Slot: 1, Run: "f1#1", Head: l.BaseSHA, Reason: QuarantinedUncommitted},
		{Slot: 1, Run: "f1#1", Head: head, Reason: QuarantinedUnpushed},
	}
	if got := f.quarantines(); !slices.Equal(got, want) {
		t.Fatalf("quarantines %+v, want %+v", got, want)
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

// A run that pushed its branch to the remote frees its slot at once (ProofPushed), with no merge.
func TestPushedBranchFreesItsSlot(t *testing.T) {
	f := open(t)
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	head := f.commit(l.Path, "feature.txt", "done")
	f.must(f.g.run(ctx, l.Path, "push", "--quiet", "origin", "HEAD:refs/heads/"+l.Branch))
	if err := f.pool.Release(ctx, f.repo, l.Slot, "f1#1", Proof{}); err != nil {
		t.Fatalf("a pushed branch: %v", err)
	}
	if s := f.slot(1); s.State != StateFree || s.Head != head {
		t.Fatalf("slot %+v", s)
	}
	evs, err := f.store.ReadStream(ctx, Stream("acme", "web"), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var r Released
	if err := json.Unmarshal(evs[len(evs)-1].Payload, &r); err != nil || r.Proof != ProofPushed {
		t.Fatalf("the release %+v, %v", r, err)
	}
}

// A quarantined slot whose branch is pushed later is freed by the next fresh preparation of the
// repository, and leased again.
func TestQuarantinedSlotIsFreedOnceItsWorkLands(t *testing.T) {
	f := open(t)
	f.pool.Max = 1
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	f.commit(l.Path, "feature.txt", "done")
	if err := f.pool.Release(ctx, f.repo, l.Slot, "f1#1", Proof{}); !errors.Is(err, ErrUnlanded) {
		t.Fatal(err)
	}
	f.must(f.g.run(ctx, l.Path, "push", "--quiet", "origin", "HEAD:refs/heads/"+l.Branch))
	next, err := f.pool.Acquire(ctx, f.repo, "f2#1", "main")
	if err != nil || next.Slot != 2 {
		t.Fatalf("the next run: %+v, %v", next, err)
	}
	if s := f.slot(1); s.State != StateFree {
		t.Fatalf("the landed quarantined slot: %+v", s)
	}
	if err := f.pool.Release(ctx, f.repo, next.Slot, "f2#1", Proof{}); err != nil {
		t.Fatal(err)
	}
	if again, err := f.pool.Acquire(ctx, f.repo, "f3#1", "main"); err != nil || again.Slot != 1 {
		t.Fatalf("the freed slot is leased again: %+v, %v", again, err)
	}
}

// A slot that cannot be made is recorded as broken and never leased; the next run gets another.
func TestBrokenSlotIsNotLeasedAgain(t *testing.T) {
	f := open(t)
	stray := filepath.Join(f.pool.Root, "acme", "web", "slot-1")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main"); err == nil {
		t.Fatal("a slot over a directory with files the pool did not make was leased")
	}
	if s := f.slot(1); s.State != StateBroken || !strings.Contains(s.Reason, "git worktree") {
		t.Fatalf("slot %+v", s)
	}
	if b, err := os.ReadFile(filepath.Join(stray, "stray")); err != nil || string(b) != "x" {
		t.Fatalf("the stray file: %q, %v", b, err)
	}
	l, err := f.pool.Acquire(ctx, f.repo, "f2#1", "main")
	if err != nil || l.Slot != 2 {
		t.Fatalf("after a broken slot: %+v, %v", l, err)
	}
}

// A run whose slot broke after it committed on its branch is refused a new slot, and its branch
// keeps the commit: a fresh lease never moves a branch holding work that has not landed.
func TestRetryAfterBrokenSlotKeepsUnlandedBranch(t *testing.T) {
	f := open(t)
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	work := f.commit(l.Path, "draft.txt", "kept")
	if err := os.RemoveAll(l.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Path, "stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main"); err == nil {
		t.Fatal("a worktree was made over a directory with files in it")
	}
	if s := f.slot(1); s.State != StateBroken {
		t.Fatalf("slot %+v", s)
	}
	if _, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main"); !errors.Is(err, ErrUnlanded) {
		t.Fatalf("the retry: %v", err)
	}
	base := filepath.Join(f.pool.Root, "acme", "web", "base.git")
	if tip, err := f.g.run(ctx, base, "rev-parse", "refs/heads/"+l.Branch); err != nil || tip != work {
		t.Fatalf("the run's branch is at %s, %v; want %s", tip, err, work)
	}
	// The retry's slot was given back free, its preparation failed: it never held the run's work.
	if s := f.slot(2); s.State != StateFree || s.Prep != PrepFailed {
		t.Fatalf("the retry's slot %+v", s)
	}
}

// A slot broken with its directory gone leaves no registration holding the run's branch: the
// retry leases a new slot on the branch, and no further slot is broken.
func TestRetryAfterBrokenSlotReusesTheBranch(t *testing.T) {
	f := open(t)
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(l.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Path, []byte("stray"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main"); err == nil {
		t.Fatal("a worktree was made over a file")
	}
	if s := f.slot(1); s.State != StateBroken {
		t.Fatalf("slot %+v", s)
	}
	retry, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil || retry.Slot != 2 || retry.Branch != l.Branch {
		t.Fatalf("the retry: %+v, %v", retry, err)
	}
	if br, _ := f.g.run(ctx, retry.Path, "branch", "--show-current"); br != l.Branch {
		t.Fatalf("the new slot's branch %q", br)
	}
	if s := f.slot(2); s.State != StateLeased {
		t.Fatalf("slot %+v", s)
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
		"a run with '..'":        {f.repo, "a..b#1", "main"},
		"a run ending in .lock":  {f.repo, "a.lock", "main"},
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
	for name, proof := range map[string]Proof{
		"a head like a flag":   {Head: "--output=/tmp/x", Merge: f.head(f.work)},
		"a short merge commit": {Head: f.head(f.work), Merge: "abc123"},
		"a ref, not an id":     {Head: f.head(f.work), Merge: "refs/heads/main"},
	} {
		if err := f.pool.Release(ctx, f.repo, 1, "r#1", proof); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Runs whose ids differ only where a branch name cannot keep them apart still get distinct
// branches, and so can be leased at once.
func TestDistinctRunsGetDistinctBranches(t *testing.T) {
	f := open(t)
	a, err := f.pool.Acquire(ctx, f.repo, "a.b#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.pool.Acquire(ctx, f.repo, "a-b#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if a.Branch != branch("a.b-1", "a.b#1") || b.Branch != branch("a-b-1", "a-b#1") || a.Branch == b.Branch {
		t.Fatalf("branches %q and %q", a.Branch, b.Branch)
	}
}

// A run that asks again for a slot it holds gets the same lease: its work is left alone, and a
// worktree that went missing is made again.
func TestAcquireAgainReturnsTheHeldSlot(t *testing.T) {
	f := open(t)
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil || again != l {
		t.Fatalf("a second acquire: %+v, %v; first %+v", again, err, l)
	}
	if _, err := f.pool.Acquire(ctx, f.repo, "f1#1", "release"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the held slot for another base: %v", err)
	}

	work := f.commit(l.Path, "draft.txt", "kept")
	if err := os.WriteFile(filepath.Join(l.Path, "notes.txt"), []byte("unsaved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if again, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main"); err != nil || again != l {
		t.Fatalf("with work in it: %+v, %v", again, err)
	}
	if f.head(l.Path) != work {
		t.Fatalf("the run's commit was reset away: head %s", f.head(l.Path))
	}
	if b, err := os.ReadFile(filepath.Join(l.Path, "notes.txt")); err != nil || string(b) != "unsaved" {
		t.Fatalf("the run's uncommitted work: %q, %v", b, err)
	}

	// The worktree goes missing with the run's commit on its branch: it is made again on the
	// branch, and the commit is kept.
	if err := os.RemoveAll(l.Path); err != nil {
		t.Fatal(err)
	}
	if again, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main"); err != nil || again != l {
		t.Fatalf("with the worktree missing: %+v, %v", again, err)
	}
	if f.head(l.Path) != work {
		t.Fatalf("the remade worktree dropped the run's commit: head %s", f.head(l.Path))
	}
	if br, _ := f.g.run(ctx, l.Path, "branch", "--show-current"); br != l.Branch {
		t.Fatalf("the remade worktree's branch %q", br)
	}

	// HEAD detached at the base while the branch is ahead: the worktree is put back on the branch.
	f.must(f.g.run(ctx, l.Path, "checkout", "--quiet", "--detach", l.BaseSHA))
	if again, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main"); err != nil || again != l {
		t.Fatalf("with HEAD detached at the base: %+v, %v", again, err)
	}
	if f.head(l.Path) != work {
		t.Fatalf("the branch's commit was reset away: head %s", f.head(l.Path))
	}
	if br, _ := f.g.run(ctx, l.Path, "branch", "--show-current"); br != l.Branch {
		t.Fatalf("the worktree's branch %q", br)
	}

	// A worktree that goes missing with nothing beyond its base is made again at the base.
	m, err := f.pool.Acquire(ctx, f.repo, "f9#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(m.Path); err != nil {
		t.Fatal(err)
	}
	if again, err := f.pool.Acquire(ctx, f.repo, "f9#1", "main"); err != nil || again != m || f.head(m.Path) != m.BaseSHA {
		t.Fatalf("an empty worktree remade: %+v, %v", again, err)
	}
	if br, _ := f.g.run(ctx, m.Path, "branch", "--show-current"); br != m.Branch {
		t.Fatalf("the remade empty worktree's branch %q", br)
	}
	if slots, err := f.pool.Slots(ctx, "acme", "web"); err != nil || len(slots) != 2 || slots[0].State != StateLeased || slots[1].State != StateLeased {
		t.Fatalf("slots %+v, %v", slots, err)
	}
}

// A release cancelled while the forge's merge is checked records nothing: the slot stays leased.
func TestCancelledReleaseRecordsNothing(t *testing.T) {
	f := open(t)
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	head := f.commit(l.Path, "draft.txt", "work")
	f.must(f.g.run(ctx, f.work, "checkout", "--quiet", "-b", "side"))
	side := f.commit(f.work, "side.txt", "side")
	f.must(f.g.run(ctx, f.work, "push", "--quiet", "origin", "side"))
	marker := filepath.Join(f.dir, "checking")
	script := filepath.Join(f.dir, "git-slow-merge-check")
	body := "#!/bin/sh\ncase \"$*\" in *\"merge-base --is-ancestor --end-of-options " + side + "\"*) : > " +
		strconv.Quote(marker) + "; exec sleep 30;; esac\nexec git \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	f.pool.Git = script
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for cctx.Err() == nil {
			if _, err := os.Lstat(marker); err == nil {
				cancel()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if err := f.pool.Release(cctx, f.repo, l.Slot, "f1#1", Proof{Head: head, Merge: side}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled release: %v", err)
	}
	if got := f.quarantines(); len(got) != 0 {
		t.Fatalf("quarantines %+v", got)
	}
	if s := f.slot(1); s.State != StateLeased || s.Run != "f1#1" {
		t.Fatalf("slot %+v", s)
	}
}

// A run acquiring its held slot again, clean at its base, keeps its ignored files: only a free
// slot is ever fully reset.
func TestAcquireAgainKeepsIgnoredFiles(t *testing.T) {
	f := open(t)
	f.commit(f.work, ".gitignore", "build/\n")
	f.must(f.g.run(ctx, f.work, "push", "--quiet", "origin", "HEAD:main"))
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(l.Path, "build", "out")
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("built"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func(){
		"on its branch":        func() {},
		"detached at its base": func() { f.must(f.g.run(ctx, l.Path, "checkout", "--quiet", "--detach", l.BaseSHA)) },
	} {
		setup()
		if again, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main"); err != nil || again != l {
			t.Fatalf("%s: %+v, %v", name, again, err)
		}
		if b, err := os.ReadFile(out); err != nil || string(b) != "built" {
			t.Fatalf("%s: the ignored file: %q, %v", name, b, err)
		}
		if br, _ := f.g.run(ctx, l.Path, "branch", "--show-current"); br != l.Branch {
			t.Fatalf("%s: branch %q", name, br)
		}
	}
}

// A preparation cancelled while the slot's worktree is being made is recorded as stopped, never
// broken: the slot is given back free, and the next lease makes it and succeeds.
func TestCancelledPreparationLeavesTheSlotReusable(t *testing.T) {
	f := open(t)
	marker := filepath.Join(f.dir, "adding")
	script := filepath.Join(f.dir, "git-slow-add")
	body := "#!/bin/sh\ncase \"$*\" in *\"worktree add\"*) : > " + strconv.Quote(marker) + "; exec sleep 30;; esac\nexec git \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	f.pool.Git = script
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for cctx.Err() == nil {
			if _, err := os.Lstat(marker); err == nil {
				cancel()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if _, err := f.pool.Acquire(cctx, f.repo, "f1#1", "main"); err == nil {
		t.Fatal("a cancelled preparation succeeded")
	}
	if s := f.slot(1); s.State != StateFree || s.Prep != PrepFailed || s.Reason != PrepareStopped {
		t.Fatalf("slot %+v", s)
	}
	f.pool.Git = ""
	l, err := f.pool.Acquire(ctx, f.repo, "f2#1", "main")
	if err != nil || l.Slot != 1 || f.head(l.Path) != l.BaseSHA {
		t.Fatalf("the next lease: %+v, %v", l, err)
	}
}

// Discard removes a quarantined or broken slot for the owner, recorded first, and nothing else:
// a leased or free slot is refused, the other slots and the clone stay, the run's branch stays in
// the clone, and the slot's number is not used again.
func TestDiscardRemovesOnlyAQuarantinedOrBrokenSlot(t *testing.T) {
	f := open(t)
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Discard(ctx, "acme", "web", 1, "owner"); !errors.Is(err, ErrInUse) {
		t.Fatalf("a leased slot: %v", err)
	}
	work := f.commit(l.Path, "draft.txt", "kept")
	if err := f.pool.Release(ctx, f.repo, 1, "f1#1", Proof{}); !errors.Is(err, ErrUnlanded) {
		t.Fatal(err)
	}
	other, err := f.pool.Acquire(ctx, f.repo, "f2#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.pool.Release(ctx, f.repo, other.Slot, "f2#1", Proof{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Discard(ctx, "acme", "web", other.Slot, "owner"); !errors.Is(err, ErrInUse) {
		t.Fatalf("a free slot: %v", err)
	}
	if _, err := f.pool.Discard(ctx, "acme", "web", 9, "owner"); !errors.Is(err, ErrNotHeld) {
		t.Fatalf("no such slot: %v", err)
	}

	if os.Geteuid() != 0 {
		// A removal that fails finds the discard recorded already, and a second discard retries it.
		if err := os.Chmod(l.Path, 0o500); err != nil {
			t.Fatal(err)
		}
		_, err := f.pool.Discard(ctx, "acme", "web", 1, "owner")
		if cerr := os.Chmod(l.Path, 0o700); cerr != nil {
			t.Fatal(cerr)
		}
		if err == nil {
			t.Fatal("a removal that cannot delete succeeded")
		}
		if got := f.types(); got[len(got)-1] != (Discarded{}).EventType() {
			t.Fatalf("the discard was not recorded before the removal: %v", got)
		}
	}
	s, err := f.pool.Discard(ctx, "acme", "web", 1, "owner")
	if err != nil || s.State != StateDiscarded {
		t.Fatalf("discard: %+v, %v", s, err)
	}
	if s := f.slot(1); s.State != StateDiscarded {
		t.Fatalf("slot %+v", s)
	}
	evs, err := f.store.ReadStream(ctx, Stream("acme", "web"), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var d Discarded
	if err := json.Unmarshal(evs[len(evs)-1].Payload, &d); err != nil {
		t.Fatal(err)
	}
	if d != (Discarded{Slot: 1, Run: "f1#1", Branch: l.Branch, Head: work, By: "owner"}) {
		t.Fatalf("the record %+v", d)
	}
	if _, err := os.Lstat(l.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the slot's directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(other.Path, "README.md")); err != nil {
		t.Fatalf("the other slot: %v", err)
	}
	base := filepath.Join(f.pool.Root, "acme", "web", "base.git")
	if tip, err := f.g.run(ctx, base, "rev-parse", "refs/heads/"+l.Branch); err != nil || tip != work {
		t.Fatalf("the run's branch: %s, %v", tip, err)
	}
	// Two runs at once: the free slot 2, and a new slot 3, never 1 again.
	a, err := f.pool.Acquire(ctx, f.repo, "f3#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.pool.Acquire(ctx, f.repo, "f4#1", "main")
	if err != nil || a.Slot != 2 || b.Slot != 3 {
		t.Fatalf("after a discard: %+v, %+v, %v", a, b, err)
	}
}

// A base clone left with no origin, as when making it stopped after git init, is repaired.
func TestBaseCloneWithoutOriginIsRepaired(t *testing.T) {
	f := open(t)
	dir := filepath.Join(f.pool.Root, "acme", "web", "base.git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	f.must(f.g.run(ctx, f.dir, "init", "--quiet", "--bare", "--", dir))
	l, err := f.pool.Acquire(ctx, f.repo, "f1#1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if l.BaseSHA != f.head(f.work) {
		t.Fatalf("lease %+v", l)
	}

	other := Repo{Org: "acme", Name: "api", Remote: f.repo.Remote}
	odir := filepath.Join(f.pool.Root, "acme", "api", "base.git")
	if err := os.MkdirAll(filepath.Dir(odir), 0o700); err != nil {
		t.Fatal(err)
	}
	f.must(f.g.run(ctx, f.dir, "clone", "--quiet", "--bare", "--", f.repo.Remote, odir))
	f.must(f.g.run(ctx, odir, "remote", "remove", "origin"))
	if _, err := f.pool.Acquire(ctx, other, "f2#1", "main"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a clone with refs and no origin: %v", err)
	}
}

// Leases of a repository made at once, on its first use, all succeed with distinct slots.
func TestConcurrentAcquires(t *testing.T) {
	f := open(t)
	f.pool.Max = 4
	var wg sync.WaitGroup
	leases := make([]Lease, 4)
	errs := make([]error, 4)
	for i := range leases {
		wg.Go(func() {
			leases[i], errs[i] = f.pool.Acquire(ctx, f.repo, "f"+strconv.Itoa(i)+"#1", "main")
		})
	}
	wg.Wait()
	seen := map[int]bool{}
	for i, l := range leases {
		if errs[i] != nil {
			t.Fatalf("acquire %d: %v", i, errs[i])
		}
		if seen[l.Slot] {
			t.Fatalf("slot %d leased twice: %+v", l.Slot, leases)
		}
		seen[l.Slot] = true
	}
}

// Every way a preparation ends maps to the event that records it, so none leaves its slot
// preparing: ready, the slot's own failure (broken), a stop, and any other error, a failed read of
// the slot or a failed record of ready included (a failed preparation). Only a slot no longer the
// run's, or ready already, records nothing.
func TestEveryPreparationOutcomeIsRecorded(t *testing.T) {
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	appendFailed := errors.New("eventstore: disk I/O error")
	for name, c := range map[string]struct {
		ctx  context.Context
		err  error
		want any
	}{
		"ready":               {ctx, nil, Prepared{Slot: 3, Run: "r#1", BaseSHA: "abc"}},
		"the slot's own":      {ctx, brokenError{errors.New("git worktree add: exit 128")}, Broken{Slot: 3, Reason: "git worktree add: exit 128"}},
		"stopped":             {stopped, brokenError{context.Canceled}, PrepareFailed{Slot: 3, Run: "r#1", Reason: PrepareStopped}},
		"a failed read":       {ctx, errors.New("sql: database is locked"), PrepareFailed{Slot: 3, Run: "r#1", Reason: "sql: database is locked"}},
		"a failed record":     {ctx, appendFailed, PrepareFailed{Slot: 3, Run: "r#1", Reason: appendFailed.Error()}},
		"no longer the run's": {ctx, ErrNotHeld, nil},
		"ready already":       {ctx, errReady, nil},
	} {
		got := prepOutcome(c.ctx, 3, "r#1", "abc", c.err)
		if c.want == nil && got != nil || c.want != nil && any(got) != c.want {
			t.Errorf("%s: %#v, want %#v", name, got, c.want)
		}
	}
}

// A pool root others can open fails the reservation at once, before any slot is recorded.
func TestReserveRefusesAnOpenRoot(t *testing.T) {
	f := open(t)
	if err := os.MkdirAll(f.pool.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.pool.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Reserve(ctx, f.repo, "f1#1", "main"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reserve: %v", err)
	}
	if slots, err := f.pool.Slots(ctx, "acme", "web"); err != nil || len(slots) != 0 {
		t.Fatalf("slots %+v, %v", slots, err)
	}
}
