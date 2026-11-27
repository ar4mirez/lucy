// Package worktree is lucy's built-in worktree pool (LUCY-RUN-007): per org and repository, a set
// of git worktrees (slots) that runs lease, recorded in the event store. A slot is reserved for a
// run without running git, then prepared (reset to the run's base) apart from the lease; it is
// given back only with proof that the work in it landed, and is otherwise quarantined, so unlanded
// work is never torn down.
//
// Layout, under the pool's root (0700):
//
//	<org>/<repo>/base.git   a bare clone of the repository's remote, fetched before every fresh preparation and release
//	<org>/<repo>/slot-<n>   slot n: a worktree of base.git
//
// The pool runs git with an environment of its own (no user or system configuration, no prompts,
// only the https, ssh and file transports). The one directory it deletes is a quarantined or broken
// slot the owner discards (Discard), through the path guard. What it lets git remove is base.git's
// stale worktree registrations (git worktree prune) and, when a free slot is reset for a new lease,
// that slot's tracked changes and its untracked and ignored files (checkout --force, reset --hard,
// clean -ffdx). That is safe only because a slot becomes free only after Release has proven its
// work landed, or found the run never started in it.
package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/pathguard"
)

// DefaultMax is how many slots a repository's pool holds when Pool.Max is zero.
const DefaultMax = 4

var (
	// ErrUnlanded refuses a release whose work has not landed: uncommitted changes, or commits on
	// no ref of the remote and in no merge the forge reported. The slot is quarantined.
	ErrUnlanded = errors.New("worktree: the slot's work has not landed")
	// ErrBusy refuses a lease when as many slots of the repository as the pool's size are leased
	// or broken, and none is free.
	ErrBusy = errors.New("worktree: every slot of the repository is in use")
	// ErrNotHeld refuses a release of a slot the run does not hold.
	ErrNotHeld = errors.New("worktree: the run does not hold this slot")
	// ErrInUse refuses to discard a slot that is free or leased: only a quarantined or broken one is.
	ErrInUse = errors.New("worktree: the slot is free or leased")
	// ErrInvalid refuses a malformed org, repository, run, base or remote.
	ErrInvalid = errors.New("worktree: invalid")
)

// Repo names a repository and where to fetch it from.
type Repo struct {
	Org    string // a label: a-z, 0-9 and '-'
	Name   string // a label: a-z, 0-9, '.', '_' and '-'
	Remote string // an https://, ssh:// or file URL, or a local path, of the repository
}

// Proof is the forge's word that the work landed: it merged the pull request whose head was Head,
// and Merge is the merge commit (or the squashed commit) on the base branch. The pool trusts it
// only when the slot's head is exactly Head and Merge is on the fetched base branch.
type Proof struct {
	Head  string
	Merge string
}

// Pool is the worktree pool. Its state is the worktree_slots projection, and every change is one
// store transaction; git runs outside the transaction, after a slot is recorded as leased, so no
// two runs ever share a slot. A Pool is safe for concurrent use: it runs one preparation, release or
// discard of a repository at a time, so the git steps on its base.git never race. One Pool per store is the
// unit of concurrency: two Pools over the same store and root do not serialize with each other.
type Pool struct {
	Store *eventstore.Store // registers Payloads() and the Projector
	Root  string            // absolute; created 0700
	Max   int               // slots per repository; DefaultMax when zero
	Git   string            // the git program; "git" on PATH when empty

	locks sync.Map // org/repo -> *sync.Mutex
}

// lock holds repo's lock until the returned func is called.
func (p *Pool) lock(repo Repo) func() {
	m, _ := p.locks.LoadOrStore(repo.Org+"/"+repo.Name, new(sync.Mutex))
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (p *Pool) git() git { return git{path: p.Git, home: p.Root} }

func (p *Pool) max() int {
	if p.Max <= 0 {
		return DefaultMax
	}
	return p.Max
}

// Lease is a slot a run holds.
type Lease struct {
	Slot    int
	Path    string // the slot's worktree: the run's working directory
	Branch  string // the branch the run works on, made at BaseSHA
	BaseSHA string
	// Deadline is when the slot's background preparation stops at the latest (Source.Acquire).
	Deadline time.Time
}

// Acquire leases run a slot of repo and prepares it: Reserve, then Prepare.
func (p *Pool) Acquire(ctx context.Context, repo Repo, run, base string) (Lease, error) {
	l, err := p.Reserve(ctx, repo, run, base)
	if err != nil {
		return Lease{}, err
	}
	return p.Prepare(ctx, repo, run, l.Slot)
}

// Reserve leases run a slot of repo for base, in one store transaction and without running git:
// the slot the run holds already (a retry, or a restart after the lease was recorded), else a free
// slot, else a new one while fewer than the pool's size are leased or broken (ErrBusy beyond); a
// quarantined or discarded slot is never leased and not counted. The slot is then being prepared,
// and the returned lease has no base commit until Prepare has made it ready.
func (p *Pool) Reserve(ctx context.Context, repo Repo, run, base string) (Lease, error) {
	if err := p.check(repo); err != nil {
		return Lease{}, err
	}
	branch, err := branchFor(run)
	if err != nil {
		return Lease{}, err
	}
	if err := checkBase(base); err != nil {
		return Lease{}, err
	}
	var l Lease
	err = p.Store.Update(ctx, func(tx *eventstore.Tx) error {
		held, err := scanSlot(tx.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND state = ? AND run = ?
			ORDER BY slot LIMIT 1`, repo.Org, repo.Name, StateLeased, run))
		switch {
		case err == nil:
			if held.Base != base {
				return fmt.Errorf("%w: run %q holds slot %d of %s/%s on base %q, not %q", ErrInvalid, run, held.Slot, repo.Org, repo.Name, held.Base, base)
			}
			l = Lease{Slot: held.Slot, Path: held.Path, Branch: held.Branch, BaseSHA: held.BaseSHA}
			_, err = tx.Append(ctx, Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon",
				Leased{Slot: held.Slot, Run: run, Branch: held.Branch, Base: base, BaseSHA: held.BaseSHA})
			return err
		case !errors.Is(err, ErrNotHeld):
			return err
		}
		s, err := scanSlot(tx.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND state = ?
			ORDER BY slot LIMIT 1`, repo.Org, repo.Name, StateFree))
		if errors.Is(err, ErrNotHeld) {
			var inUse, last int
			if err := tx.QueryRowContext(ctx, `SELECT coalesce(sum(state IN (?, ?)), 0), coalesce(max(slot), 0) FROM worktree_slots WHERE org = ? AND repo = ?`,
				StateLeased, StateBroken, repo.Org, repo.Name).Scan(&inUse, &last); err != nil {
				return err
			}
			if inUse >= p.max() {
				return ErrBusy
			}
			s = SlotInfo{Slot: last + 1, Path: filepath.Join(p.Root, repo.Org, repo.Name, "slot-"+strconv.Itoa(last+1))}
			if _, err := tx.Append(ctx, Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon",
				Added{Org: repo.Org, Repo: repo.Name, Slot: s.Slot, Path: s.Path}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		l = Lease{Slot: s.Slot, Path: s.Path, Branch: branch}
		_, err = tx.Append(ctx, Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon",
			Leased{Slot: s.Slot, Run: run, Branch: branch, Base: base})
		return err
	})
	if err != nil {
		return Lease{}, err
	}
	return l, nil
}

// Prepare makes slot, which Reserve leased run, ready for it, and records how that ended, on every
// path but one: when the slot is no longer leased to run, nothing is recorded. A fresh lease fetches
// the remote, resolves the base and resets the slot to it (or makes its worktree), on the run's
// branch, and frees each quarantined slot of repo whose work has landed since; a lease of a slot the
// run held already is repaired or put on the run's branch, its work kept. Ready is recorded with the
// base commit (Prepared). A failure of the slot's own worktree is recorded as broken: it is never
// leased again. Any other failure, including a failed read of the slot or a failed record of ready,
// or ctx ending, is recorded as a failed preparation (PrepareFailed; PrepareStopped when ctx ended),
// which leaves the slot reusable: a fresh lease is freed, and a held one stays leased to its run,
// for a later preparation to repair. Every outcome is recorded even when ctx has ended.
func (p *Pool) Prepare(ctx context.Context, repo Repo, run string, slot int) (Lease, error) {
	defer p.lock(repo)()
	l, err := p.prepare(ctx, repo, run, slot)
	ev := prepOutcome(ctx, slot, run, l.BaseSHA, err)
	if ev == nil {
		return l, ignoreReady(err)
	}
	rerr := p.record(ctx, repo, err, ev)
	if _, ok := ev.(Prepared); ok && rerr != nil {
		return Lease{}, p.record(ctx, repo, rerr, prepOutcome(ctx, slot, run, "", rerr))
	}
	if rerr != nil {
		return Lease{}, rerr
	}
	return l, nil
}

// errReady says prepare found the slot ready already: there is nothing to record.
var errReady = errors.New("worktree: the slot is ready already")

func ignoreReady(err error) error {
	if errors.Is(err, errReady) {
		return nil
	}
	return err
}

// brokenError is a failure of the slot's own worktree.
type brokenError struct{ error }

func (e brokenError) Unwrap() error { return e.error }

// prepOutcome is the event that records how a preparation of slot for run ended with err: Prepared
// at baseSHA, Broken for a failure of the slot's own worktree, PrepareFailed with PrepareStopped
// when ctx ended, else PrepareFailed with the error; nil when the slot is no longer run's or was
// ready already.
func prepOutcome(ctx context.Context, slot int, run, baseSHA string, err error) eventstore.Payload {
	var be brokenError
	switch {
	case err == nil:
		return Prepared{Slot: slot, Run: run, BaseSHA: baseSHA}
	case errors.Is(err, ErrNotHeld), errors.Is(err, errReady):
		return nil
	case ctx.Err() != nil:
		return PrepareFailed{Slot: slot, Run: run, Reason: PrepareStopped}
	case errors.As(err, &be):
		return Broken{Slot: slot, Reason: capReason(err.Error())}
	}
	return PrepareFailed{Slot: slot, Run: run, Reason: capReason(err.Error())}
}

// prepare is Prepare's work, under repo's lock: the slot made ready, or why not.
func (p *Pool) prepare(ctx context.Context, repo Repo, run string, slot int) (Lease, error) {
	if err := p.check(repo); err != nil {
		return Lease{}, err
	}
	s, err := scanSlot(p.Store.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND slot = ?`,
		repo.Org, repo.Name, slot))
	if err != nil {
		return Lease{}, err
	}
	if s.State != StateLeased || s.Run != run {
		return Lease{}, fmt.Errorf("%w: slot %d of %s/%s", ErrNotHeld, slot, repo.Org, repo.Name)
	}
	l := Lease{Slot: s.Slot, Path: s.Path, Branch: s.Branch, BaseSHA: s.BaseSHA}
	if s.Prep == PrepReady {
		return l, errReady
	}
	baseDir, how, err := p.plan(ctx, repo, &l, s.Base)
	if err != nil {
		return Lease{}, err
	}
	if err := p.reset(ctx, baseDir, l, how); err != nil {
		return Lease{}, brokenError{err}
	}
	return l, nil
}

// record appends ev to repo's stream, even when ctx has ended, and returns why joined with any
// error appending it.
func (p *Pool) record(ctx context.Context, repo Repo, why error, ev eventstore.Payload) error {
	if _, err := p.Store.Append(context.WithoutCancel(ctx), Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon", ev); err != nil {
		return errors.Join(why, err)
	}
	return why
}

// plan readies everything a preparation needs but the slot's worktree, and says how that is made.
// A held lease (one with a base commit) is planned from the slot as it is (heldPrep). A fresh lease
// fetches the remote, sets l's base commit to the base's tip and is planned as freshSlot; it is
// refused (ErrUnlanded) when the run's branch already holds commits that have not landed on the
// base.
func (p *Pool) plan(ctx context.Context, repo Repo, l *Lease, base string) (string, int, error) {
	g := p.git()
	baseDir, err := p.baseClone(ctx, repo)
	if err != nil {
		return "", 0, err
	}
	if _, err := g.run(ctx, baseDir, "worktree", "prune"); err != nil {
		return "", 0, err
	}
	if l.BaseSHA != "" {
		how, err := p.heldPrep(ctx, baseDir, *l)
		return baseDir, how, err
	}
	if _, err := g.run(ctx, baseDir, "fetch", "--prune", "--quiet", "origin"); err != nil {
		return "", 0, err
	}
	baseSHA, err := g.run(ctx, baseDir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+base+"^{commit}")
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, fmt.Errorf("%w: base %q is not a branch of the remote", ErrInvalid, base)
	}
	tip, err := branchTip(ctx, g, baseDir, l.Branch)
	if err != nil {
		return "", 0, err
	}
	if tip != "" && tip != baseSHA {
		landed, err := g.isAncestor(ctx, baseDir, tip, "refs/remotes/origin/"+base)
		if err != nil {
			return "", 0, err
		}
		if !landed {
			return "", 0, fmt.Errorf("%w: the run's branch %s holds commits that have not landed on %q", ErrUnlanded, l.Branch, base)
		}
	}
	if err := p.reclaim(ctx, repo, baseDir); err != nil {
		return "", 0, err
	}
	l.BaseSHA = baseSHA
	return baseDir, freshSlot, nil
}

// reclaim frees each quarantined slot of repo whose work has landed, as fetched into baseDir.
func (p *Pool) reclaim(ctx context.Context, repo Repo, baseDir string) error {
	slots, err := p.Slots(ctx, repo.Org, repo.Name)
	if err != nil {
		return err
	}
	for _, s := range slots {
		if s.State != StateQuarantined {
			continue
		}
		v, err := p.verdict(ctx, s, Proof{})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || v.proof == "" {
			continue // still held: a slot whose work cannot be read stays quarantined, for the owner
		}
		if _, err := p.Store.Append(ctx, Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon",
			Released{Slot: s.Slot, Run: s.Run, Head: v.head, Proof: v.proof, Merge: v.merge}); err != nil {
			return err
		}
	}
	return nil
}

// How a leased slot's worktree is prepared.
const (
	resetSlot      = iota // put a free slot's worktree back at the base, removing everything else
	remakeSlot            // make a held slot's worktree again: it is missing, or not one of base.git
	keepSlot              // leave a held slot as it is: it may hold the run's work, or is ready already
	rebranchSlot          // put a held slot, clean at its base, on the run's branch, keeping every file
	remakeOnBranch        // remakeSlot, on the run's branch, which is ahead of the base: never moved
	checkoutBranch        // put a held slot's clean worktree on the run's branch, ahead of the base: never moved
	freshSlot             // a freshly leased slot: resetSlot when it is a worktree of base.git, else remakeSlot
)

// heldPrep says how a slot the run holds already is prepared: made again when its worktree is
// missing or not a worktree of base.git, put on the run's branch when it is clean at the lease's
// base commit (keeping its untracked and ignored files), and otherwise kept as it is. When the run's branch has commits beyond the base,
// the worktree is made or put on the branch instead, and the branch is never moved.
func (p *Pool) heldPrep(ctx context.Context, baseDir string, l Lease) (int, error) {
	how, err := p.heldWorktree(ctx, baseDir, l)
	if err != nil || how == keepSlot {
		return how, err
	}
	tip, err := branchTip(ctx, p.git(), baseDir, l.Branch)
	switch {
	case err != nil:
		return 0, err
	case tip == "" || tip == l.BaseSHA:
		return how, nil
	case how == remakeSlot:
		return remakeOnBranch, nil
	default:
		return checkoutBranch, nil
	}
}

// branchTip is the commit branch is at in baseDir, or "" when there is no such branch.
func branchTip(ctx context.Context, g git, baseDir, branch string) (string, error) {
	tip, err := g.run(ctx, baseDir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return "", nil
	}
	return tip, err
}

// heldWorktree is heldPrep for the worktree alone: remakeSlot, rebranchSlot or keepSlot.
func (p *Pool) heldWorktree(ctx context.Context, baseDir string, l Lease) (int, error) {
	g := p.git()
	if _, err := os.Lstat(l.Path); errors.Is(err, fs.ErrNotExist) {
		return remakeSlot, nil
	} else if err != nil {
		return 0, err
	}
	common, err := g.run(ctx, l.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return remakeSlot, nil
	}
	if !samePath(common, baseDir) {
		return remakeSlot, nil
	}
	dirty, err := g.run(ctx, l.Path, "status", "--porcelain", "--untracked-files=all", "--ignored=no")
	if err != nil {
		return 0, err
	}
	head, err := g.run(ctx, l.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return 0, err
	}
	if dirty != "" || head != l.BaseSHA {
		return keepSlot, nil
	}
	cur, err := g.run(ctx, l.Path, "branch", "--show-current")
	if err != nil {
		return 0, err
	}
	if cur == l.Branch {
		return keepSlot, nil
	}
	return rebranchSlot, nil
}

// samePath reports whether a and b name the same directory, symbolic links resolved.
func samePath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	return err == nil && ra == rb
}

// reset makes the slot's worktree, or puts a free one back, at the lease's base commit with
// nothing else in it, on a fresh branch for the run. A free slot was given back with its work
// landed (Release), so nothing in it is lost. A held slot is only put on the run's branch: at the
// base when the branch is absent or there, else on the branch as it is, with every file kept.
func (p *Pool) reset(ctx context.Context, baseDir string, l Lease, how int) error {
	g := p.git()
	if how == freshSlot {
		fi, err := os.Lstat(l.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			how = remakeSlot
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("worktree: slot %d's path %s is not a directory", l.Slot, l.Path)
		default:
			common, err := g.run(ctx, l.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
			switch {
			case err == nil && samePath(common, baseDir):
				how = resetSlot
			case ctx.Err() != nil:
				return ctx.Err()
			default:
				how = remakeSlot
			}
		}
	}
	switch how {
	case keepSlot:
		return nil
	case remakeOnBranch:
		_, err := g.run(ctx, baseDir, "worktree", "add", "--quiet", "--force", "--", l.Path, l.Branch)
		return err
	case checkoutBranch:
		_, err := g.run(ctx, l.Path, "checkout", "--quiet", l.Branch)
		return err
	case rebranchSlot:
	case remakeSlot:
		if _, err := g.run(ctx, baseDir, "worktree", "add", "--quiet", "--force", "--detach", "--", l.Path, l.BaseSHA); err != nil {
			return err
		}
	default:
		for _, args := range [][]string{
			{"checkout", "--quiet", "--force", "--detach", l.BaseSHA},
			{"reset", "--quiet", "--hard", l.BaseSHA},
			{"clean", "-ffdxq"},
		} {
			if _, err := g.run(ctx, l.Path, args...); err != nil {
				return err
			}
		}
	}
	_, err := g.run(ctx, l.Path, "checkout", "--quiet", "-B", l.Branch, l.BaseSHA)
	return err
}

// Release gives run's slot back once its work has landed: the slot's worktree has no uncommitted
// change and its head is the base it was leased at (ProofEmpty), a commit on the base branch as
// fetched now (ProofAncestor), exactly proof's head when the forge's merge commit is on the base
// branch (ProofMerged), or a commit every one of whose ancestors is on a ref of the remote
// (ProofPushed). A slot never made ready for the run is given back as it is (ProofUnprepared): the
// run's work never started in it. Otherwise the slot is quarantined, its work untouched, and
// Release returns ErrUnlanded: with why, or QuarantinedUnchecked when the remote could not be
// fetched or the worktree read (unless ctx ended, which leaves the slot as it was); a later Release of the quarantined slot, or the next fresh
// preparation of the repository, gives it back once its work has landed.
func (p *Pool) Release(ctx context.Context, repo Repo, slot int, run string, proof Proof) error {
	if err := p.check(repo); err != nil {
		return err
	}
	if proof != (Proof{}) && (!objectID(proof.Head) || !objectID(proof.Merge)) {
		return fmt.Errorf("%w: proof %+v names no commits", ErrInvalid, proof)
	}
	defer p.lock(repo)()
	s, err := scanSlot(p.Store.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND slot = ?`,
		repo.Org, repo.Name, slot))
	if err != nil {
		return err
	}
	if s.State != StateLeased && s.State != StateQuarantined || s.Run != run {
		return fmt.Errorf("%w: slot %d of %s/%s", ErrNotHeld, slot, repo.Org, repo.Name)
	}
	if s.State == StateLeased && s.BaseSHA == "" {
		return p.settle(ctx, repo, s, Released{Slot: slot, Run: run, Proof: ProofUnprepared})
	}
	v, err := p.fetchVerdict(ctx, repo, s, proof)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		v = verdict{head: s.Head, reason: QuarantinedUnchecked}
	}
	if v.proof != "" {
		return p.settle(ctx, repo, s, Released{Slot: slot, Run: run, Head: v.head, Proof: v.proof, Merge: v.merge})
	}
	why := fmt.Errorf("%w: slot %d: %s", ErrUnlanded, slot, v.reason)
	if err != nil {
		why = errors.Join(why, err)
	}
	if s.State == StateQuarantined && s.Head == v.head && s.Reason == v.reason {
		return why
	}
	if err := p.settle(ctx, repo, s, Quarantined{Slot: slot, Run: run, Head: v.head, Reason: v.reason}); err != nil {
		return errors.Join(why, err)
	}
	return why
}

// fetchVerdict fetches the remote and reads whether s's work has landed.
func (p *Pool) fetchVerdict(ctx context.Context, repo Repo, s SlotInfo, proof Proof) (verdict, error) {
	if _, err := p.git().run(ctx, p.basePath(repo), "fetch", "--prune", "--quiet", "origin"); err != nil {
		return verdict{}, err
	}
	return p.verdict(ctx, s, proof)
}

// settle appends ev for slot s, refused (ErrNotHeld) when the slot changed since s was read.
func (p *Pool) settle(ctx context.Context, repo Repo, s SlotInfo, ev eventstore.Payload) error {
	return p.Store.Update(ctx, func(tx *eventstore.Tx) error {
		cur, err := scanSlot(tx.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND slot = ?`,
			repo.Org, repo.Name, s.Slot))
		if err != nil {
			return err
		}
		if cur.Version != s.Version {
			return fmt.Errorf("%w: slot %d changed while its release was checked", ErrNotHeld, s.Slot)
		}
		_, err = tx.Append(ctx, Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon", ev)
		return err
	})
}

// verdict is whether a slot's work has landed, as read from its worktree against the remote's refs
// as last fetched: the proof that it has (empty when it has not, with the reason), and its head.
type verdict struct {
	proof, merge, head, reason string
}

func (p *Pool) verdict(ctx context.Context, s SlotInfo, proof Proof) (verdict, error) {
	g := p.git()
	head, err := g.run(ctx, s.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return verdict{}, err
	}
	dirty, err := g.run(ctx, s.Path, "status", "--porcelain", "--untracked-files=all", "--ignored=no")
	if err != nil {
		return verdict{}, err
	}
	v := verdict{head: head}
	if dirty != "" {
		v.reason = QuarantinedUncommitted
		return v, nil
	}
	baseRef := "refs/remotes/origin/" + s.Base
	onBase, err := g.isAncestor(ctx, s.Path, head, baseRef)
	switch {
	case err != nil:
		return verdict{}, err
	case head == s.BaseSHA:
		v.proof = ProofEmpty
		return v, nil
	case onBase:
		v.proof = ProofAncestor
		return v, nil
	case proof.Head == head:
		_, err := g.run(ctx, s.Path, "rev-parse", "--verify", "--quiet", "--end-of-options", proof.Merge+"^{commit}")
		var ee *exec.ExitError
		switch {
		case ctx.Err() != nil:
			return verdict{}, ctx.Err()
		case errors.As(err, &ee) && ee.ExitCode() == 1:
		case err != nil:
			return verdict{}, err
		default:
			merged, err := g.isAncestor(ctx, s.Path, proof.Merge, baseRef)
			if ctx.Err() != nil {
				return verdict{}, ctx.Err()
			}
			if err != nil {
				return verdict{}, err
			}
			if merged {
				v.proof, v.merge = ProofMerged, proof.Merge
				return v, nil
			}
		}
	}
	unpushed, err := g.run(ctx, s.Path, "rev-list", "--max-count=1", head, "--not", "--remotes=origin")
	if err != nil {
		return verdict{}, err
	}
	if unpushed == "" {
		v.proof = ProofPushed
		return v, nil
	}
	v.reason = QuarantinedUnpushed
	return v, nil
}

// Discard removes a quarantined or broken slot of org's repo for the owner (by). It records the
// discard first (Discarded: by whom, the slot, its run, branch and head), then removes the slot's
// directory through the path guard, the pool's root its one root, and prunes base.git's record of
// it. The run's branch stays in base.git, so its commits are recoverable until git gc. A free or
// leased slot is refused (ErrInUse); a discarded one is discarded again, recorded again, so a
// removal that failed is retried. The slot's number is never used again.
func (p *Pool) Discard(ctx context.Context, org, repo string, slot int, by string) (SlotInfo, error) {
	r := Repo{Org: org, Name: repo}
	if !label(org, false) || !label(repo, true) {
		return SlotInfo{}, fmt.Errorf("%w: org %q or repository %q", ErrInvalid, org, repo)
	}
	if err := p.checkRoot(); err != nil {
		return SlotInfo{}, err
	}
	defer p.lock(r)()
	s, err := scanSlot(p.Store.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND slot = ?`,
		org, repo, slot))
	if err != nil {
		return SlotInfo{}, err
	}
	if s.State != StateQuarantined && s.State != StateBroken && s.State != StateDiscarded {
		return SlotInfo{}, fmt.Errorf("%w: slot %d of %s/%s is %s", ErrInUse, slot, org, repo, s.State)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return SlotInfo{}, err
	}
	guard, err := pathguard.New(home, pathguard.Root{Kind: pathguard.Project, Path: p.Root})
	if err != nil {
		return SlotInfo{}, err
	}
	if err := p.settle(ctx, r, s, Discarded{Slot: slot, Run: s.Run, Branch: s.Branch, Head: s.Head, By: by}); err != nil {
		return SlotInfo{}, err
	}
	// Spell the slot's directory under the guard's symlink-resolved root (on macOS /var is a
	// symlink to /private/var), as the guard requires of the path it is asked to delete.
	dir := filepath.Join(guard.Roots()[0].Path, org, repo, "slot-"+strconv.Itoa(slot))
	if _, err := os.Lstat(dir); err == nil {
		if err := guard.RemoveAll(dir); err != nil {
			return SlotInfo{}, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return SlotInfo{}, err
	}
	if baseDir := p.basePath(r); isDir(baseDir) {
		if _, err := p.git().run(ctx, baseDir, "worktree", "prune"); err != nil {
			return SlotInfo{}, err
		}
	}
	s.State = StateDiscarded
	return s, nil
}

func isDir(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}

// RunSlot returns the slot of repo that run held last, in whatever state it is now, or ErrNotHeld.
func (p *Pool) RunSlot(ctx context.Context, repo Repo, run string) (SlotInfo, error) {
	return scanSlot(p.Store.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND run = ?
		ORDER BY version DESC LIMIT 1`, repo.Org, repo.Name, run))
}

// List returns every slot of every repository, of org only when it is not empty, by org,
// repository and number.
func (p *Pool) List(ctx context.Context, org string) ([]SlotInfo, error) {
	return p.query(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE ? = '' OR org = ? ORDER BY org, repo, slot`, org, org)
}

// Held returns the slot of repo that run holds leased, or ErrNotHeld.
func (p *Pool) Held(ctx context.Context, repo Repo, run string) (SlotInfo, error) {
	return scanSlot(p.Store.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND state = ? AND run = ?
		ORDER BY slot LIMIT 1`, repo.Org, repo.Name, StateLeased, run))
}

// Slots returns org's repo's slots, by number.
func (p *Pool) Slots(ctx context.Context, org, repo string) ([]SlotInfo, error) {
	return p.query(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? ORDER BY slot`, org, repo)
}

func (p *Pool) query(ctx context.Context, q string, args ...any) ([]SlotInfo, error) {
	rows, err := p.Store.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []SlotInfo
	for rows.Next() {
		s, err := scanSlot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Pool) basePath(repo Repo) string {
	return filepath.Join(p.Root, repo.Org, repo.Name, "base.git")
}

// baseClone returns the repository's bare clone, making it on first use: a bare repository whose
// origin is the remote, fetched into refs/remotes/origin. A clone left with no origin (making it
// stopped before the remote was added) is given its origin while it holds no refs; an existing
// clone of another remote, or one with refs and no origin, is refused.
func (p *Pool) baseClone(ctx context.Context, repo Repo) (string, error) {
	dir := p.basePath(repo)
	g := p.git()
	steps := [][]string{
		{"-C", dir, "remote", "add", "origin", "--", repo.Remote},
		{"-C", dir, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
	}
	if fi, err := os.Lstat(dir); err == nil {
		if !fi.IsDir() {
			return "", fmt.Errorf("%w: %s is not a directory", ErrInvalid, dir)
		}
		url, err := g.run(ctx, dir, "config", "--get", "remote.origin.url")
		var ee *exec.ExitError
		switch {
		case errors.As(err, &ee) && ee.ExitCode() == 1:
			refs, err := g.run(ctx, dir, "for-each-ref", "--count=1")
			if err != nil {
				return "", err
			}
			if refs != "" {
				return "", fmt.Errorf("%w: %s/%s's clone has refs and no origin", ErrInvalid, repo.Org, repo.Name)
			}
		case err != nil:
			return "", err
		case url != repo.Remote:
			return "", fmt.Errorf("%w: %s/%s is a clone of %q, not %q", ErrInvalid, repo.Org, repo.Name, url, repo.Remote)
		default:
			return dir, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	} else {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return "", err
		}
		steps = append([][]string{{"init", "--quiet", "--bare", "--", dir}}, steps...)
	}
	for _, args := range steps {
		if _, err := g.run(ctx, p.Root, args...); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// check validates repo and makes the pool's root.
func (p *Pool) check(repo Repo) error {
	if err := checkRepo(repo); err != nil {
		return err
	}
	return p.checkRoot()
}

// checkRepo refuses a malformed org, repository name or remote.
func checkRepo(repo Repo) error {
	if !label(repo.Org, false) || !label(repo.Name, true) {
		return fmt.Errorf("%w: org %q or repository %q", ErrInvalid, repo.Org, repo.Name)
	}
	if repo.Remote == "" || strings.HasPrefix(repo.Remote, "-") || strings.Contains(repo.Remote, "::") {
		return fmt.Errorf("%w: remote %q", ErrInvalid, repo.Remote)
	}
	return nil
}

// checkRoot makes the pool's root, refusing one that is not absolute and clean or that others can
// open.
func (p *Pool) checkRoot() error {
	if !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root {
		return fmt.Errorf("%w: the pool's root %q is not absolute and clean", ErrInvalid, p.Root)
	}
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(p.Root)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: the pool's root %s must be a directory only its owner can open (it is %v)", ErrInvalid, p.Root, fi.Mode())
	}
	return nil
}

// checkBase refuses a base that is not a plain branch name, by git's rules for one
// (git check-ref-format --branch), so it is checked without running git.
func checkBase(base string) error {
	bad := base == "" || base == "@" || len(base) > 200 || strings.HasPrefix(base, "-") || strings.HasPrefix(base, "/") ||
		strings.HasSuffix(base, "/") || strings.HasSuffix(base, ".") || strings.Contains(base, "..") ||
		strings.Contains(base, "//") || strings.Contains(base, "@{") || strings.ContainsAny(base, " ~^:?*[\\")
	for i := 0; i < len(base) && !bad; i++ {
		bad = base[i] < 0x20 || base[i] == 0x7f
	}
	for _, c := range strings.Split(base, "/") {
		bad = bad || strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".lock")
	}
	if bad {
		return fmt.Errorf("%w: base %q is not a branch name", ErrInvalid, base)
	}
	return nil
}

// label reports whether s is 1-63 bytes of a-z, 0-9 and '-' (with dots, also '.' and '_'),
// starting with a letter or digit.
func label(s string, dots bool) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || dots && (c == '.' || c == '_'))
		if !ok {
			return false
		}
	}
	return !strings.Contains(s, "..")
}

// branchFor is the branch a run works on: lucy/, the run id with anything but a-z, A-Z, 0-9, '.',
// '_' and '-' made '-', then '-' and the first 8 hex digits of the SHA-256 of the run id, so two
// runs never share a branch. A run id that would make no branch name is refused.
func branchFor(run string) (string, error) {
	if run == "" || len(run) > 200 {
		return "", fmt.Errorf("%w: run %q", ErrInvalid, run)
	}
	b := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '-'
	}, run)
	b = strings.Trim(b, "-")
	if b == "" || strings.HasPrefix(b, ".") || strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock") || strings.Contains(b, "..") {
		return "", fmt.Errorf("%w: run %q names no branch", ErrInvalid, run)
	}
	sum := sha256.Sum256([]byte(run))
	return "lucy/" + b + "-" + hex.EncodeToString(sum[:4]), nil
}

// objectID reports whether s is a full git object id: 40 or 64 lowercase hex digits.
func objectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func capReason(s string) string {
	if len(s) > 512 {
		return s[:512]
	}
	return s
}
