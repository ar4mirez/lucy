package runner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/harness"
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
	store    *eventstore.Store
	now      func() time.Time
	verifier PlacementVerifier // nil: every container lease is refused

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

// VerifyPlacementWith makes v the check on container leases' placement proofs. Without one, every
// lease to a runner placed in a container is refused (ErrPlacementProof).
func (b *Broker) VerifyPlacementWith(v PlacementVerifier) { b.verifier = v }

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

// LiveInContainer returns the runs leased by a runner placed in org's container, sorted: the
// sessions that starting, restarting or stopping that container would stop (LUCY-SBX-003). A
// lease that has run out but is not yet recorded as expired still counts, so a session is never
// missed because the daemon has not looked at it yet.
func (b *Broker) LiveInContainer(ctx context.Context, org string) ([]string, error) {
	rows, err := b.store.QueryContext(ctx, `SELECT run FROM runner_runs WHERE org = ? AND state = ? AND placement = ?
		ORDER BY run`, org, string(StateLeased), string(PlacementContainer))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []string
	for rows.Next() {
		var run string
		if err := rows.Scan(&run); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// HoldContainer holds org's container for an operation that stops it, if the runs live in it are
// exactly live (sorted, as LiveInContainer returns them): the check and the hold are one
// transaction, so no run is placed in the container between them. Until ReleaseContainer or ttl
// passes, Lease refuses a runner placed in a container a run of org (ErrSandboxBusy). held is
// false, and nothing is recorded, when the live runs are not live.
func (b *Broker) HoldContainer(ctx context.Context, org string, live []string, ttl time.Duration) (held bool, err error) {
	err = b.store.Update(ctx, func(tx *eventstore.Tx) error {
		var now sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT group_concat(run, char(10)) FROM (SELECT run FROM runner_runs
			WHERE org = ? AND state = ? AND placement = ? ORDER BY run)`, org, string(StateLeased), string(PlacementContainer)).Scan(&now); err != nil {
			return err
		}
		if now.String != strings.Join(live, "\n") {
			return nil
		}
		held = true
		_, err := tx.Append(ctx, ContainerStream(org), eventstore.AnyVersion, ActorDaemon,
			ContainerHeld{Org: org, ExpiresAt: b.now().Add(ttl).UTC()})
		return err
	})
	if err != nil {
		return false, err
	}
	return held, nil
}

// ReleaseContainer ends the hold on org's container.
func (b *Broker) ReleaseContainer(ctx context.Context, org string) error {
	_, err := b.store.Append(ctx, ContainerStream(org), eventstore.AnyVersion, ActorDaemon, ContainerReleased{Org: org})
	return err
}

// OrgActivity says whether org has work for its container and when it last had any: busy when a
// run of org is queued (any runner may take it) or leased by a runner placed in its container;
// last is the latest moment any run of org did something or ended, zero when it has had none.
// An idle container (not busy, last long ago) is one the daemon may stop (LUCY-SBX-001).
func (b *Broker) OrgActivity(ctx context.Context, org string) (busy bool, last time.Time, err error) {
	var n, lastNS int64
	err = b.store.QueryRowContext(ctx, `SELECT
			count(CASE WHEN state = ? OR (state = ? AND placement = ?) THEN 1 END),
			coalesce(max(max(activity_ns, ended_ns)), 0)
		FROM runner_runs WHERE org = ?`,
		string(StateQueued), string(StateLeased), string(PlacementContainer), org).Scan(&n, &lastNS)
	if err != nil {
		return false, time.Time{}, err
	}
	if lastNS > 0 {
		last = time.Unix(0, lastNS).UTC()
	}
	return n > 0, last, nil
}

// Queued returns how many runs wait for a runner placed at placement (PlacementHost when empty),
// of org only when org is not empty. It is a read: a runner polls it, and leases only when there
// is something to lease, so an idle runner adds nothing to the log. A runner placed in a container
// is not counted the runs of an org whose container is held, as Lease would not give it them.
func (b *Broker) Queued(ctx context.Context, placement Placement, org string) (int, error) {
	if placement == "" {
		placement = PlacementHost
	}
	if !placement.Valid() {
		return 0, fmt.Errorf("%w: placement %q", ErrInvalid, placement)
	}
	if org != "" {
		if err := checkOrg(org); err != nil {
			return 0, err
		}
	}
	var n int
	err := b.store.QueryRowContext(ctx, `SELECT count(*) FROM runner_runs WHERE state = ? AND `+leasable,
		string(StateQueued), org, org, string(placement), string(PlacementContainer), b.now().UnixNano()).Scan(&n)
	return n, err
}

// leasable is the condition on a queued run that a runner may be leased it: the runner's org
// (empty for any) twice, its placement, then PlacementContainer, then the time in Unix nanoseconds
// are its arguments.
const leasable = `(? = '' OR org = ?) AND (? != ? OR org NOT IN (SELECT org FROM runner_holds WHERE expires_ns > ?))`

// Lease gives runner the oldest queued run of org, under a new fence that expires after ttl
// (clamped to MinTTL..MaxTTL; DefaultTTL when zero). ok is false when nothing is queued. Every
// runner names its org (ErrRunnerOrg otherwise). On the host it must be the host org: the first
// run leased to the host sets it, recorded, and a host lease for any other org is refused
// (ErrHostOrg). A runner placed in a container is leased only with a proof of that placement the
// broker's PlacementVerifier accepts (ErrPlacementProof otherwise), and is refused (ErrSandboxBusy)
// while org's container is held.
func (b *Broker) Lease(ctx context.Context, runner string, placement Placement, org, proof string, ttl time.Duration) (l Lease, ok bool, err error) {
	if err := CheckRunnerID(runner); err != nil {
		return Lease{}, false, err
	}
	if !placement.Valid() {
		return Lease{}, false, fmt.Errorf("%w: placement %q", ErrInvalid, placement)
	}
	if org == "" {
		return Lease{}, false, fmt.Errorf("%w: runner %q names no org", ErrRunnerOrg, runner)
	}
	if err := checkOrg(org); err != nil {
		return Lease{}, false, err
	}
	if placement == PlacementContainer {
		if b.verifier == nil {
			return Lease{}, false, ErrPlacementProof
		}
		if err := b.verifier.VerifyPlacement(ctx, runner, org, proof); err != nil {
			return Lease{}, false, fmt.Errorf("%w: %w", ErrPlacementProof, err)
		}
	}
	ttl = clampTTL(ttl)
	err = b.store.Update(ctx, func(tx *eventstore.Tx) error {
		now := b.now()
		var host string
		if placement == PlacementHost {
			h, err := hostOrg(ctx, tx)
			if err != nil {
				return err
			}
			host = h
			if host != "" && host != org {
				return fmt.Errorf("%w: host placement serves org %q, not %q", ErrHostOrg, host, org)
			}
		}
		ri, err := scanRun(tx.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runner_runs WHERE state = ?
			AND `+leasable+` ORDER BY queued_seq LIMIT 1`, string(StateQueued), org, org, string(placement), string(PlacementContainer), now.UnixNano()))
		if errors.Is(err, ErrNoRun) {
			if placement != PlacementContainer {
				return nil
			}
			var queued int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runner_runs WHERE state = ? AND org = ?`,
				string(StateQueued), org).Scan(&queued); err != nil {
				return err
			}
			if queued > 0 {
				return ErrSandboxBusy
			}
			return nil
		}
		if err != nil {
			return err
		}
		if placement == PlacementHost && host == "" {
			if _, err := tx.Append(ctx, HostOrgStream, eventstore.AnyVersion, runnerActor(runner), HostOrgSet{Org: org}); err != nil {
				return err
			}
		}
		exp := now.Add(ttl).UTC()
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

// HostOrg returns the org whose runs runners placed on the host lease, empty until the first host
// lease or SetHostOrg sets it.
func (b *Broker) HostOrg(ctx context.Context) (string, error) {
	return hostOrg(ctx, b.store)
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func hostOrg(ctx context.Context, q rowQuerier) (string, error) {
	var org string
	err := q.QueryRowContext(ctx, `SELECT org FROM runner_host_org WHERE id = 1`).Scan(&org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return org, err
}

// SetHostOrg makes org the host org, recorded under actor: the owner's command. It is refused
// (ErrHostOrg) while a run leased by a runner on the host is live, so no host run of the old org
// overlaps one of the new.
func (b *Broker) SetHostOrg(ctx context.Context, org, actor string) error {
	if err := checkOrg(org); err != nil {
		return err
	}
	return b.store.Update(ctx, func(tx *eventstore.Tx) error {
		host, err := hostOrg(ctx, tx)
		if err != nil || host == org {
			return err
		}
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runner_runs WHERE state = ? AND placement = ?`,
			string(StateLeased), string(PlacementHost)).Scan(&live); err != nil {
			return err
		}
		if live > 0 {
			return fmt.Errorf("%w: %d host runs of org %q are live; stop them or let them finish first", ErrHostOrg, live, host)
		}
		_, err = tx.Append(ctx, HostOrgStream, eventstore.AnyVersion, actor, HostOrgSet{Org: org})
		return err
	})
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
		if (ev.Kind == KindHook) != (ev.Hook != "") || (ev.Hook != "" && !slices.Contains(harness.Hooks, ev.Hook)) {
			return Beat{}, fmt.Errorf("%w: event %d: a hook event, and only a hook event, names a known hook, not %q", ErrInvalid, ev.Seq, ev.Hook)
		}
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
			payloads = append(payloads, Streamed{Fence: fence, Seq: ev.Seq, Kind: ev.Kind, Text: capText(ev.Text), Question: ev.Question, Hook: ev.Hook})
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
	return b.expireLeases(ctx)
}

// DefaultGrace is how long a restarted daemon waits for a held lease's runner before expiring it:
// two lease lives, so a runner that kept working while the daemon was down has time to reconnect.
const DefaultGrace = 2 * DefaultTTL

// Resume is what a daemon does on start, before it expires anything: every run still leased gets
// a grace window, until grace (DefaultGrace when zero) from now, unless its lease already runs
// longer. A lease that ran out while the daemon was down was not renewed only because no daemon
// was there to hear the heartbeats; its runner may still be working, and it reattaches under the
// same fence. A runner that is really gone is expired once the grace window ends (LUCY-OPS-005,
// LUCY-RUN-005). It returns how many leases it extended, each recorded as run.resumed.
//
// The window keeps the run, not the runner's authority: Earned stays what the runner last earned,
// so nothing that trusts a lease (a capability token) trusts it past that until the runner renews
// it. A lease already in a window is not extended again, so a daemon that restarts over and over
// cannot keep a dead runner's run from being replaced.
func (b *Broker) Resume(ctx context.Context, grace time.Duration) (int, error) {
	if grace <= 0 {
		grace = DefaultGrace
	}
	until := b.now().Add(grace).UTC()
	n := 0
	err := b.store.Update(ctx, func(tx *eventstore.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+runColumns+` FROM runner_runs WHERE state = ? AND expires_ns <= earned_ns
			AND expires_ns < ? ORDER BY queued_seq`, string(StateLeased), until.UnixNano())
		if err != nil {
			return err
		}
		var held []RunInfo
		for rows.Next() {
			ri, err := scanRun(rows)
			if err != nil {
				_ = rows.Close() // the scan error is what matters
				return err
			}
			held = append(held, ri)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, ri := range held {
			if _, err := tx.Append(ctx, Stream(ri.Org, ri.Run), ri.Version, ActorDaemon, Resumed{Fence: ri.Fence, ExpiresAt: until}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (b *Broker) expireLeases(ctx context.Context) (int, error) {
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
	if !permitKinds[p.Kind] || p.Tool == "" || len(p.Tool) > 64 || len(p.Resource) > MaxPermitResource {
		return Permission{}, fmt.Errorf("%w: a permit is a read, write, exec or fetch by a named tool, on at most %d bytes", ErrInvalid, MaxPermitResource)
	}
	var perm Permission
	err := b.held(ctx, runner, run, fence, func(tx *eventstore.Tx, ri RunInfo) error {
		req := policy.Request{
			Actor:  policy.Actor{ID: "pilot/" + ri.Run, Role: policy.RoleWorker, Org: ri.Org},
			Kind:   p.Kind,
			Target: policy.Target{Org: ri.Org, Project: ri.Project, Resource: p.Resource},
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

// permitKinds are the actions a Pilot's prompted tool may be asked about.
var permitKinds = map[policy.Kind]bool{policy.KindRead: true, policy.KindWrite: true, policy.KindExec: true, policy.KindFetch: true}
