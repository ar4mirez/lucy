package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// Protocol is the runner's view of the broker: the Broker itself in the daemon's process, or the
// API client (Remote) from `lucy runner`.
type Protocol interface {
	Queued(ctx context.Context) (int, error)
	Lease(ctx context.Context, runner string, placement Placement, ttl time.Duration) (Lease, bool, error)
	Heartbeat(ctx context.Context, runner, run string, fence int64, ttl time.Duration, events []Event) (Beat, error)
	Finish(ctx context.Context, runner, run string, fence int64, res Result) error
}

var _ Protocol = (*Broker)(nil)

// Executor starts the work of a leased run. Start's ctx bounds only the start: the work lives
// until it ends or its Execution is stopped.
type Executor interface {
	Start(ctx context.Context, l Lease) (Execution, error)
}

// Execution is one run's work in progress. Poll never blocks: it returns what happened since the
// last Poll and, once the work has ended, its Result. Stop asks the work to end; a later Poll
// reports the Result.
type Execution interface {
	Poll() Update
	Stop()
}

// Answerer is an Execution that takes input: the owner's answer to the question its work asked,
// as the Tower relays it in a heartbeat. An Execution that never streams a question needs none.
type Answerer interface {
	Answer(text string) error
}

// Update is what an Execution reports on one Poll. Its events' Seq is ignored: the runner numbers
// a run's events.
type Update struct {
	Events []Event
	Result *Result // set once the work has ended
}

// Config configures a Runner.
type Config struct {
	ID        string    // the runner's id, CheckRunnerID; required
	Placement Placement // PlacementHost when empty
	Protocol  Protocol  // required
	Executor  Executor  // required
	Now       func() time.Time
	TTL       time.Duration // the lease life asked for; DefaultTTL when zero
	MaxRuns   int           // runs held at once; 1 when zero
	Log       *slog.Logger
}

// Runner is the runner's loop: it leases queued runs, starts their work, streams the work's events
// with every heartbeat, renews each lease at a third of its life and finishes each run with its
// result. Step does one round and never sleeps, so a test drives it on virtual time; Run calls
// Step on a timer. A Runner is not safe for concurrent use.
type Runner struct {
	cfg  Config
	runs map[string]*held
}

type held struct {
	lease    Lease
	exec     Execution
	next     int64   // the next event number to give
	pending  []Event // numbered, not yet recorded by the broker
	beatAt   time.Time
	result   *Result
	stopping bool
	question string // the work's question, streamed and not yet answered
	heard    bool   // the broker has reported question as asking
	answered string // the question just answered, until the work streams something else
}

// New returns a Runner.
func New(cfg Config) (*Runner, error) {
	if cfg.Placement == "" {
		cfg.Placement = PlacementHost
	}
	if err := CheckRunnerID(cfg.ID); err != nil {
		return nil, err
	}
	if !cfg.Placement.Valid() || cfg.Protocol == nil || cfg.Executor == nil {
		return nil, errors.New("runner: Config needs a valid placement, a Protocol and an Executor")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.TTL = clampTTL(cfg.TTL)
	if cfg.MaxRuns <= 0 {
		cfg.MaxRuns = 1
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	return &Runner{cfg: cfg, runs: map[string]*held{}}, nil
}

// Held returns the leases the runner holds, by run id.
func (r *Runner) Held() []Lease {
	var out []Lease
	for _, id := range r.order() {
		out = append(out, r.runs[id].lease)
	}
	return out
}

func (r *Runner) order() []string {
	ids := make([]string, 0, len(r.runs))
	for id := range r.runs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Step tends every held run, in run id order, then leases a queued run if the runner has room.
func (r *Runner) Step(ctx context.Context) error {
	var errs []error
	for _, id := range r.order() {
		if err := r.tend(ctx, r.runs[id]); err != nil {
			errs = append(errs, fmt.Errorf("runner: run %s: %w", id, err))
		}
	}
	if len(r.runs) < r.cfg.MaxRuns {
		if err := r.take(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// tend streams a run's new events, renews its lease when due and finishes it once its work ended
// and every event is recorded. A fenced run is dropped: its work is stopped and nothing more is
// sent under the old fence.
func (r *Runner) tend(ctx context.Context, h *held) error {
	u := h.exec.Poll()
	for _, ev := range u.Events {
		h.queue(ev)
	}
	if u.Result != nil && h.result == nil {
		h.result = u.Result
	}
	now := r.cfg.Now()
	for len(h.pending) > 0 || !now.Before(h.beatAt) {
		batch := h.pending[:min(len(h.pending), MaxBatch)]
		beat, err := r.cfg.Protocol.Heartbeat(ctx, r.cfg.ID, h.lease.Run, h.lease.Fence, r.cfg.TTL, batch)
		if errors.Is(err, ErrFenced) {
			r.drop(h, "fenced")
			return nil
		}
		if err != nil {
			return err // kept: resent on the next Step, and the broker skips what it already has
		}
		before := len(h.pending)
		for len(h.pending) > 0 && h.pending[0].Seq <= beat.Accepted {
			h.pending = h.pending[1:]
		}
		h.lease.Expires, h.beatAt = beat.Expires, now.Add(r.cfg.TTL/3)
		if len(batch) > 0 && len(h.pending) == before {
			return fmt.Errorf("the broker recorded none of events %d..%d", batch[0].Seq, batch[len(batch)-1].Seq)
		}
		switch {
		case beat.Answer != "":
			h.answered, h.question, h.heard = h.question, "", false
			r.answer(h, beat.Answer)
		case len(h.pending) > 0 || h.question == "":
		case beat.Asking:
			h.heard = true
		case h.heard && h.result == nil:
			// The broker held the question and holds it no more, yet no answer came: it was
			// answered and the answer was lost, as when the daemon restarted before this
			// heartbeat. Ask again.
			r.cfg.Log.Warn("runner lost a run's answer; asking again", "run", h.lease.Run)
			h.queue(Event{Kind: KindAsking, Text: h.question})
		}
		if beat.Stop && !h.stopping {
			h.exec.Stop()
			h.stopping = true
		}
	}
	if h.result == nil {
		return nil
	}
	err := r.cfg.Protocol.Finish(ctx, r.cfg.ID, h.lease.Run, h.lease.Fence, *h.result)
	if err != nil && !errors.Is(err, ErrFenced) {
		return err
	}
	delete(r.runs, h.lease.Run)
	return nil
}

// answer gives the work the owner's answer. One that cannot be given is logged, not retried: the
// work is still asking, so the Tower asks the owner again.
func (r *Runner) answer(h *held, text string) {
	a, ok := h.exec.(Answerer)
	if !ok {
		r.cfg.Log.Warn("runner got an answer for a run whose work takes no input", "run", h.lease.Run)
		return
	}
	if err := a.Answer(text); err != nil {
		h.answered = ""
		r.cfg.Log.Warn("runner could not give a run its answer", "run", h.lease.Run, "err", err)
	}
}

// queue numbers one of the work's events for streaming and tracks its open question.
func (h *held) queue(ev Event) {
	ev.Text = capText(ev.Text)
	if ev.Kind == KindAsking && h.answered != "" && ev.Text == h.answered {
		return // the work repeated a question whose answer it is being given
	}
	h.answered = ""
	ev.Seq = h.next
	h.next++
	h.pending = append(h.pending, ev)
	h.question, h.heard = "", false
	if ev.Kind == KindAsking {
		h.question = ev.Text
	}
}

func (r *Runner) drop(h *held, why string) {
	h.exec.Stop()
	delete(r.runs, h.lease.Run)
	r.cfg.Log.Warn("runner dropped a run", "run", h.lease.Run, "fence", h.lease.Fence, "why", why)
}

// take leases the oldest queued run and starts its work. A run whose work cannot start is
// finished as failed.
func (r *Runner) take(ctx context.Context) error {
	n, err := r.cfg.Protocol.Queued(ctx)
	if err != nil || n == 0 {
		return err
	}
	l, ok, err := r.cfg.Protocol.Lease(ctx, r.cfg.ID, r.cfg.Placement, r.cfg.TTL)
	if err != nil || !ok {
		return err
	}
	exec, err := r.cfg.Executor.Start(ctx, l)
	if err != nil {
		r.cfg.Log.Warn("runner could not start a run", "run", l.Run, "err", err)
		ferr := r.cfg.Protocol.Finish(ctx, r.cfg.ID, l.Run, l.Fence, Result{Outcome: OutcomeFailed, Reason: "the runner could not start the work: " + err.Error()})
		if errors.Is(ferr, ErrFenced) {
			return nil
		}
		return ferr
	}
	r.runs[l.Run] = &held{lease: l, exec: exec, next: 1, beatAt: r.cfg.Now().Add(r.cfg.TTL / 3)}
	return nil
}

// Shutdown stops every held run's work, waits up to grace for each to end, records what it
// streamed and finishes it as lost, so the Tower replaces it rather than waiting for the lease to
// expire. TODO(LUCY-RUN-004): keep the work alive across a runner restart and reattach to it
// (m1 step 5).
func (r *Runner) Shutdown(ctx context.Context, grace time.Duration) error {
	for _, id := range r.order() {
		r.runs[id].exec.Stop()
	}
	deadline := time.Now().Add(grace)
	var errs []error
	for _, id := range r.order() {
		h := r.runs[id]
		for h.result == nil && time.Now().Before(deadline) {
			u := h.exec.Poll()
			for _, ev := range u.Events {
				h.queue(ev)
			}
			h.result = u.Result
			if h.result == nil {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if h.result == nil || (h.result.Outcome == OutcomeStopped && !h.stopping) {
			h.result = &Result{Outcome: OutcomeLost, Reason: "the runner shut down"} // the runner stopped it, not the Tower
		}
		h.beatAt = time.Time{}
		if err := r.tend(ctx, h); err != nil {
			errs = append(errs, fmt.Errorf("runner: run %s: %w", id, err))
		}
		delete(r.runs, id)
	}
	return errors.Join(errs...)
}

// Run calls Step every interval until ctx ends, then shuts down with a 5 s grace. A Step's error
// is logged, not fatal: the daemon may be restarting, and a lease outlives a short outage.
func (r *Runner) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := r.Step(ctx); err != nil && ctx.Err() == nil {
			r.cfg.Log.Warn("runner step failed", "err", err)
		}
		select {
		case <-ctx.Done():
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			return r.Shutdown(sctx, 5*time.Second)
		case <-t.C:
		}
	}
}
