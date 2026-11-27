// Package worktree is lucy's built-in worktree pool (LUCY-RUN-007): per org and repository, a set
// of git worktrees (slots) that runs lease, recorded in the event store. A slot is reset to the
// run's base when it is leased, and given back only with proof that the work in it landed, so
// unlanded work is never torn down.
//
// Layout, under the pool's root (0700):
//
//	<org>/<repo>/base.git   a bare clone of the repository's remote, fetched before every lease and release
//	<org>/<repo>/slot-<n>   slot n: a worktree of base.git
//
// The pool runs git with an environment of its own (no user or system configuration, no prompts,
// only the https, ssh and file transports). It deletes no directory of its own. What it lets git
// remove is base.git's stale worktree registrations (git worktree prune) and, when a free slot is
// reset for a new lease, that slot's tracked changes and its untracked and ignored files
// (checkout --force, reset --hard, clean -ffdx). That is safe only because a slot becomes free
// only after Release has proven its work landed.
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

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// DefaultMax is how many slots a repository's pool holds when Pool.Max is zero.
const DefaultMax = 4

var (
	// ErrUnlanded refuses a release whose work has not landed: uncommitted changes, or commits
	// on no base branch and in no merge the forge reported. The slot stays leased.
	ErrUnlanded = errors.New("worktree: the slot's work has not landed")
	// ErrBusy refuses a lease when every slot of the repository is leased or broken and the pool
	// is at its size.
	ErrBusy = errors.New("worktree: every slot of the repository is in use")
	// ErrNotHeld refuses a release of a slot the run does not hold.
	ErrNotHeld = errors.New("worktree: the run does not hold this slot")
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
// two runs ever share a slot. A Pool is safe for concurrent use: it runs one lease or release of a
// repository at a time, so the git steps on its base.git never race. One Pool per store is the
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
}

// Acquire leases run a slot of repo, reset to base as fetched from the remote: a free slot when
// there is one, else a new one while the pool is under its size (ErrBusy beyond). A run that holds
// a slot of repo already (a retry, or a restart after the lease was recorded) gets that lease
// back, its worktree made again or reset only when that loses nothing. A slot that cannot be reset
// or made is recorded as broken and the error returned.
func (p *Pool) Acquire(ctx context.Context, repo Repo, run, base string) (Lease, error) {
	if err := p.check(repo); err != nil {
		return Lease{}, err
	}
	branch, err := branchFor(run)
	if err != nil {
		return Lease{}, err
	}
	defer p.lock(repo)()
	g := p.git()
	baseDir, err := p.baseClone(ctx, repo)
	if err != nil {
		return Lease{}, err
	}
	if err := checkBase(ctx, g, baseDir, base); err != nil {
		return Lease{}, err
	}
	if _, err := g.run(ctx, baseDir, "worktree", "prune"); err != nil {
		return Lease{}, err
	}
	held, err := scanSlot(p.Store.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND state = ? AND run = ?
		ORDER BY slot LIMIT 1`, repo.Org, repo.Name, StateLeased, run))
	switch {
	case err == nil:
		if held.Base != base {
			return Lease{}, fmt.Errorf("%w: run %q holds slot %d of %s/%s on base %q, not %q", ErrInvalid, run, held.Slot, repo.Org, repo.Name, held.Base, base)
		}
		l := Lease{Slot: held.Slot, Path: held.Path, Branch: held.Branch, BaseSHA: held.BaseSHA}
		how, err := p.heldPrep(ctx, baseDir, l)
		if err != nil {
			return Lease{}, err
		}
		if how == keepSlot {
			return l, nil
		}
		return p.prepare(ctx, repo, baseDir, l, how)
	case !errors.Is(err, ErrNotHeld):
		return Lease{}, err
	}
	if _, err := g.run(ctx, baseDir, "fetch", "--prune", "--quiet", "origin"); err != nil {
		return Lease{}, err
	}
	baseSHA, err := g.run(ctx, baseDir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+base+"^{commit}")
	if err != nil {
		return Lease{}, fmt.Errorf("%w: base %q is not a branch of the remote", ErrInvalid, base)
	}
	tip, err := branchTip(ctx, g, baseDir, branch)
	if err != nil {
		return Lease{}, err
	}
	if tip != "" && tip != baseSHA {
		landed, err := g.isAncestor(ctx, baseDir, tip, "refs/remotes/origin/"+base)
		if err != nil {
			return Lease{}, err
		}
		if !landed {
			return Lease{}, fmt.Errorf("%w: run %q's branch %s holds commits that have not landed on %q", ErrUnlanded, run, branch, base)
		}
	}
	var l Lease
	how := resetSlot
	err = p.Store.Update(ctx, func(tx *eventstore.Tx) error {
		s, err := scanSlot(tx.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND state = ?
			ORDER BY slot LIMIT 1`, repo.Org, repo.Name, StateFree))
		if errors.Is(err, ErrNotHeld) {
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_slots WHERE org = ? AND repo = ?`, repo.Org, repo.Name).Scan(&n); err != nil {
				return err
			}
			if n >= p.max() {
				return ErrBusy
			}
			s = SlotInfo{Slot: n + 1, Path: filepath.Join(p.Root, repo.Org, repo.Name, "slot-"+strconv.Itoa(n+1))}
			how = newSlot
			if _, err := tx.Append(ctx, Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon",
				Added{Org: repo.Org, Repo: repo.Name, Slot: s.Slot, Path: s.Path}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		l = Lease{Slot: s.Slot, Path: s.Path, Branch: branch, BaseSHA: baseSHA}
		_, err = tx.Append(ctx, Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon",
			Leased{Slot: s.Slot, Run: run, Branch: branch, Base: base, BaseSHA: baseSHA})
		return err
	})
	if err != nil {
		return Lease{}, err
	}
	return p.prepare(ctx, repo, baseDir, l, how)
}

// How a leased slot's worktree is prepared.
const (
	resetSlot      = iota // put a free slot\'s worktree back at the base, removing everything else
	newSlot               // make the worktree, in a directory that must not exist yet
	remakeSlot            // make a held slot's worktree again: it is missing, or not one of base.git
	keepSlot              // leave a held slot as it is: it may hold the run's work, or is ready already
	rebranchSlot          // put a held slot, clean at its base, on the run's branch, keeping every file
	remakeOnBranch        // remakeSlot, on the run's branch, which is ahead of the base: never moved
	checkoutBranch        // put a held slot's clean worktree on the run's branch, ahead of the base: never moved
)

// prepare readies l's worktree as how says and returns l, or records the slot as broken when that
// fails. The record outlives ctx: a lease cancelled halfway still leaves its slot accounted for.
func (p *Pool) prepare(ctx context.Context, repo Repo, baseDir string, l Lease, how int) (Lease, error) {
	if err := p.reset(ctx, baseDir, l, how); err != nil {
		if _, berr := p.Store.Append(context.WithoutCancel(ctx), Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon",
			Broken{Slot: l.Slot, Reason: capReason(err.Error())}); berr != nil {
			return Lease{}, errors.Join(err, berr)
		}
		return Lease{}, err
	}
	return l, nil
}

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
	switch how {
	case remakeOnBranch:
		_, err := g.run(ctx, baseDir, "worktree", "add", "--quiet", "--force", "--", l.Path, l.Branch)
		return err
	case checkoutBranch:
		_, err := g.run(ctx, l.Path, "checkout", "--quiet", l.Branch)
		return err
	case rebranchSlot:
	case newSlot, remakeSlot:
		if how == newSlot {
			if _, err := os.Lstat(l.Path); !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("worktree: slot %d's directory %s exists already, and the pool did not make it", l.Slot, l.Path)
			}
		}
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

// Release gives run's slot back. It refuses (ErrUnlanded), and the slot stays leased, unless the
// slot's worktree has no uncommitted change and its head is landed: the base it was leased at, or
// a commit on the base branch as fetched now, or exactly proof's head when the forge's merge
// commit is on the base branch.
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
	if s.State != StateLeased || s.Run != run {
		return fmt.Errorf("%w: slot %d of %s/%s", ErrNotHeld, slot, repo.Org, repo.Name)
	}
	g := p.git()
	baseDir := p.basePath(repo)
	if _, err := g.run(ctx, baseDir, "fetch", "--prune", "--quiet", "origin"); err != nil {
		return err
	}
	head, err := g.run(ctx, s.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	dirty, err := g.run(ctx, s.Path, "status", "--porcelain", "--untracked-files=all", "--ignored=no")
	if err != nil {
		return err
	}
	if dirty != "" {
		return p.refuse(ctx, repo, slot, run, head, RefusedUncommitted,
			fmt.Errorf("%w: slot %d has uncommitted changes", ErrUnlanded, slot))
	}
	baseRef := "refs/remotes/origin/" + s.Base
	how, merge := "", ""
	switch onBase, err := g.isAncestor(ctx, s.Path, head, baseRef); {
	case err != nil:
		return err
	case head == s.BaseSHA:
		how = ProofEmpty
	case onBase:
		how = ProofAncestor
	case proof.Head == head:
		_, err := g.run(ctx, s.Path, "rev-parse", "--verify", "--quiet", "--end-of-options", proof.Merge+"^{commit}")
		var ee *exec.ExitError
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.As(err, &ee) && ee.ExitCode() == 1:
			return p.refuse(ctx, repo, slot, run, head, RefusedNotLanded,
				fmt.Errorf("%w: the merge commit %s is unknown", ErrUnlanded, proof.Merge))
		case err != nil:
			return err
		}
		merged, err := g.isAncestor(ctx, s.Path, proof.Merge, baseRef)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if merged {
			how, merge = ProofMerged, proof.Merge
		}
	}
	if how == "" {
		return p.refuse(ctx, repo, slot, run, head, RefusedNotLanded,
			fmt.Errorf("%w: slot %d's head %s is on no base branch and in no merge the forge reported", ErrUnlanded, slot, head))
	}
	return p.Store.Update(ctx, func(tx *eventstore.Tx) error {
		cur, err := scanSlot(tx.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND slot = ?`,
			repo.Org, repo.Name, slot))
		if err != nil {
			return err
		}
		if cur.Version != s.Version {
			return fmt.Errorf("%w: slot %d changed while its release was checked", ErrNotHeld, slot)
		}
		_, err = tx.Append(ctx, Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon",
			Released{Slot: slot, Run: run, Head: head, Proof: how, Merge: merge})
		return err
	})
}

// refuse records why run's release of slot was refused and returns why; the slot stays leased.
func (p *Pool) refuse(ctx context.Context, repo Repo, slot int, run, head, reason string, why error) error {
	if _, err := p.Store.Append(context.WithoutCancel(ctx), Stream(repo.Org, repo.Name), eventstore.AnyVersion, "daemon",
		ReleaseRefused{Slot: slot, Run: run, Head: head, Reason: reason}); err != nil {
		return errors.Join(why, err)
	}
	return why
}

// Held returns the slot of repo that run holds leased, or ErrNotHeld.
func (p *Pool) Held(ctx context.Context, repo Repo, run string) (SlotInfo, error) {
	return scanSlot(p.Store.QueryRowContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? AND state = ? AND run = ?
		ORDER BY slot LIMIT 1`, repo.Org, repo.Name, StateLeased, run))
}

// Slots returns org's repo's slots, by number.
func (p *Pool) Slots(ctx context.Context, org, repo string) ([]SlotInfo, error) {
	rows, err := p.Store.QueryContext(ctx, `SELECT `+slotColumns+` FROM worktree_slots WHERE org = ? AND repo = ? ORDER BY slot`, org, repo)
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
	if !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root {
		return fmt.Errorf("%w: the pool's root %q is not absolute and clean", ErrInvalid, p.Root)
	}
	if !label(repo.Org, false) || !label(repo.Name, true) {
		return fmt.Errorf("%w: org %q or repository %q", ErrInvalid, repo.Org, repo.Name)
	}
	if repo.Remote == "" || strings.HasPrefix(repo.Remote, "-") || strings.Contains(repo.Remote, "::") {
		return fmt.Errorf("%w: remote %q", ErrInvalid, repo.Remote)
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

// checkBase refuses a base that is not a plain branch name.
func checkBase(ctx context.Context, g git, dir, base string) error {
	if base == "" || strings.HasPrefix(base, "-") || len(base) > 200 {
		return fmt.Errorf("%w: base %q", ErrInvalid, base)
	}
	if _, err := g.run(ctx, dir, "check-ref-format", "--branch", base); err != nil {
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
