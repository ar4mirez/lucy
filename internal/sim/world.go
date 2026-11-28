package sim

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"time"

	"github.com/ar4mirez/lucy/internal/audit"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Options configure a World. The zero value is a World at Epoch with the Tower's defaults.
type Options struct {
	Start time.Time    // the clock's start; Epoch when zero
	Tower tower.Config // timings and limits; Store, Runtime, Forge, Clock and ForgeIdentity are the World's own
	// Runners, when not zero, flies Pilots through the runner protocol with that many runners of
	// RunnerOrg's runs (see runners.go); zero lets the Tower drive the fake runtime directly.
	Runners   int
	RunnerOrg string // "acme" when empty
}

// World is one simulation: a virtual clock, a fake forge, a fake Pilot runtime, an event store
// in dir and a Tower on it.
type World struct {
	Seed    uint64
	Clock   *Clock
	Forge   *Forge
	Runtime *Runtime
	Store   *eventstore.Store
	Tower   *tower.Tower
	// With Options.Runners: the daemon's broker (nil while the daemon is down) and the runners.
	Broker  *runner.Broker
	Runners []*runner.Runner
	// FlightErrors are the flights' failed steps, in order: the Tower retries each such flight on
	// its own backoff, so a Run goes on.
	FlightErrors []tower.FlightError

	dir      string
	config   tower.Config
	runners  bool
	sessions map[string]string // run -> the fake session playing it
	cut      map[int]bool      // runners cut off from the daemon
	stopped  time.Time         // when the daemon last stopped
}

// Registry returns the event registry a World's store opens with: the Tower's events, policy
// decisions and the runner protocol's run events, so a scenario can drive the daemon's broker.
func Registry() (*eventstore.Registry, error) {
	return eventstore.NewRegistry(append(append(tower.Payloads(), policy.DecisionRecord{}), runner.Payloads()...)...)
}

// Open starts a World whose store lives in dir (a directory the caller owns, such as
// t.TempDir(); it must be mode 0700 and outside any git work tree). All its randomness comes
// from seed.
func Open(ctx context.Context, dir string, seed uint64, opts Options) (*World, error) {
	start := opts.Start
	if start.IsZero() {
		start = Epoch
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x6c756379)) // #nosec G404 -- deterministic simulation from a test seed (0x6c756379 is "lucy"); never for secrets
	clock := NewClock(start)
	forge := newForge(clock, rng)
	w := &World{Seed: seed, Clock: clock, Forge: forge, Runtime: newRuntime(clock, rng, forge), dir: dir, config: opts.Tower,
		runners: opts.Runners > 0, sessions: map[string]string{}, cut: map[int]bool{}}
	if err := w.open(ctx); err != nil {
		return nil, err
	}
	if w.runners {
		org := opts.RunnerOrg
		if org == "" {
			org = "acme"
		}
		if err := w.newRunners(opts.Runners, org); err != nil {
			_ = w.Close() // the runner's error is what matters
			return nil, err
		}
	}
	return w, nil
}

func (w *World) open(ctx context.Context) error {
	reg, err := Registry()
	if err != nil {
		return err
	}
	store, err := eventstore.Open(ctx, filepath.Join(w.dir, "lucy", eventstore.DefaultFile), eventstore.Options{
		Registry: reg, Projectors: []eventstore.Projector{runner.Projector{}, audit.Projector{}}, Now: w.Clock.Now,
		SnapshotDir: filepath.Join(w.dir, "lucy", "state"),
	})
	if err != nil {
		return err
	}
	cfg := w.config
	cfg.Store, cfg.Runtime, cfg.Forge, cfg.Clock, cfg.ForgeIdentity = store, w.Runtime, w.Forge, w.Clock, w.Forge.Identity
	var broker *runner.Broker
	if w.runners {
		// As the daemon does on start: every held lease is moved later by the outage first.
		broker = runner.NewBroker(store, w.Clock.Now)
		if _, err := broker.Resume(ctx, w.stopped); err != nil {
			_ = store.Close() // the broker's error is what matters
			return err
		}
		cfg.Runtime = runner.Runtime{Broker: broker}
	}
	t, err := tower.New(ctx, cfg)
	if err != nil {
		_ = store.Close() // the Tower's error is what matters
		return err
	}
	w.Store, w.Tower, w.Broker = store, t, broker
	return nil
}

// Close closes the World's store.
func (w *World) Close() error {
	if w.Store == nil {
		return nil
	}
	err := w.Store.Close()
	w.Store, w.Tower, w.Broker, w.stopped = nil, nil, nil, w.Clock.Now()
	return err
}

// Restart is a fault: the daemon stops and starts again. The store is closed and reopened and a
// new Tower rebuilds every flight from the log; the forge, the Pilot sessions and the clock
// carry on, as they would outside the daemon.
func (w *World) Restart(ctx context.Context) error {
	if err := w.Close(); err != nil {
		return err
	}
	return w.open(ctx)
}

// Dispatch dispatches a flight, scripting its first Pilot generation when steps are given.
func (w *World) Dispatch(ctx context.Context, s tower.Spec, steps ...Step) error {
	if len(steps) > 0 {
		w.Runtime.Script(s.ID, steps...)
	}
	return w.Tower.Dispatch(ctx, s)
}

// RunOptions bound a Run.
type RunOptions struct {
	// For is the most virtual time the Run may take; 7 days when zero.
	For time.Duration
	// BeforeStep, when set, runs before every Tower step with the step's number (from 1): the
	// hook the chaos suite injects faults through.
	BeforeStep func(ctx context.Context, w *World, step int) error
}

// ErrTimeout is returned when a Run's virtual time runs out with a flight still active.
var ErrTimeout = errors.New("sim: virtual time ran out with a flight still active")

// Run steps the Tower (with runners, each runner first) and advances the clock by the Tower's poll interval until every flight is
// final or waiting on the owner (asking or holding), and returns. Between Runs a scenario plays
// the owner: Answer, Approve.
func (w *World) Run(ctx context.Context, opts RunOptions) error {
	limit := opts.For
	if limit <= 0 {
		limit = 7 * 24 * time.Hour
	}
	end := w.Clock.Now().Add(limit)
	for step := 1; ; step++ {
		if opts.BeforeStep != nil {
			if err := opts.BeforeStep(ctx, w, step); err != nil {
				return err
			}
		}
		if w.runners {
			if err := w.stepRunners(ctx); err != nil {
				return err
			}
		}
		if err := w.flightErrors(w.Tower.Step(ctx)); err != nil {
			return err
		}
		next, active := w.Tower.NextWake()
		if !active || w.waitingOnOwner() {
			return nil
		}
		if next.After(end) {
			return fmt.Errorf("%w (after %s)", ErrTimeout, limit)
		}
		if w.runners && next.Sub(w.Clock.Now()) > runnerStep {
			next = w.Clock.Now().Add(runnerStep) // the runners renew their leases in time
		}
		w.Clock.Advance(next.Sub(w.Clock.Now()))
	}
}

// waitingOnOwner reports whether every active flight waits on the owner.
func (w *World) waitingOnOwner() bool {
	for _, f := range w.Tower.Flights() {
		if !f.State.Final() && f.State != tower.StateAsking && f.State != tower.StateHolding {
			return false
		}
	}
	return true
}

// Line is one event of the log, as the determinism test compares them.
type Line struct {
	Seq     int64
	Time    time.Time
	Stream  string
	Version int64
	Type    string
	Actor   string
	Payload string
}

// Log returns the whole event log.
func (w *World) Log(ctx context.Context) ([]Line, error) {
	var out []Line
	err := w.Store.Replay(ctx, 0, func(e eventstore.Event) error {
		out = append(out, Line{e.Seq, e.Time, e.Stream, e.StreamVersion, e.Type, e.Actor, string(e.Payload)})
		return nil
	})
	return out, err
}

// Decisions returns every policy decision recorded, in order, with its seq.
func (w *World) Decisions(ctx context.Context) ([]Decision, error) {
	var out []Decision
	err := w.Store.Replay(ctx, 0, func(e eventstore.Event) error {
		if e.Stream != policy.Stream {
			return nil
		}
		rec, err := eventstore.DecodeAs[policy.DecisionRecord](w.Store.Registry(), e)
		if err != nil {
			return err
		}
		out = append(out, Decision{Seq: e.Seq, Record: rec})
		return nil
	})
	return out, err
}

// Decision is a recorded policy decision.
type Decision struct {
	Seq    int64
	Record policy.DecisionRecord
}

// flightErrors keeps each flight's failed step in FlightErrors, since the Tower retries that flight
// on its own backoff, and returns whatever else went wrong.
func (w *World) flightErrors(err error) error {
	if err == nil {
		return nil
	}
	errs := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	}
	var rest []error
	for _, e := range errs {
		var fe *tower.FlightError
		if errors.As(e, &fe) {
			w.FlightErrors = append(w.FlightErrors, *fe)
			continue
		}
		rest = append(rest, e)
	}
	return errors.Join(rest...)
}
