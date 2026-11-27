package runner

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// ActorDaemon is the actor of the events the broker appends on its own: queueing, stops and
// expiries. A runner's events are appended as "runner/<id>".
const ActorDaemon = "daemon"

func runnerActor(id string) string { return "runner/" + id }

// Broker is the daemon's side of the runner protocol. Its state is the runner_runs projection,
// so it holds nothing in memory and is safe for concurrent use: every call reads and appends in
// one store transaction, and the store has one writer.
//
// The store must register Payloads() and the Projector.
type Broker struct {
	store *eventstore.Store
	now   func() time.Time
}

// NewBroker returns a broker on store, reading time from now (time.Now when nil).
func NewBroker(store *eventstore.Store, now func() time.Time) *Broker {
	if now == nil {
		now = time.Now
	}
	return &Broker{store: store, now: now}
}

// Submit queues a run. Its Base is "main" when empty. A run id is unique across every org and
// may be submitted only once (ErrExists).
func (b *Broker) Submit(ctx context.Context, t Task) error {
	if t.Base == "" {
		t.Base = "main"
	}
	if err := checkTask(t); err != nil {
		return err
	}
	return b.store.Update(ctx, func(tx *eventstore.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runner_runs WHERE run = ?`, t.Run).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: %q", ErrExists, t.Run)
		}
		_, err := tx.Append(ctx, Stream(t.Org, t.Run), eventstore.NoStream, ActorDaemon, Queued{
			Org: t.Org, Project: t.Project, Flight: t.Flight, Generation: t.Generation, Goal: capText(t.Goal), Base: t.Base,
		})
		return err
	})
}

// Run returns one run.
func (b *Broker) Run(ctx context.Context, run string) (RunInfo, error) {
	return scanRun(b.store.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runner_runs WHERE run = ?`, run))
}

// Runs returns up to limit runs queued after the run whose queued sequence number is after,
// oldest first.
func (b *Broker) Runs(ctx context.Context, after int64, limit int) ([]RunInfo, error) {
	rows, err := b.store.QueryContext(ctx, `SELECT `+runColumns+` FROM runner_runs WHERE queued_seq > ?
		ORDER BY queued_seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []RunInfo
	for rows.Next() {
		ri, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ri)
	}
	return out, rows.Err()
}

// Queued returns how many runs wait for a runner. It is a read: a runner polls it, and leases
// only when there is something to lease, so an idle runner adds nothing to the log.
func (b *Broker) Queued(ctx context.Context) (int, error) {
	var n int
	err := b.store.QueryRowContext(ctx, `SELECT count(*) FROM runner_runs WHERE state = ?`, string(StateQueued)).Scan(&n)
	return n, err
}

// Lease gives runner the oldest queued run, under a new fence that expires after ttl (clamped to
// MinTTL..MaxTTL; DefaultTTL when zero). ok is false when nothing is queued.
func (b *Broker) Lease(ctx context.Context, runner string, placement Placement, ttl time.Duration) (l Lease, ok bool, err error) {
	if err := CheckRunnerID(runner); err != nil {
		return Lease{}, false, err
	}
	if !placement.Valid() {
		return Lease{}, false, fmt.Errorf("%w: placement %q", ErrInvalid, placement)
	}
	ttl = clampTTL(ttl)
	err = b.store.Update(ctx, func(tx *eventstore.Tx) error {
		ri, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runner_runs WHERE state = ?
			ORDER BY queued_seq LIMIT 1`, string(StateQueued)))
		if errors.Is(err, ErrNoRun) {
			return nil
		}
		if err != nil {
			return err
		}
		exp := b.now().Add(ttl).UTC()
		evs, err := tx.Append(ctx, Stream(ri.Org, ri.Run), ri.Version, runnerActor(runner),
			Leased{Runner: runner, Placement: placement, ExpiresAt: exp})
		if err != nil {
			return err
		}
		l, ok = Lease{Task: ri.Task, Runner: runner, Fence: evs[0].Seq, Expires: exp}, true
		return nil
	})
	if err != nil {
		return Lease{}, false, err
	}
	return l, ok, nil
}

// held runs fn on the run if runner holds it under fence and the lease is live. A lease found past
// its expiry is recorded as expired, and that record commits even though the call is refused.
func (b *Broker) held(ctx context.Context, runner, run string, fence int64, fn func(tx *eventstore.Tx, ri RunInfo) error) error {
	fenced := false
	err := b.store.Update(ctx, func(tx *eventstore.Tx) error {
		ri, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runner_runs WHERE run = ?`, run))
		if err != nil {
			return err
		}
		if ri.State != StateLeased || ri.Runner != runner || ri.Fence != fence {
			fenced = true
			return nil
		}
		if !b.now().Before(ri.Expires) {
			fenced = true
			_, err := tx.Append(ctx, Stream(ri.Org, ri.Run), ri.Version, ActorDaemon, Expired{Fence: ri.Fence, ExpiresAt: ri.Expires})
			return err
		}
		return fn(tx, ri)
	})
	if err != nil {
		return err
	}
	if fenced {
		return ErrFenced
	}
	return nil
}

// Heartbeat renews runner's lease on run for another ttl and records the streamed events, in one
// transaction. Events numbered at or below what the broker already recorded are a resent batch
// and are skipped; the rest must continue the numbering without a gap.
func (b *Broker) Heartbeat(ctx context.Context, runner, run string, fence int64, ttl time.Duration, events []Event) (Beat, error) {
	if len(events) > MaxBatch {
		return Beat{}, fmt.Errorf("%w: at most %d events per heartbeat", ErrInvalid, MaxBatch)
	}
	for _, ev := range events {
		if !ev.Kind.Valid() || ev.Seq < 1 {
			return Beat{}, fmt.Errorf("%w: event %d of kind %q", ErrInvalid, ev.Seq, ev.Kind)
		}
	}
	ttl = clampTTL(ttl)
	var beat Beat
	err := b.held(ctx, runner, run, fence, func(tx *eventstore.Tx, ri RunInfo) error {
		accepted := ri.Accepted
		var payloads []eventstore.Payload
		for _, ev := range events {
			switch {
			case ev.Seq <= accepted:
				continue // resent
			case ev.Seq != accepted+1:
				return fmt.Errorf("%w: got %d after %d", ErrSequence, ev.Seq, accepted)
			}
			accepted = ev.Seq
			payloads = append(payloads, Streamed{Fence: fence, Seq: ev.Seq, Kind: ev.Kind, Text: capText(ev.Text)})
		}
		exp := b.now().Add(ttl).UTC()
		payloads = append(payloads, Renewed{Fence: fence, ExpiresAt: exp})
		if _, err := tx.Append(ctx, Stream(ri.Org, ri.Run), ri.Version, runnerActor(runner), payloads...); err != nil {
			return err
		}
		beat = Beat{Expires: exp, Stop: ri.Stop, Accepted: accepted}
		return nil
	})
	return beat, err
}

// Finish ends runner's run with its result.
func (b *Broker) Finish(ctx context.Context, runner, run string, fence int64, res Result) error {
	if !res.Outcome.Valid() {
		return fmt.Errorf("%w: outcome %q", ErrInvalid, res.Outcome)
	}
	if res.PR < 0 || (res.PR != 0 && res.Outcome != OutcomeDone) {
		return fmt.Errorf("%w: only a done run reports a pull request", ErrInvalid)
	}
	return b.held(ctx, runner, run, fence, func(tx *eventstore.Tx, ri RunInfo) error {
		_, err := tx.Append(ctx, Stream(ri.Org, ri.Run), ri.Version, runnerActor(runner),
			Finished{Fence: fence, Outcome: res.Outcome, PR: res.PR, Reason: capText(res.Reason)})
		return err
	})
}

// Stop asks for a run to stop. A queued run is finished as stopped at once; a leased one is told
// on its runner's next heartbeat. Stopping a finished run does nothing.
func (b *Broker) Stop(ctx context.Context, run string) error {
	return b.store.Update(ctx, func(tx *eventstore.Tx) error {
		ri, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runner_runs WHERE run = ?`, run))
		if err != nil {
			return err
		}
		stream := Stream(ri.Org, ri.Run)
		switch {
		case ri.State == StateQueued:
			_, err = tx.Append(ctx, stream, ri.Version, ActorDaemon, StopRequested{},
				Finished{Outcome: OutcomeStopped, Reason: "stopped before a runner took it"})
		case ri.State == StateLeased && !ri.Stop:
			_, err = tx.Append(ctx, stream, ri.Version, ActorDaemon, StopRequested{})
		}
		return err
	})
}

// Expire records every leased run whose lease is past its expiry as expired, and returns how many
// it expired. The daemon calls it on a timer; Runtime.Poll also expires the run it reads.
func (b *Broker) Expire(ctx context.Context) (int, error) {
	now := b.now()
	rows, err := b.store.QueryContext(ctx, `SELECT run FROM runner_runs WHERE state = ? AND expires_ns <= ? ORDER BY queued_seq`,
		string(StateLeased), now.UnixNano())
	if err != nil {
		return 0, err
	}
	var runs []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			_ = rows.Close() // the scan error is what matters
			return 0, err
		}
		runs = append(runs, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range runs {
		done, err := b.expire(ctx, r)
		if err != nil {
			return n, err
		}
		if done {
			n++
		}
	}
	return n, nil
}

// expire records run as expired if its lease is past its expiry.
func (b *Broker) expire(ctx context.Context, run string) (expired bool, err error) {
	err = b.store.Update(ctx, func(tx *eventstore.Tx) error {
		ri, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runner_runs WHERE run = ?`, run))
		if err != nil {
			return err
		}
		if ri.State != StateLeased || b.now().Before(ri.Expires) {
			return nil
		}
		expired = true
		_, err = tx.Append(ctx, Stream(ri.Org, ri.Run), ri.Version, ActorDaemon, Expired{Fence: ri.Fence, ExpiresAt: ri.Expires})
		return err
	})
	return expired, err
}
