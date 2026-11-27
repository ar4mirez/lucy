package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Activity says whether an org has work for its container and when it last had any: the runner
// broker (runner.Broker.OrgActivity). busy means a run of the org is queued or leased in its
// container; last is when a run of the org last did something or ended, zero when none has.
type Activity interface {
	OrgActivity(ctx context.Context, org string) (busy bool, last time.Time, err error)
}

// DefaultIdleAfter is how long an org's container may sit with no work before it is stopped.
const DefaultIdleAfter = 30 * time.Minute

// IdleResult is what an idle check of one org came to.
type IdleResult struct {
	Org     string    `json:"org"`
	Running bool      `json:"running"`         // the provider reported the container running
	Busy    bool      `json:"busy"`            // a run of the org is queued or live in its container
	Last    time.Time `json:"last"`            // when the org last had work; zero when it never had any
	Idle    bool      `json:"idle"`            // running, not busy, and no work for at least the idle time
	Stop    *Result   `json:"stop,omitempty"`  // the guarded stop, when it was idle
	Error   string    `json:"error,omitempty"` // why this org could not be checked or stopped
}

// StopIdle stops org's container when it is running and has had no work for at least after
// (DefaultIdleAfter when zero), as of now: only orgs with active work keep a running container
// (LUCY-SBX-001, D3a). The stop is an ordinary guarded `down` asked for by the daemon: it is
// decided by the policy engine on the sessions live in the container, recorded before berth is
// asked, and holds the container while it runs, so a session that started since the check is
// never stopped without the owner's confirmation. A stopped or busy container, or one of an org
// lucy has never run work in, is left alone, and nothing is recorded for it.
func (s *Sandbox) StopIdle(ctx context.Context, org string, act Activity, now time.Time, after time.Duration) (IdleResult, error) {
	res := IdleResult{Org: org}
	if act == nil {
		return res, errors.New("sandbox: StopIdle needs the orgs' activity")
	}
	if after <= 0 {
		after = DefaultIdleAfter
	}
	st, err := s.Status(ctx, org)
	if err != nil {
		return res, err
	}
	res.Running = st.Running
	if !st.Running {
		return res, nil
	}
	res.Busy, res.Last, err = act.OrgActivity(ctx, org)
	if err != nil {
		return res, fmt.Errorf("sandbox: reading %s's activity: %w", org, err)
	}
	// An org lucy has never run work in is left alone: its container may hold work lucy does not
	// know about, and only work lucy ran tells it when the container was last needed.
	if res.Busy || res.Last.IsZero() || now.Sub(res.Last) < after {
		return res, nil
	}
	res.Idle = true
	stop, err := s.Request(ctx, org, OpDown, Daemon("idle"))
	if err != nil {
		return res, err
	}
	res.Stop = &stop
	return res, nil
}

// StopIdleOrgs runs StopIdle for each of orgs, the orgs whose Sandbox binding lucy holds: it never
// looks at, or stops, an org it was not given. One org's failure does not stop the others; it is
// in that org's result and in the joined error.
func (s *Sandbox) StopIdleOrgs(ctx context.Context, orgs []string, act Activity, now time.Time, after time.Duration) ([]IdleResult, error) {
	out := make([]IdleResult, 0, len(orgs))
	var errs []error
	for _, org := range orgs {
		r, err := s.StopIdle(ctx, org, act, now, after)
		if err != nil {
			r.Error = capText(err.Error())
			errs = append(errs, fmt.Errorf("%s: %w", org, err))
		}
		out = append(out, r)
	}
	return out, errors.Join(errs...)
}
