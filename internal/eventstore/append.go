package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Expected-version values for Append besides an exact stream version.
const (
	// AnyVersion appends whatever the stream's current version is.
	AnyVersion int64 = -1
	// NoStream requires the stream not to exist yet.
	NoStream int64 = 0
)

// ErrConflict is returned (wrapped in a *ConflictError) when an append's expected version is not the
// stream's current version: someone else appended first. Re-read the stream and decide again.
var ErrConflict = errors.New("eventstore: stream version conflict")

// ErrClosed is returned when the transaction function is used after it returned.
var ErrClosed = errors.New("eventstore: transaction is finished")

// ConflictError reports an optimistic-concurrency conflict on one stream.
type ConflictError struct {
	Stream   string
	Expected int64
	Actual   int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("eventstore: stream %q is at version %d, expected %d", e.Stream, e.Actual, e.Expected)
}

// Is makes errors.Is(err, ErrConflict) true.
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// Tx is one write transaction. Everything done through it, every append and the projector updates
// they cause and every ack, commits together or not at all.
type Tx struct {
	s      *Store
	tx     *sql.Tx
	done   bool
	broken error // set when a write failed partway; the transaction can then only roll back
}

// Update runs fn in one write transaction and commits if it returns nil. Writes are serialized, so
// fn should be short and must not wait on anything outside the store.
func (s *Store) Update(ctx context.Context, fn func(tx *Tx) error) error {
	sqlTx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tx := &Tx{s: s, tx: sqlTx}
	defer func() {
		tx.done = true
		sqlTx.Rollback() //nolint:errcheck // a no-op after Commit
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if tx.broken != nil {
		return fmt.Errorf("eventstore: transaction rolled back after a failed write: %w", tx.broken)
	}
	return sqlTx.Commit()
}

// Append appends events to one stream in their own transaction. See Tx.Append.
func (s *Store) Append(ctx context.Context, stream string, expected int64, actor string, payloads ...Payload) ([]Event, error) {
	var out []Event
	err := s.Update(ctx, func(tx *Tx) error {
		var err error
		out, err = tx.Append(ctx, stream, expected, actor, payloads...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Append appends events to a stream if the stream is at the expected version (or NoStream, or
// AnyVersion), runs every projector over them, and returns them with their sequence numbers.
// On a conflict it returns a *ConflictError and appends nothing.
func (tx *Tx) Append(ctx context.Context, stream string, expected int64, actor string, payloads ...Payload) ([]Event, error) {
	if tx.done {
		return nil, ErrClosed
	}
	if tx.broken != nil {
		return nil, tx.broken
	}
	if err := checkLabel("stream", stream, MaxStreamBytes, false); err != nil {
		return nil, err
	}
	if err := checkLabel("actor", actor, MaxActorBytes, true); err != nil {
		return nil, err
	}
	if expected < AnyVersion {
		return nil, fmt.Errorf("%w: expected version %d", ErrInvalidEvent, expected)
	}
	if len(payloads) == 0 || len(payloads) > MaxEventsPerAppend {
		return nil, fmt.Errorf("%w: %d events in one append; want 1 to %d", ErrInvalidEvent, len(payloads), MaxEventsPerAppend)
	}
	enc := make([]encoded, len(payloads))
	for i, p := range payloads {
		k, data, err := tx.s.reg.encode(p)
		if err != nil {
			return nil, err
		}
		enc[i] = encoded{k, data}
	}

	current, err := streamVersion(ctx, tx.tx, stream)
	if err != nil {
		return nil, err
	}
	if expected != AnyVersion && expected != current {
		return nil, &ConflictError{Stream: stream, Expected: expected, Actual: current}
	}

	out, err := tx.write(ctx, stream, current, actor, enc)
	if err != nil {
		tx.broken = err
		return nil, err
	}
	return out, nil
}

type encoded struct {
	key  typeKey
	data []byte
}

// write inserts validated events after the version check and runs the projectors. An error here
// leaves the transaction partly written, so the caller marks it broken.
func (tx *Tx) write(ctx context.Context, stream string, current int64, actor string, enc []encoded) ([]Event, error) {
	at := time.Unix(0, tx.s.now().UnixNano()).UTC() // exactly what a later read returns
	out := make([]Event, len(enc))
	for i, e := range enc {
		ev := Event{
			Stream:          stream,
			StreamVersion:   current + int64(i) + 1,
			Type:            e.key.name,
			TypeVersion:     e.key.version,
			EnvelopeVersion: EnvelopeVersion,
			Time:            at,
			Actor:           actor,
			Payload:         e.data,
		}
		err := tx.tx.QueryRowContext(ctx,
			`INSERT INTO events (stream, stream_version, type, type_version, envelope_version, occurred_at, actor, payload)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING seq`,
			ev.Stream, ev.StreamVersion, ev.Type, ev.TypeVersion, ev.EnvelopeVersion, at.UnixNano(), ev.Actor, string(ev.Payload),
		).Scan(&ev.Seq)
		if err != nil {
			return nil, fmt.Errorf("eventstore: append to %q: %w", stream, err)
		}
		out[i] = ev
	}
	last := out[len(out)-1].StreamVersion
	if _, err := tx.tx.ExecContext(ctx,
		`INSERT INTO streams (id, version) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET version = excluded.version`,
		stream, last); err != nil {
		return nil, err
	}
	for _, p := range tx.s.projectors {
		if err := applyAll(ctx, tx.tx, p, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// StreamVersion returns a stream's current version inside the transaction; 0 if it does not exist.
func (tx *Tx) StreamVersion(ctx context.Context, stream string) (int64, error) {
	if tx.done {
		return 0, ErrClosed
	}
	return streamVersion(ctx, tx.tx, stream)
}

// QueryRowContext runs a query inside the transaction, seeing its own uncommitted writes. It is for
// reading projections; state changes go through Append and the projectors.
func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.tx.QueryRowContext(ctx, query, args...)
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func streamVersion(ctx context.Context, q querier, stream string) (int64, error) {
	var v int64
	err := q.QueryRowContext(ctx, `SELECT version FROM streams WHERE id = ?`, stream).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}
