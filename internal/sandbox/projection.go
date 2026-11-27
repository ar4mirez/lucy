package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// Held is an operation on an org's container that waits on the owner: live sessions stop it until
// the owner confirms stopping exactly those sessions.
type Held struct {
	Org          string
	Op           Op
	DecisionSeq  int64          // the decision that is waiting; a confirmation names it
	Outcome      policy.Outcome // holding, or denied when an earlier confirmation lapsed
	LiveSessions []string       // the sessions that would stop
	Since        time.Time      // when it was decided
}

// Projector maintains the sandbox_held table from the "sandbox/<org>" streams: the latest
// request per org and operation that waits on the owner. Register it with the Sandbox's store.
type Projector struct{}

// Name implements eventstore.Projector.
func (Projector) Name() string { return "sandbox_held" }

// Version implements eventstore.Projector.
func (Projector) Version() int { return 1 }

// Reset implements eventstore.Projector.
func (Projector) Reset(ctx context.Context, tx eventstore.Execer) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS sandbox_held`,
		`CREATE TABLE sandbox_held (
			org           TEXT NOT NULL,
			op            TEXT NOT NULL,
			decision_seq  INTEGER NOT NULL,
			outcome       TEXT NOT NULL,
			live_sessions TEXT NOT NULL,
			since_ns      INTEGER NOT NULL,
			PRIMARY KEY (org, op)
		)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// Apply implements eventstore.Projector. A request replaces what was held for its operation: held
// again when it waits on the owner, gone when it was cleared or denied for another reason. A
// confirmation releases what it confirmed.
func (Projector) Apply(ctx context.Context, tx eventstore.Execer, e eventstore.Event) error {
	if !strings.HasPrefix(e.Type, "sandbox.") {
		return nil
	}
	org, ok := strings.CutPrefix(e.Stream, StreamPrefix)
	if !ok || org == "" {
		return fmt.Errorf("sandbox: %s event on stream %q", e.Type, e.Stream)
	}
	switch e.Type {
	case Requested{}.EventType():
		var p Requested
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if !p.NeedsOwner {
			_, err := tx.ExecContext(ctx, `DELETE FROM sandbox_held WHERE org = ? AND op = ?`, org, string(p.Op))
			return err
		}
		live, err := json.Marshal(p.LiveSessions)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sandbox_held (org, op, decision_seq, outcome, live_sessions, since_ns)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (org, op) DO UPDATE SET decision_seq = excluded.decision_seq,
			outcome = excluded.outcome, live_sessions = excluded.live_sessions, since_ns = excluded.since_ns`,
			org, string(p.Op), p.DecisionSeq, string(p.Outcome), string(live), e.Time.UnixNano())
		return err
	case Confirmed{}.EventType():
		var p Confirmed
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sandbox_held WHERE org = ? AND op = ? AND decision_seq = ?`,
			org, string(p.Op), p.DecisionSeq)
		return err
	}
	return nil
}

// Held returns every operation that waits on the owner, oldest first.
func (s *Sandbox) Held(ctx context.Context) ([]Held, error) {
	rows, err := s.store.QueryContext(ctx, `SELECT org, op, decision_seq, outcome, live_sessions, since_ns
		FROM sandbox_held ORDER BY since_ns, org, op`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []Held
	for rows.Next() {
		var h Held
		var op, outcome, live string
		var since int64
		if err := rows.Scan(&h.Org, &op, &h.DecisionSeq, &outcome, &live, &since); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(live), &h.LiveSessions); err != nil {
			return nil, err
		}
		h.Op, h.Outcome, h.Since = Op(op), policy.Outcome(outcome), time.Unix(0, since).UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}
