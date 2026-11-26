package sim

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Result is how a check run ends.
type Result string

// Check results.
const (
	Pass  Result = "pass"
	Fail  Result = "fail"
	Flake Result = "flake" // fails, is re-run automatically and passes
)

// Check is one check a pull request runs on every head: it reports Result After it starts.
type Check struct {
	Name   string
	After  time.Duration
	Result Result
}

// Passing, Failing and Flaky are checks that report after d.
func Passing(name string, d time.Duration) Check { return Check{name, d, Pass} }
func Failing(name string, d time.Duration) Check { return Check{name, d, Fail} }
func Flaky(name string, d time.Duration) Check   { return Check{name, d, Flake} }

// Merge is a merge the fake forge performed.
type Merge struct {
	Repo     string
	PR       int
	HeadSHA  string
	MergeSHA string
	By       string // the forge account that merged
	Time     time.Time
}

// Forge is a fake GitHub-like forge: repositories with branches, pull requests whose checks run
// on virtual time, and merges. It implements tower.Forge; the rest of its methods are what Pilots
// and scenarios do to it.
type Forge struct {
	// Identity is the account Merge merges as: lucy's, DefaultIdentity; a World's Tower is
	// opened with it as its ForgeIdentity.
	Identity string
	// HideMerger makes the forge report no merging account, as a forge that cannot say.
	HideMerger bool

	clock  *Clock
	rng    *rand.Rand
	repos  map[string]*repo
	merges []Merge
	calls  int
}

type repo struct {
	branches map[string]string // branch -> head SHA
	required []string          // check names the base branch requires
	prs      map[int]*pullRequest
	next     int
}

type pullRequest struct {
	number    int
	branch    string
	base      string
	head      string
	checks    []Check
	started   time.Time // when the checks on head started
	conflict  bool
	merged    bool
	mergeSHA  string
	mergedBy  string
	loseReply bool
	failMerge int   // merges still to fail before they happen; -1 for every one
	failErr   error // what they fail with
}

// DefaultIdentity is the forge account lucy merges as in a World.
const DefaultIdentity = "lucy[bot]"

func newForge(clock *Clock, rng *rand.Rand) *Forge {
	return &Forge{Identity: DefaultIdentity, clock: clock, rng: rng, repos: map[string]*repo{}}
}

func (f *Forge) sha() string {
	return fmt.Sprintf("%016x%016x%08x", f.rng.Uint64(), f.rng.Uint64(), f.rng.Uint32())
}

// AddRepo creates a repository ("org/project") with a main branch, requiring the named checks.
// Opening a pull request creates its repository when it does not exist.
func (f *Forge) AddRepo(name string, required ...string) {
	if _, ok := f.repos[name]; !ok {
		f.repos[name] = &repo{branches: map[string]string{"main": f.sha()}, prs: map[int]*pullRequest{}, next: 1}
	}
	f.repos[name].required = required
}

func (f *Forge) pr(name string, number int) (*repo, *pullRequest, error) {
	r, ok := f.repos[name]
	if !ok {
		return nil, nil, fmt.Errorf("forge: no repository %q", name)
	}
	p, ok := r.prs[number]
	if !ok {
		return nil, nil, fmt.Errorf("forge: no pull request %s#%d", name, number)
	}
	return r, p, nil
}

// OpenPR pushes branch and opens a pull request from it onto base, with checks starting at at.
func (f *Forge) OpenPR(name, branch, base string, checks []Check, at time.Time) int {
	f.AddRepo(name, f.required(name)...)
	r := f.repos[name]
	if _, ok := r.branches[base]; !ok {
		r.branches[base] = f.sha()
	}
	head := f.sha()
	r.branches[branch] = head
	n := r.next
	r.next++
	r.prs[n] = &pullRequest{number: n, branch: branch, base: base, head: head, checks: checks, started: at}
	return n
}

func (f *Forge) required(name string) []string {
	if r, ok := f.repos[name]; ok {
		return r.required
	}
	return nil
}

// Push is someone else pushing a new commit to the pull request's branch: a new head, and its
// checks run again from now.
func (f *Forge) Push(name string, number int) (string, error) {
	r, p, err := f.pr(name, number)
	if err != nil {
		return "", err
	}
	p.head, p.started = f.sha(), f.clock.Now()
	r.branches[p.branch] = p.head
	return p.head, nil
}

// MoveBase is someone else landing a change on the base branch: its SHA changes.
func (f *Forge) MoveBase(name, base string) (string, error) {
	r, ok := f.repos[name]
	if !ok {
		return "", fmt.Errorf("forge: no repository %q", name)
	}
	r.branches[base] = f.sha()
	return r.branches[base], nil
}

// SetConflict makes the pull request merge cleanly or not.
func (f *Forge) SetConflict(name string, number int, conflict bool) error {
	_, p, err := f.pr(name, number)
	if err == nil {
		p.conflict = conflict
	}
	return err
}

// LoseMergeReply is a fault: the next merge of the pull request happens, but its caller gets an
// error instead of the reply, as when the connection or the daemon dies mid-merge.
func (f *Forge) LoseMergeReply(name string, number int) error {
	_, p, err := f.pr(name, number)
	if err == nil {
		p.loseReply = true
	}
	return err
}

// FailMerge is a fault: the next times merges of the pull request (every one when times is -1)
// fail with mergeErr before they happen, as when the forge answers 5xx, or refuses with a 405
// from branch protection or a 403 for a missing token scope.
func (f *Forge) FailMerge(name string, number, times int, mergeErr error) error {
	_, p, err := f.pr(name, number)
	if err == nil {
		p.failMerge, p.failErr = times, mergeErr
	}
	return err
}

// MergeCalls returns how many times Merge was called.
func (f *Forge) MergeCalls() int { return f.calls }

// Merges returns every merge the forge performed, in order.
func (f *Forge) Merges() []Merge { return append([]Merge(nil), f.merges...) }

// checkState is one check's state now: pending, or its latest result.
func (p *pullRequest) checkState(c Check, now time.Time) policy.CheckState {
	done := p.started.Add(c.After)
	switch {
	case now.Before(done):
		return policy.ChecksPending
	case c.Result == Pass:
		return policy.ChecksGreen
	case c.Result == Fail:
		return policy.ChecksRed
	case now.Before(done.Add(c.After)): // a flake: red until its re-run reports
		return policy.ChecksRed
	}
	return policy.ChecksGreen
}

// PullRequest implements tower.Forge. A required check that never reported leaves Checks the
// state of the checks that did, with RequiredUnreported set, as GitHub reports one that never
// started.
func (f *Forge) PullRequest(_ context.Context, name string, number int) (tower.PullRequest, error) {
	r, p, err := f.pr(name, number)
	if err != nil {
		return tower.PullRequest{}, err
	}
	now := f.clock.Now()
	out := tower.PullRequest{Number: p.number, HeadSHA: p.head, BaseRef: p.base, BaseSHA: r.branches[p.base],
		Conflict: p.conflict, Merged: p.merged, MergeSHA: p.mergeSHA}
	if !f.HideMerger {
		out.MergedBy = p.mergedBy
	}
	reported := map[string]bool{}
	var red, pending bool
	for _, c := range p.checks {
		reported[c.Name] = true
		switch p.checkState(c, now) {
		case policy.ChecksRed:
			red = true
		case policy.ChecksPending:
			pending = true
		}
	}
	for _, name := range r.required {
		if !reported[name] {
			out.RequiredUnreported = true
		}
	}
	switch {
	case red:
		out.Checks = policy.ChecksRed
	case pending:
		out.Checks = policy.ChecksPending
	case len(p.checks) > 0:
		out.Checks = policy.ChecksGreen
	}
	return out, nil
}

// errReplyLost is what a merge whose reply was lost returns.
var errReplyLost = errors.New("forge: the connection closed before the merge's reply")

// Merge implements tower.Forge: it merges only the head it is given, and only cleanly.
func (f *Forge) Merge(_ context.Context, name string, number int, head string) (string, error) {
	f.calls++
	r, p, err := f.pr(name, number)
	if err != nil {
		return "", err
	}
	switch {
	case p.merged:
		return "", fmt.Errorf("forge: %s#%d is already merged", name, number)
	case p.head != head:
		return "", tower.ErrHeadMoved
	case p.conflict:
		return "", tower.ErrConflict
	case p.failMerge != 0:
		if p.failMerge > 0 {
			p.failMerge--
		}
		return "", p.failErr
	}
	f.land(name, r, p, f.Identity)
	if p.loseReply {
		p.loseReply = false
		return "", errReplyLost
	}
	return p.mergeSHA, nil
}

// MergeAs is someone else merging the pull request on the forge, at its current head, as account.
func (f *Forge) MergeAs(name string, number int, account string) error {
	r, p, err := f.pr(name, number)
	if err != nil {
		return err
	}
	if p.merged {
		return fmt.Errorf("forge: %s#%d is already merged", name, number)
	}
	f.land(name, r, p, account)
	return nil
}

func (f *Forge) land(name string, r *repo, p *pullRequest, account string) {
	p.merged, p.mergeSHA, p.mergedBy = true, f.sha(), account
	r.branches[p.base] = p.mergeSHA
	f.merges = append(f.merges, Merge{Repo: name, PR: p.number, HeadSHA: p.head, MergeSHA: p.mergeSHA, By: account, Time: f.clock.Now()})
}
