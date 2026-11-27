package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"
)

// Protocol is the runner's view of the broker: the Broker itself in the daemon's process, or the
// API client (Remote) from `lucy runner`.
type Protocol interface {
	Queued(ctx context.Context, placement Placement, org string) (int, error)
	Lease(ctx context.Context, runner string, placement Placement, org, proof string, ttl time.Duration) (Lease, bool, error)
	Heartbeat(ctx context.Context, runner, run string, fence int64, ttl time.Duration, events []Event, acked string) (Beat, error)
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
	// Org is the one org whose runs the runner leases; required (ErrRunnerOrg).
	Org string
	// PlacementProof is a runner placed in a container's proof of that placement, which the
	// broker's PlacementVerifier checks on every lease.
	PlacementProof string
	Protocol       Protocol // required
	Executor       Executor // required
	Now            func() time.Time
	TTL            time.Duration // the lease life asked for; DefaultTTL when zero
	MaxRuns        int           // runs held at once; 1 when zero
	Log            *slog.Logger
}

// Runner is the runner's loop: it leases queued runs, starts their work, streams the work's events
// with every heartbeat, renews each lease at a third of its life and finishes each run with its
// result. Step does one round and never sleeps, so a test drives it on virtual time; Run calls
// Step on a timer. A Runner is not safe for concurrent use.
type Runner struct {
	cfg      Config
	runs     map[string]*held
	draining atomic.Bool
}

type held struct {
	lease    Lease
	exec     Execution // nil while the run's worktree is being prepared
	next     int64     // the next event number to give
	pending  []Event   // numbered, not yet recorded by the broker
	beatAt   time.Time
	result   *Result
	stopping bool
	question *Event // the work's question, streamed and not yet answered
	heard    bool   // the broker has reported question as asking
	answered string // the id of the question whose answer the work took last
	ack      string // the id to acknowledge on the next heartbeat
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
	if cfg.Org == "" {
		return nil, fmt.Errorf("%w: Config names no org", ErrRunnerOrg)
	}
	if err := checkOrg(cfg.Org); err != nil {
		return nil, err
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

// Drain makes the runner lease nothing more: its held runs carry on to their end, and Run returns
// once it holds none, so a planned restart or upgrade of the runner loses no work. It is safe to
// call from any goroutine, such as a signal handler; nothing undoes it.
func (r *Runner) Drain() { r.draining.Store(true) }

// Draining reports whether Drain was called.
func (r *Runner) Draining() bool { return r.draining.Load() }

// Drained reports whether the runner is draining and holds no run.
func (r *Runner) Drained() bool { return r.draining.Load() && len(r.runs) == 0 }

// Step tends every held run, in run id order, then leases a queued run if the runner has room and
// is not draining.
func (r *Runner) Step(ctx context.Context) error {
	var errs []error
	for _, id := range r.order() {
		if err := r.tend(ctx, r.runs[id]); err != nil {
			errs = append(errs, fmt.Errorf("runner: run %s: %w", id, err))
		}
	}
	if len(r.runs) < r.cfg.MaxRuns && !r.draining.Load() {
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
	if h.exec != nil {
		u := h.exec.Poll()
		for _, ev := range u.Events {
			h.queue(ev)
		}
		if u.Result != nil && h.result == nil {
			h.result = u.Result
		}
	}
	now := r.cfg.Now()
	waiting := h.exec == nil && h.result == nil // its worktree is being prepared: ask on every step
	for waiting || len(h.pending) > 0 || !now.Before(h.beatAt) {
		waiting = false
		batch := h.pending[:min(len(h.pending), MaxBatch)]
		acked := h.ack
		beat, err := r.cfg.Protocol.Heartbeat(ctx, r.cfg.ID, h.lease.Run, h.lease.Fence, r.cfg.TTL, batch, acked)
		if errors.Is(err, ErrFenced) {
			r.drop(h, "fenced")
			return nil
		}
		if err != nil {
			return err // kept: resent on the next Step, and the broker skips what it already has
		}
		if h.ack == acked {
			h.ack = ""
		}
		before := len(h.pending)
		for len(h.pending) > 0 && h.pending[0].Seq <= beat.Accepted {
			h.pending = h.pending[1:]
		}
		h.lease.Expires, h.beatAt = beat.Expires, now.Add(r.cfg.TTL/3)
		if beat.Capability != "" {
			h.lease.Capability = beat.Capability
			if rn, ok := h.exec.(Renewer); ok {
				rn.Renew(beat.Capability)
			}
		}
		if len(batch) > 0 && len(h.pending) == before {
			return fmt.Errorf("the broker recorded none of events %d..%d", batch[0].Seq, batch[len(batch)-1].Seq)
		}
		switch {
		case beat.Answer != "":
			r.answer(h, beat)
		case len(h.pending) > 0 || h.question == nil:
		case beat.Asking:
			h.heard = true
		case h.heard && h.result == nil:
			// The broker held the question and holds it no more, yet no answer came: it was
			// answered and the answer was lost, as when the daemon restarted before this
			// heartbeat. Ask again.
			r.cfg.Log.Warn("runner lost a run's answer; asking again", "run", h.lease.Run)
			h.queue(*h.question)
		}
		if h.exec == nil && h.result == nil {
			r.await(ctx, h, beat)
		}
		if beat.Stop && !h.stopping && h.exec != nil {
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

// await starts the work of a run whose worktree was being prepared once the heartbeat reports it
// ready, and finishes the run as failed when it reports it failed, or none, and as stopped when
// the Tower asked for the run to stop first.
func (r *Runner) await(ctx context.Context, h *held, beat Beat) {
	switch {
	case beat.Stop:
		h.result, h.stopping = &Result{Outcome: OutcomeStopped, Reason: "stopped while its worktree was being prepared"}, true
	case beat.WorktreeState == WorktreePreparing:
	case beat.WorktreeState == WorktreeReady:
		h.lease.WorktreeState = WorktreeReady
		exec, err := r.cfg.Executor.Start(ctx, h.lease)
		if err != nil {
			r.cfg.Log.Warn("runner could not start a run", "run", h.lease.Run, "err", err)
			h.result = &Result{Outcome: OutcomeFailed, Reason: "the runner could not start the work: " + err.Error()}
			return
		}
		h.exec = exec
	default:
		why := beat.WorktreeError
		if why == "" {
			why = "the daemon reports no worktree for the run"
		}
		h.result = &Result{Outcome: OutcomeFailed, Reason: "the run's worktree could not be prepared: " + why}
	}
}

// answer gives the work the owner's answer to a question once, however often the broker sends
// it, and acknowledges it once the work took it. One the work cannot take is logged and not
// acknowledged: the question stays open, and the broker sends the answer again.
func (r *Runner) answer(h *held, beat Beat) {
	if beat.Question != h.answered {
		a, ok := h.exec.(Answerer)
		if !ok {
			r.cfg.Log.Warn("runner got an answer for a run whose work takes no input", "run", h.lease.Run)
			return
		}
		if err := a.Answer(beat.Answer); err != nil {
			r.cfg.Log.Warn("runner could not give a run its answer", "run", h.lease.Run, "err", err)
			return
		}
		h.answered, h.question, h.heard = beat.Question, nil, false
	}
	h.ack = beat.Question
}

// queue numbers one of the work's events for streaming and tracks its open question.
func (h *held) queue(ev Event) {
	ev.Seq, ev.Text = h.next, capText(ev.Text)
	h.next++
	h.pending = append(h.pending, ev)
	h.question, h.heard = nil, false
	if ev.Kind == KindAsking {
		h.question = &Event{Kind: KindAsking, Text: ev.Text, Question: ev.Question}
	}
}

func (r *Runner) drop(h *held, why string) {
	if h.exec != nil {
		h.exec.Stop()
	}
	delete(r.runs, h.lease.Run)
	r.cfg.Log.Warn("runner dropped a run", "run", h.lease.Run, "fence", h.lease.Fence, "why", why)
}

// take leases the oldest queued run and starts its work, or, while its worktree is being prepared,
// holds it until a heartbeat reports the worktree ready. A run whose work cannot start is finished
// as failed.
func (r *Runner) take(ctx context.Context) error {
	n, err := r.cfg.Protocol.Queued(ctx, r.cfg.Placement, r.cfg.Org)
	if err != nil || n == 0 {
		return err
	}
	l, ok, err := r.cfg.Protocol.Lease(ctx, r.cfg.ID, r.cfg.Placement, r.cfg.Org, r.cfg.PlacementProof, r.cfg.TTL)
	if errors.Is(err, ErrSandboxBusy) {
		return nil // the next Step leases again
	}
	if err != nil || !ok {
		return err
	}
	if l.WorktreeState == WorktreePreparing {
		r.runs[l.Run] = &held{lease: l, next: 1, beatAt: r.cfg.Now().Add(r.cfg.TTL / 3)}
		return nil
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
// streamed and finishes it as lost, so the Tower replaces it, once, rather than waiting for the
// lease to expire. A planned restart drains instead (Drain), and loses nothing; a runner that
// crashes loses its runs the same way, when their leases expire.
func (r *Runner) Shutdown(ctx context.Context, grace time.Duration) error {
	for _, id := range r.order() {
		if h := r.runs[id]; h.exec != nil {
			h.exec.Stop()
		}
	}
	deadline := time.Now().Add(grace)
	var errs []error
	for _, id := range r.order() {
		h := r.runs[id]
		for h.exec != nil && h.result == nil && time.Now().Before(deadline) {
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

// Run calls Step every interval until ctx ends, then shuts down with a 5 s grace, or until the
// runner, draining, holds no run, and then returns nil. A Step's error is logged, not fatal: the
// daemon may be restarting, and a lease outlives a short outage. The one exceptions are ErrHostOrg
// and ErrPlacementProof, which no retry clears: Run shuts down and returns them.
func (r *Runner) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := r.Step(ctx); err != nil && ctx.Err() == nil {
			if errors.Is(err, ErrHostOrg) || errors.Is(err, ErrPlacementProof) {
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				return errors.Join(err, r.Shutdown(sctx, 5*time.Second))
			}
			r.cfg.Log.Warn("runner step failed", "err", err)
		}
		if r.Drained() {
			r.cfg.Log.Info("runner drained: every held run ended")
			return nil
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
