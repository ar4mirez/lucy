package worktree

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/spec"
)

// DefaultPrepareTimeout bounds one background preparation or release when Source.Timeout is zero.
const DefaultPrepareTimeout = 30 * time.Minute

// Source gives each run its slot, from the org's Runway resources: a run of a project whose Runway
// names a repository works in a slot of that repository's pool (LUCY-RUN-007). The daemon reserves
// the slot when it leases the run, without running git, and prepares it in the background; when
// the run finishes, the daemon gives the slot back in the background, or quarantines it. The git
// work runs under Context, not under any API call's deadline.
type Source struct {
	Pool *Pool
	// Remote names the remote of a Runway's repository (owner/name on the forge):
	// https://github.com/<owner>/<name>.git when nil.
	Remote func(repository string) string
	// Gate, when set, is applied to every remote before a slot of it is reserved: a remote it
	// refuses gives the run no checkout, and nothing is reserved or cloned.
	Gate func(remote string) error
	// Context is the daemon's: every background preparation and release runs under it, and stops
	// only when it ends. context.Background when nil.
	Context context.Context
	// Timeout bounds one background preparation or release; DefaultPrepareTimeout when zero.
	Timeout time.Duration
	Log     *slog.Logger // slog.Default when nil

	runways map[string]runway // by org/project
	wg      sync.WaitGroup
}

type runway struct {
	repo string // owner/name
	base string // the branch changes land on
}

// SetRunways replaces the Runways the Source places runs by: every admitted lucy Runway with a
// repository, keyed by its namespace (the org) and name (the project).
func (s *Source) SetRunways(rs []spec.Resource) {
	m := map[string]runway{}
	for _, r := range rs {
		if r.APIVersion != spec.LucyAPIVersion || r.Kind != "Runway" {
			continue
		}
		repo, _ := r.Spec["repository"].(string)
		if repo == "" {
			continue
		}
		base, _ := r.Spec["defaultBranch"].(string)
		if base == "" {
			base = "main"
		}
		m[r.Metadata.Namespace+"/"+r.Metadata.Name] = runway{repo: repo, base: base}
	}
	s.runways = m
}

// repo is the pool repository of org's project, and whether its Runway names one.
func (s *Source) repo(org, project string) (Repo, runway, bool, error) {
	rw, ok := s.runways[org+"/"+project]
	if !ok {
		return Repo{}, runway{}, false, nil
	}
	repo, err := s.repoOf(org, rw)
	if err != nil {
		return Repo{}, runway{}, true, fmt.Errorf("Runway %s/%s: %w", org, project, err)
	}
	return repo, rw, true, nil
}

func (s *Source) repoOf(org string, rw runway) (Repo, error) {
	owner, name, ok := strings.Cut(rw.repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repo{}, fmt.Errorf("%w: the repository %q is not owner/name", ErrInvalid, rw.repo)
	}
	remote := "https://github.com/" + rw.repo + ".git"
	if s.Remote != nil {
		remote = s.Remote(rw.repo)
	}
	return Repo{Org: org, Name: strings.ToLower(name), Remote: remote}, nil
}

// RemoteOf returns the remote a run of org's project fetches, as Acquire resolves it, and
// whether the project's Runway names a repository at all.
func (s *Source) RemoteOf(org, project string) (remote string, ok bool, err error) {
	repo, _, ok, err := s.repo(org, project)
	return repo.Remote, ok, err
}

// repoNamed is org's pool repository name, from the org's Runways, and whether one names it.
func (s *Source) repoNamed(org, name string) (Repo, bool) {
	for key, rw := range s.runways {
		if o, _, _ := strings.Cut(key, "/"); o != org {
			continue
		}
		if repo, err := s.repoOf(org, rw); err == nil && repo.Name == name {
			return repo, true
		}
	}
	return Repo{}, false
}

// Acquire reserves run a slot of its project's repository for base (the Runway's default branch
// when empty), without running git, and starts preparing it in the background; the returned lease
// has no base commit yet (State says when it is ready), and its Deadline is when the preparation
// stops at the latest. ok is false, with no error, when the
// project's Runway names no repository: the run then has no checkout.
func (s *Source) Acquire(ctx context.Context, org, project, run, base string) (l Lease, ok bool, err error) {
	repo, rw, ok, err := s.repo(org, project)
	if !ok || err != nil {
		return Lease{}, ok, err
	}
	if s.Gate != nil {
		if err := s.Gate(repo.Remote); err != nil {
			return Lease{}, true, err
		}
	}
	if base == "" {
		base = rw.base
	}
	if l, err = s.Pool.Reserve(ctx, repo, run, base); err != nil {
		return Lease{}, true, err
	}
	l.Deadline = time.Now().Add(s.timeout())
	slot := l.Slot
	s.spawn("prepare", run, func(ctx context.Context) error {
		_, err := s.Pool.Prepare(ctx, repo, run, slot)
		return err
	})
	return l, true, nil
}

// State is how run's slot of its project's repository stands: PrepPreparing, PrepReady, or
// PrepFailed with why. It is empty when the project names no repository, or no slot is the run's.
func (s *Source) State(ctx context.Context, org, project, run string) (state, reason string, err error) {
	repo, _, ok, err := s.repo(org, project)
	if !ok || err != nil {
		return "", "", err
	}
	sl, err := s.Pool.RunSlot(ctx, repo, run)
	switch {
	case errors.Is(err, ErrNotHeld):
		return "", "", nil
	case err != nil:
		return "", "", err
	case sl.State == StateBroken, sl.Prep == PrepFailed:
		return PrepFailed, sl.Reason, nil
	case sl.State == StateLeased:
		return sl.Prep, "", nil
	}
	return "", "", nil
}

// Release gives run's slot back, in the background, once its work has landed (Pool.Release, with
// no forge proof); work that has not landed leaves the slot quarantined. A run that holds no slot
// releases nothing.
func (s *Source) Release(_ context.Context, org, project, run string) error {
	repo, _, ok, err := s.repo(org, project)
	if !ok || err != nil {
		return err
	}
	s.spawn("release", run, func(ctx context.Context) error { return s.release(ctx, repo, run) })
	return nil
}

func (s *Source) release(ctx context.Context, repo Repo, run string) error {
	slot, err := s.Pool.Held(ctx, repo, run)
	if errors.Is(err, ErrNotHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	err = s.Pool.Release(ctx, repo, slot.Slot, run, Proof{})
	if errors.Is(err, ErrUnlanded) || errors.Is(err, ErrNotHeld) {
		return nil
	}
	return err
}

// Recover runs once, as the daemon starts and before it leases anything: a slot left preparing by
// a daemon that stopped without recording how its preparation ended is recorded as stopped
// (PrepareStopped), so its run sees its worktree failed and a retry gets a fresh preparation; and
// the slot of each run that has finished (finished reports it) is released in the background, as
// a release cut short by the stop would have done.
func (s *Source) Recover(ctx context.Context, finished func(ctx context.Context, run string) (bool, error)) error {
	slots, err := s.Pool.List(ctx, "")
	if err != nil {
		return err
	}
	var errs []error
	for _, sl := range slots {
		if sl.State != StateLeased {
			continue
		}
		if sl.Prep == PrepPreparing {
			if err := s.Pool.record(ctx, Repo{Org: sl.Org, Name: sl.Repo}, nil,
				PrepareFailed{Slot: sl.Slot, Run: sl.Run, Reason: PrepareStopped}); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		done, err := finished(ctx, sl.Run)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		repo, ok := s.repoNamed(sl.Org, sl.Repo)
		if !done || !ok {
			continue
		}
		run := sl.Run
		s.spawn("release", run, func(ctx context.Context) error { return s.release(ctx, repo, run) })
	}
	return errors.Join(errs...)
}

// List returns every slot, of org only when it is not empty.
func (s *Source) List(ctx context.Context, org string) ([]SlotInfo, error) {
	return s.Pool.List(ctx, org)
}

// Discard discards a quarantined or broken slot for the owner (Pool.Discard).
func (s *Source) Discard(ctx context.Context, org, repo string, slot int, by string) (SlotInfo, error) {
	return s.Pool.Discard(ctx, org, repo, slot, by)
}

// Wait returns once every background preparation and release has ended: after Context has ended,
// each has recorded how it stopped. Call it before the store is closed, once no API call can start
// another.
func (s *Source) Wait() { s.wg.Wait() }

func (s *Source) timeout() time.Duration {
	if s.Timeout <= 0 {
		return DefaultPrepareTimeout
	}
	return s.Timeout
}

// spawn runs f in the background under Context, bounded by Timeout, and logs its error.
func (s *Source) spawn(what, run string, f func(ctx context.Context) error) {
	base := s.Context
	if base == nil {
		base = context.Background()
	}
	timeout := s.timeout()
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	s.wg.Go(func() {
		ctx, cancel := context.WithTimeout(base, timeout)
		defer cancel()
		if err := f(ctx); err != nil {
			log.Warn("worktree: could not "+what+" a run's slot", "run", run, "err", err)
		}
	})
}

// Checkout returns the worktree of the slot run holds of its project's repository, with every
// symlink resolved, as the runner resolves the paths of a Pilot's tools, and whether it holds one
// whose path resolves.
func (s *Source) Checkout(ctx context.Context, org, project, run string) (string, bool) {
	repo, _, ok, err := s.repo(org, project)
	if !ok || err != nil {
		return "", false
	}
	slot, err := s.Pool.Held(ctx, repo, run)
	if err != nil {
		return "", false
	}
	p, err := filepath.EvalSymlinks(slot.Path)
	if err != nil {
		return "", false
	}
	return p, true
}
