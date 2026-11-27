package worktree

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ar4mirez/lucy/internal/spec"
)

// Source gives each run its slot, from the org's Runway resources: a run of a project whose Runway
// names a repository works in a slot of that repository's pool (LUCY-RUN-007). The daemon leases
// the slot when it leases the run and gives it back when the run finishes.
type Source struct {
	Pool *Pool
	// Remote names the remote of a Runway's repository (owner/name on the forge):
	// https://github.com/<owner>/<name>.git when nil.
	Remote  func(repository string) string
	runways map[string]runway // by org/project
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
	owner, name, ok := strings.Cut(rw.repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repo{}, runway{}, true, fmt.Errorf("%w: Runway %s/%s's repository %q is not owner/name", ErrInvalid, org, project, rw.repo)
	}
	remote := "https://github.com/" + rw.repo + ".git"
	if s.Remote != nil {
		remote = s.Remote(rw.repo)
	}
	return Repo{Org: org, Name: strings.ToLower(name), Remote: remote}, rw, true, nil
}

// Acquire leases run a slot of its project's repository, reset to base (the Runway's default
// branch when empty). ok is false, with no error, when the project's Runway names no repository:
// the run then has no checkout.
func (s *Source) Acquire(ctx context.Context, org, project, run, base string) (l Lease, ok bool, err error) {
	repo, rw, ok, err := s.repo(org, project)
	if !ok || err != nil {
		return Lease{}, ok, err
	}
	if base == "" {
		base = rw.base
	}
	l, err = s.Pool.Acquire(ctx, repo, run, base)
	return l, true, err
}

// Release gives run's slot back once its work has landed (Pool.Release, with no forge proof).
// A run that holds no slot is not an error, and neither is work that has not landed yet: the slot
// stays leased, the refusal is recorded, and the Tower gives it back at takeoff with the merge as
// proof.
func (s *Source) Release(ctx context.Context, org, project, run string) error {
	repo, _, ok, err := s.repo(org, project)
	if !ok || err != nil {
		return err
	}
	slot, err := s.Pool.Held(ctx, repo, run)
	if errors.Is(err, ErrNotHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	err = s.Pool.Release(ctx, repo, slot.Slot, run, Proof{})
	if errors.Is(err, ErrUnlanded) {
		return nil
	}
	return err
}
