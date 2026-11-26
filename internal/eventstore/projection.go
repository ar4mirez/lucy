package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

// Projector maintains state tables derived from the log. lucy's state is projections: a projector's
// tables can be dropped and rebuilt from the events at any time, so they never hold anything the log
// does not.
//
// Apply runs inside every append transaction, after the events are inserted, so an event and the
// state change it describes commit together (LUCY-EVT-002); an Apply error rolls the append back.
// Projectors must be deterministic: Apply may depend only on the event and the projector's own
// tables, never on the clock, the network or another projector's tables. All SQL uses parameters.
type Projector interface {
	// Name identifies the projection; lower case letters, digits and underscores. Prefix its tables
	// with it.
	Name() string
	// Version is bumped whenever the tables or Apply's logic change; the store then rebuilds the
	// projection when it opens.
	Version() int
	// Reset drops and recreates the projection's tables, leaving them empty.
	Reset(ctx context.Context, tx Execer) error
	// Apply folds one event into the tables.
	Apply(ctx context.Context, tx Execer, e Event) error
}

// Execer is the transaction a projector writes through.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var projectorName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// ErrUnknownProjection is returned by Rebuild for a name no projector has.
var ErrUnknownProjection = errors.New("eventstore: unknown projection")

func checkProjectors(ps []Projector) error {
	seen := map[string]bool{}
	for _, p := range ps {
		if p == nil {
			return errors.New("eventstore: nil projector")
		}
		n := p.Name()
		if !projectorName.MatchString(n) {
			return fmt.Errorf("eventstore: projector name %q must match %s", n, projectorName)
		}
		if seen[n] {
			return fmt.Errorf("eventstore: projector %q registered twice", n)
		}
		if p.Version() < 1 {
			return fmt.Errorf("eventstore: projector %q version must be at least 1", n)
		}
		seen[n] = true
	}
	return nil
}

func applyAll(ctx context.Context, tx *sql.Tx, p Projector, events []Event) error {
	for _, e := range events {
		if err := p.Apply(ctx, tx, e); err != nil {
			return fmt.Errorf("eventstore: projector %q at seq %d: %w", p.Name(), e.Seq, err)
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE projections SET last_seq = ? WHERE name = ?`, events[len(events)-1].Seq, p.Name())
	return err
}

// syncProjections rebuilds every projection that is new, whose version changed, or that is behind
// the log (which only a crash between versions of lucy could cause).
func (s *Store) syncProjections(ctx context.Context) error {
	h, err := head(ctx, s.writer)
	if err != nil {
		return err
	}
	for _, p := range s.projectors {
		var version, last int64
		err := s.writer.QueryRowContext(ctx, `SELECT version, last_seq FROM projections WHERE name = ?`, p.Name()).Scan(&version, &last)
		missing := errors.Is(err, sql.ErrNoRows)
		if err != nil && !missing {
			return err
		}
		if missing || version != int64(p.Version()) || last != h {
			if err := s.rebuild(ctx, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// Rebuild drops a projection's tables and replays the whole log into them, in one transaction:
// readers see the old state until the new one commits.
func (s *Store) Rebuild(ctx context.Context, name string) error {
	for _, p := range s.projectors {
		if p.Name() == name {
			return s.rebuild(ctx, p)
		}
	}
	return fmt.Errorf("%w: %q", ErrUnknownProjection, name)
}

func (s *Store) rebuild(ctx context.Context, p Projector) error {
	return s.Update(ctx, func(tx *Tx) error {
		if err := p.Reset(ctx, tx.tx); err != nil {
			return fmt.Errorf("eventstore: reset projection %q: %w", p.Name(), err)
		}
		if _, err := tx.tx.ExecContext(ctx,
			`INSERT INTO projections (name, version, last_seq) VALUES (?, ?, 0)
			 ON CONFLICT (name) DO UPDATE SET version = excluded.version, last_seq = 0`,
			p.Name(), p.Version()); err != nil {
			return err
		}
		var after int64
		for {
			// Read a batch, close the rows, then apply: the transaction has one connection.
			batch, err := readAfter(ctx, tx.tx, after, MaxReadLimit)
			if err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			if err := applyAll(ctx, tx.tx, p, batch); err != nil {
				return err
			}
			after = batch[len(batch)-1].Seq
		}
	})
}
