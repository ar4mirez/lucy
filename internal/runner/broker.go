package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// ActorDaemon is the actor of the events the broker appends on its own: queueing, stops and
// expiries. A runner's events are appended as "runner/<id>".
const ActorDaemon = "daemon"

func runnerActor(id string) string { return "runner/" + id }

// Broker is the daemon's side of the runner protocol. Its state is the runner_runs projection,
// and every call reads and appends in one store transaction (the store has one writer), so it is
// safe for concurrent use. The one thing it holds in memory is an answer on its way to a runner:
// the log keeps only the answer's digest, so the text waits here until the runner acknowledges it.
//
// The store must register Payloads() and the Projector.
type Broker struct {
	store *eventstore.Store
	now   func() time.Time

	mu      sync.Mutex
	answers map[string]answer // by run
}

// answer is an owner's answer waiting for the heartbeat of the lease it was given under.
type answer struct {
	fence    int64
	question string // the id of the question answered
	text     string
}

// NewBroker returns a broker on store, reading time from now (time.Now when nil).
func NewBroker(store *eventstore.Store, now func() time.Time) *Broker {
	if now == nil {
		now = time.Now
	}
	return &Broker{store: store, now: now, answers: map[string]answer{}}
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
			b.dropAnswer(run)
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

// repeatedQuestion records a question the Pilot streamed again after the owner answered it: its
// answer waits for the runner or the runner acknowledged it, so the question does not re-open. An
// answer a daemon restart lost was never acknowledged, so its question re-opens and the owner is
// asked again.
const repeatedQuestion = "the Pilot repeated a question the owner answered"

// Heartbeat renews runner's lease on run for another ttl and records the streamed events, in one
// transaction. Events numbered at or below what the broker already recorded are a resent batch
// and are skipped; the rest must continue the numbering without a gap. Every asking event names
// its question's id. acked, when set, is the id of the question whose answer the runner gave its
// work: only then is the question answered for good, and until then each reply carries the answer.
// An acked id the owner did not answer under fence is ignored.
func (b *Broker) Heartbeat(ctx context.Context, runner, run string, fence int64, ttl time.Duration, events []Event, acked string) (Beat, error) {
	if len(events) > MaxBatch {
		return Beat{}, fmt.Errorf("%w: at most %d events per heartbeat", ErrInvalid, MaxBatch)
	}
	if len(acked) > maxIDBytes {
		return Beat{}, fmt.Errorf("%w: an acknowledged question id is at most %d bytes", ErrInvalid, maxIDBytes)
	}
	for _, ev := range events {
		if !ev.Kind.Valid() || ev.Seq < 1 || len(ev.Question) > maxIDBytes || (ev.Kind == KindAsking) != (ev.Question != "") {
			return Beat{}, fmt.Errorf("%w: event %d of kind %q with question id %q; a question, and only a question, names its id", ErrInvalid, ev.Seq, ev.Kind, ev.Question)
		}
	}
	ttl = clampTTL(ttl)
	var beat Beat
	var given answer
	err := b.held(ctx, runner, run, fence, func(tx *eventstore.Tx, ri RunInfo) error {
		// The answer is read inside the transaction, which no Answer can interleave with, so the
		// runner sees it together with the asking state it belongs to.
		given = b.peekAnswer(run, fence)
		if acked != ri.Given {
			acked = "" // only an answer the owner gave can be acknowledged
		}
		if acked != "" && given.question == acked {
			given = answer{}
		}
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
			if ev.Kind == KindAsking && (ev.Question == ri.Answered || ev.Question == given.question || ev.Question == acked) {
				ev.Kind, ev.Text, ev.Question = KindActivity, repeatedQuestion, ""
			}
			payloads = append(payloads, Streamed{Fence: fence, Seq: ev.Seq, Kind: ev.Kind, Text: capText(ev.Text), Question: ev.Question})
		}
		exp := b.now().Add(ttl).UTC()
		payloads = append(payloads, Renewed{Fence: fence, ExpiresAt: exp, Answered: acked})
		if _, err := tx.Append(ctx, Stream(ri.Org, ri.Run), ri.Version, runnerActor(runner), payloads...); err != nil {
			return err
		}
		asking := ri.Asking
		if n := len(payloads) - 1; n > 0 {
			asking = payloads[n-1].(Streamed).Kind == KindAsking
		}
		beat = Beat{Expires: exp, Stop: ri.Stop, Accepted: accepted, Asking: asking, Answer: given.text, Question: given.question}
		return nil
	})
	if err != nil {
		return Beat{}, err // a refused heartbeat leaves the holder's answer waiting
	}
	if acked != "" { // set to "" in the transaction when the owner never answered it
		b.acknowledged(run, fence, acked)
	}
	return beat, nil
}

// Answer relays the owner's answer to a leased run that is asking; only the Tower calls it
// (Runtime.Answer), so a Pilot hears from the owner through the Tower and nothing else
// (LUCY-RUN-013). The log records the answer's digest; the text goes to the run's runner on
// each heartbeat until it acknowledges it. An answer the runner never received, because the
// daemon restarted first, is noticed by the runner, which asks again, so the Tower asks the owner
// again.
func (b *Broker) Answer(ctx context.Context, run, text string) error {
	if text == "" || len(text) > MaxAnswer {
		return fmt.Errorf("%w: an answer is 1 to %d bytes", ErrInvalid, MaxAnswer)
	}
	held := false
	err := b.store.Update(ctx, func(tx *eventstore.Tx) error {
		ri, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runner_runs WHERE run = ?`, run))
		if err != nil {
			return err
		}
		if ri.State != StateLeased || !ri.Asking {
			return ErrNotAsking
		}
		sum := sha256.Sum256([]byte(text))
		if _, err = tx.Append(ctx, Stream(ri.Org, ri.Run), ri.Version, ActorDaemon,
			Answered{Fence: ri.Fence, Question: ri.QuestionID, AnswerSHA256: hex.EncodeToString(sum[:])}); err != nil {
			return err
		}
		b.mu.Lock()
		b.answers[run] = answer{fence: ri.Fence, question: ri.QuestionID, text: text}
		b.mu.Unlock()
		held = true
		return nil
	})
	if err != nil && held {
		b.dropAnswer(run) // its record did not commit, so it is never delivered
	}
	return err
}

// peekAnswer returns the answer waiting for run under fence, leaving it waiting.
func (b *Broker) peekAnswer(run string, fence int64) answer {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.answers[run]
	if !ok || a.fence != fence {
		return answer{}
	}
	return a
}

// acknowledged forgets the answer to question that run's runner, under fence, gave its work.
func (b *Broker) acknowledged(run string, fence int64, question string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if a := b.answers[run]; a.fence == fence && a.question == question {
		delete(b.answers, run)
	}
}

func (b *Broker) dropAnswer(run string) {
	b.mu.Lock()
	delete(b.answers, run)
	b.mu.Unlock()
}

// Finish ends runner's run with its result.
func (b *Broker) Finish(ctx context.Context, runner, run string, fence int64, res Result) error {
	if !res.Outcome.Valid() {
		return fmt.Errorf("%w: outcome %q", ErrInvalid, res.Outcome)
	}
	if res.PR < 0 || (res.PR != 0 && res.Outcome != OutcomeDone) {
		return fmt.Errorf("%w: only a done run reports a pull request", ErrInvalid)
	}
	err := b.held(ctx, runner, run, fence, func(tx *eventstore.Tx, ri RunInfo) error {
		_, err := tx.Append(ctx, Stream(ri.Org, ri.Run), ri.Version, runnerActor(runner),
			Finished{Fence: fence, Outcome: res.Outcome, PR: res.PR, Reason: capText(res.Reason)})
		return err
	})
	if err == nil {
		b.dropAnswer(run)
	}
	return err
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
	if expired && err == nil {
		b.dropAnswer(run)
	}
	return expired, err
}

// Permit asks the policy engine whether the Pilot of runner's run may use a prompted tool, and
// records the decision, in one transaction with the lease check, so a fenced runner gets nothing.
// The Pilot is the actor, as a ship worker of the run's org; its tool call is agent output and the
// run's task is the trusted basis. The Pilot's own text never picks the actor, org or project:
// those come from the lease (LUCY-POL-001, LUCY-POL-004).
func (b *Broker) Permit(ctx context.Context, runner, run string, fence int64, p PermitRequest) (Permission, error) {
	if (p.Kind != policy.KindRead && p.Kind != policy.KindWrite) || p.Tool == "" || len(p.Tool) > 64 || len(p.Path) > 4096 {
		return Permission{}, fmt.Errorf("%w: a permit is a read or a write by a named tool", ErrInvalid)
	}
	var perm Permission
	err := b.held(ctx, runner, run, fence, func(tx *eventstore.Tx, ri RunInfo) error {
		req := policy.Request{
			Actor:  policy.Actor{ID: "pilot/" + ri.Run, Role: policy.RoleWorker, Org: ri.Org},
			Kind:   p.Kind,
			Target: policy.Target{Org: ri.Org, Project: ri.Project, Resource: p.Path},
			Inputs: []policy.Input{{Source: policy.SourceTask, Ref: "run/" + ri.Run}, {Source: policy.SourceAgent, Ref: "tool/" + p.Tool}},
		}
		d := policy.Evaluate(req, policy.Facts{})
		if _, err := tx.Append(ctx, policy.Stream, eventstore.AnyVersion, runnerActor(runner), policy.NewRecord(req, policy.Facts{}, d)); err != nil {
			return err // unrecorded, so it allows nothing
		}
		perm = Permission{Outcome: d.Outcome, Rule: d.Rule, Reason: d.Reason}
		return nil
	})
	if err != nil {
		return Permission{}, err
	}
	return perm, nil
}
