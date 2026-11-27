package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// RunInfo is a run as the broker's projection holds it.
type RunInfo struct {
	Task
	State     State
	Runner    string    // when leased or finished by a runner
	Placement Placement // the holder's placement
	Fence     int64     // the current or last lease's fence; 0 when never leased
	Expires   time.Time // when leased: the lease's expiry
	Accepted  int64     // the highest streamed event number recorded
	Activity  time.Time // when the run last did something: leased, streamed, or queued
	Asking    bool      // the last streamed event was a question
	Question  string    // when asking
	// QuestionID is the asked question's id, when asking; Answered is the id of the last
	// question whose answer a heartbeat handed to the runner under Fence.
	QuestionID string
	Answered   string
	Stop       bool   // a stop was requested
	PR         int    // when done
	Reason     string // when failed, stopped or lost
	Version    int64  // the run stream's version
	QueuedSeq  int64  // the queued event's sequence number: the queue's order
}

// Projector maintains the runner_runs table from the run streams. Register it with the store
// that the Broker uses.
type Projector struct{}

// Name implements eventstore.Projector.
func (Projector) Name() string { return "runner_runs" }

// Version implements eventstore.Projector.
func (Projector) Version() int { return 2 }

// Reset implements eventstore.Projector.
func (Projector) Reset(ctx context.Context, tx eventstore.Execer) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS runner_runs`,
		`CREATE TABLE runner_runs (
			run         TEXT PRIMARY KEY,
			version     INTEGER NOT NULL,
			queued_seq  INTEGER NOT NULL,
			org         TEXT NOT NULL,
			project     TEXT NOT NULL,
			flight      TEXT NOT NULL,
			generation  INTEGER NOT NULL,
			goal        TEXT NOT NULL,
			base        TEXT NOT NULL,
			state       TEXT NOT NULL,
			runner      TEXT NOT NULL DEFAULT '',
			placement   TEXT NOT NULL DEFAULT '',
			fence       INTEGER NOT NULL DEFAULT 0,
			expires_ns  INTEGER NOT NULL DEFAULT 0,
			accepted    INTEGER NOT NULL DEFAULT 0,
			activity_ns INTEGER NOT NULL,
			asking      INTEGER NOT NULL DEFAULT 0,
			question    TEXT NOT NULL DEFAULT '',
			question_id TEXT NOT NULL DEFAULT '',
			answered    TEXT NOT NULL DEFAULT '',
			stop        INTEGER NOT NULL DEFAULT 0,
			pr          INTEGER NOT NULL DEFAULT 0,
			reason      TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX runner_runs_state ON runner_runs (state, queued_seq)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// runOf returns the run id of a stream "run/<org>/<run>".
func runOf(stream string) (string, bool) {
	rest, ok := strings.CutPrefix(stream, StreamPrefix)
	if !ok {
		return "", false
	}
	_, run, ok := strings.Cut(rest, "/")
	return run, ok && run != ""
}

// Apply implements eventstore.Projector.
func (Projector) Apply(ctx context.Context, tx eventstore.Execer, e eventstore.Event) error {
	if !strings.HasPrefix(e.Type, "run.") {
		return nil
	}
	run, ok := runOf(e.Stream)
	if !ok {
		return fmt.Errorf("runner: %s event on stream %q", e.Type, e.Stream)
	}
	decode := func(v any) error { return json.Unmarshal(e.Payload, v) }
	at := e.Time.UnixNano()
	var q string
	var args []any
	switch e.Type {
	case Queued{}.EventType():
		var p Queued
		if err := decode(&p); err != nil {
			return err
		}
		q = `INSERT INTO runner_runs (run, version, queued_seq, org, project, flight, generation, goal, base, state, activity_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		args = []any{run, e.StreamVersion, e.Seq, p.Org, p.Project, p.Flight, p.Generation, p.Goal, p.Base, string(StateQueued), at}
	case Leased{}.EventType():
		var p Leased
		if err := decode(&p); err != nil {
			return err
		}
		q = `UPDATE runner_runs SET version = ?, state = ?, runner = ?, placement = ?, fence = ?, expires_ns = ?,
			accepted = 0, activity_ns = ?, asking = 0, question = '', question_id = '', answered = '' WHERE run = ?`
		args = []any{e.StreamVersion, string(StateLeased), p.Runner, string(p.Placement), e.Seq, p.ExpiresAt.UnixNano(), at, run}
	case Renewed{}.EventType():
		var p Renewed
		if err := decode(&p); err != nil {
			return err
		}
		q = `UPDATE runner_runs SET version = ?, expires_ns = ?, answered = CASE WHEN ? = '' THEN answered ELSE ? END WHERE run = ?`
		args = []any{e.StreamVersion, p.ExpiresAt.UnixNano(), p.Answered, p.Answered, run}
	case Streamed{}.EventType():
		var p Streamed
		if err := decode(&p); err != nil {
			return err
		}
		asking, question, id := 0, "", ""
		if p.Kind == KindAsking {
			asking, question, id = 1, p.Text, p.Question
		}
		q = `UPDATE runner_runs SET version = ?, accepted = ?, activity_ns = ?, asking = ?, question = ?, question_id = ? WHERE run = ?`
		args = []any{e.StreamVersion, p.Seq, at, asking, question, id, run}
	case Answered{}.EventType():
		q = `UPDATE runner_runs SET version = ?, activity_ns = ?, asking = 0, question = '', question_id = '' WHERE run = ?`
		args = []any{e.StreamVersion, at, run}
	case StopRequested{}.EventType():
		q = `UPDATE runner_runs SET version = ?, stop = 1 WHERE run = ?`
		args = []any{e.StreamVersion, run}
	case Finished{}.EventType():
		var p Finished
		if err := decode(&p); err != nil {
			return err
		}
		q = `UPDATE runner_runs SET version = ?, state = ?, pr = ?, reason = ?, asking = 0, question = '' WHERE run = ?`
		args = []any{e.StreamVersion, string(finalState(p.Outcome)), p.PR, p.Reason, run}
	case Expired{}.EventType():
		q = `UPDATE runner_runs SET version = ?, state = ?, reason = ?, asking = 0, question = '' WHERE run = ?`
		args = []any{e.StreamVersion, string(StateLost), "the lease expired", run}
	default:
		return nil
	}
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// finalState is the state an outcome leads to.
func finalState(o Outcome) State {
	switch o {
	case OutcomeDone:
		return StateDone
	case OutcomeStopped:
		return StateStopped
	case OutcomeLost:
		return StateLost
	}
	return StateFailed
}

const runColumns = `run, version, queued_seq, org, project, flight, generation, goal, base, state, runner, placement,
	fence, expires_ns, accepted, activity_ns, asking, question, question_id, answered, stop, pr, reason`

type rowScanner interface{ Scan(dest ...any) error }

func scanRun(r rowScanner) (RunInfo, error) {
	var ri RunInfo
	var state, placement string
	var expires, activity int64
	var asking, stop int
	err := r.Scan(&ri.Run, &ri.Version, &ri.QueuedSeq, &ri.Org, &ri.Project, &ri.Flight, &ri.Generation, &ri.Goal,
		&ri.Base, &state, &ri.Runner, &placement, &ri.Fence, &expires, &ri.Accepted, &activity, &asking,
		&ri.Question, &ri.QuestionID, &ri.Answered, &stop, &ri.PR, &ri.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		return RunInfo{}, ErrNoRun
	}
	if err != nil {
		return RunInfo{}, err
	}
	ri.State, ri.Placement = State(state), Placement(placement)
	if expires != 0 {
		ri.Expires = time.Unix(0, expires).UTC()
	}
	ri.Activity = time.Unix(0, activity).UTC()
	ri.Asking, ri.Stop = asking != 0, stop != 0
	return ri, nil
}
