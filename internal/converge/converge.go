// Package converge runs lucy's converge loops (LUCY-SPEC-008, spec section 9): level-triggered,
// idempotent reconcilers with backoff, woken by the event log.
//
// A Reconciler reads the state it reconciles, from the store, and acts to move it toward what it
// should be. It is level-triggered: it never depends on which event woke it, or on having seen
// every event, only on the state it reads. It is idempotent: run again on unchanged state, it does
// nothing, and in particular appends nothing, so its own writes do not keep waking it. Run calls it
// when the log changes (a transaction that appended commits), when the time it asked for comes, and
// at least every Resync whatever happens; after a failure it waits an exponential backoff before
// trying again, and events do not cut that wait short.
package converge

import (
	"context"
	"log/slog"
	"time"
)

// Reconciler is one converge loop's body.
type Reconciler interface {
	// Name names the loop in logs.
	Name() string
	// Reconcile moves the state one step toward what it should be. next is when it must run again
	// at the latest even if nothing changes (a timeout it watches, a poll), zero when only a change
	// to the log or the resync should wake it. An error is retried after a backoff.
	Reconcile(ctx context.Context) (next time.Time, err error)
}

// Waker signals changes to the event log: *eventstore.Store.
type Waker interface {
	// Changed returns a channel closed by the next commit that appends.
	Changed() <-chan struct{}
}

// Defaults.
const (
	DefaultResync      = time.Minute
	DefaultMinInterval = 250 * time.Millisecond
	DefaultMinBackoff  = time.Second
	DefaultMaxBackoff  = 5 * time.Minute
)

// Options tune a loop; zero values take the defaults.
type Options struct {
	Resync      time.Duration // the longest it goes without running
	MinInterval time.Duration // the shortest time between two runs: a burst of events wakes it once
	MinBackoff  time.Duration // the wait after the first failure, doubled after each further one
	MaxBackoff  time.Duration // the longest wait after failures
	Log         *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.Resync <= 0 {
		o.Resync = DefaultResync
	}
	if o.MinInterval <= 0 {
		o.MinInterval = DefaultMinInterval
	}
	if o.MinBackoff <= 0 {
		o.MinBackoff = DefaultMinBackoff
	}
	if o.MaxBackoff < o.MinBackoff {
		o.MaxBackoff = max(DefaultMaxBackoff, o.MinBackoff)
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return o
}

// Backoff is the wait after failures consecutive failures (failures >= 1): MinBackoff doubled for
// each failure after the first, at most MaxBackoff.
func (o Options) Backoff(failures int) time.Duration {
	o = o.withDefaults()
	d := o.MinBackoff
	for i := 1; i < failures && d < o.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, o.MaxBackoff)
}

// Run runs r until ctx ends: once at the start, then whenever w signals a change, r's next comes or
// Resync passes, never twice within MinInterval, and after a failure only once its backoff has
// passed.
func Run(ctx context.Context, r Reconciler, w Waker, opts Options) {
	opts = opts.withDefaults()
	failures := 0
	for {
		changed := w.Changed() // taken before the run, so a change during it wakes the next one
		started := time.Now()
		next, err := r.Reconcile(ctx)
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		if err != nil {
			failures++
			wait = opts.Backoff(failures)
			opts.Log.Warn("converge loop failed; retrying after a backoff", "loop", r.Name(), "err", err, "failures", failures, "backoff", wait)
			changed = nil // a failed loop waits its backoff out
		} else {
			failures = 0
			wait = opts.Resync
			if !next.IsZero() {
				wait = min(wait, max(0, time.Until(next)))
			}
		}
		if !sleep(ctx, wait, changed) {
			return
		}
		// Never run twice within MinInterval: a burst of events wakes the loop once.
		if rest := opts.MinInterval - time.Since(started); rest > 0 && !sleep(ctx, rest, nil) {
			return
		}
	}
}

// sleep waits d, or until changed is closed, and reports false when ctx ended first. A nil changed
// never wakes it.
func sleep(ctx context.Context, d time.Duration, changed <-chan struct{}) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	case <-changed:
	}
	return true
}
