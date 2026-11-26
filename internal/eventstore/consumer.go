package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Consumers read the log past a cursor and move the cursor with an ack. Claiming a consumer starts
// a new generation; an ack carries the generation it was read under and is refused once another
// claim has replaced it. So a supervisor interrupted before its ack leaves the events unacked and
// durable, and a stale supervisor cannot ack work it did not finish (LUCY-EVT-003).

// Errors returned by consumer operations.
var (
	ErrStaleGeneration = errors.New("eventstore: consumer generation is stale")
	ErrUnknownConsumer = errors.New("eventstore: consumer has not been claimed")
	ErrAckBeyondHead   = errors.New("eventstore: ack is past the end of the log")
)

// Lease is a consumer's claim: its generation and the cursor it starts from.
type Lease struct {
	Consumer   string
	Generation int64
	Cursor     int64 // the last acked sequence number
}

// Claim starts a new generation for a consumer, creating it with its cursor at 0 if it is new, and
// returns the lease. Any earlier generation's acks are refused from now on.
func (s *Store) Claim(ctx context.Context, consumer string) (Lease, error) {
	var l Lease
	err := s.Update(ctx, func(tx *Tx) error {
		var err error
		l, err = tx.Claim(ctx, consumer)
		return err
	})
	return l, err
}

// Claim is Store.Claim inside a transaction.
func (tx *Tx) Claim(ctx context.Context, consumer string) (Lease, error) {
	if tx.done {
		return Lease{}, ErrClosed
	}
	if err := checkLabel("consumer", consumer, MaxStreamBytes, false); err != nil {
		return Lease{}, err
	}
	l := Lease{Consumer: consumer}
	err := tx.tx.QueryRowContext(ctx,
		`INSERT INTO consumers (name, generation, cursor_seq, updated_at) VALUES (?, 1, 0, ?)
		 ON CONFLICT (name) DO UPDATE SET generation = generation + 1, updated_at = excluded.updated_at
		 RETURNING generation, cursor_seq`,
		consumer, tx.s.now().UTC().UnixNano()).Scan(&l.Generation, &l.Cursor)
	if err != nil {
		return Lease{}, err
	}
	return l, nil
}

// Ack moves a consumer's cursor to throughSeq if generation is still current. Acking at or below
// the cursor is a no-op, so a repeated ack is harmless; the cursor never moves backwards.
func (s *Store) Ack(ctx context.Context, consumer string, throughSeq, generation int64) error {
	return s.Update(ctx, func(tx *Tx) error { return tx.Ack(ctx, consumer, throughSeq, generation) })
}

// Ack is Store.Ack inside a transaction, so a consumer can append the events its work produced and
// ack the events that caused them atomically.
func (tx *Tx) Ack(ctx context.Context, consumer string, throughSeq, generation int64) error {
	if tx.done {
		return ErrClosed
	}
	var current, cursor int64
	err := tx.tx.QueryRowContext(ctx, `SELECT generation, cursor_seq FROM consumers WHERE name = ?`, consumer).Scan(&current, &cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %q", ErrUnknownConsumer, consumer)
	}
	if err != nil {
		return err
	}
	if generation != current {
		return fmt.Errorf("%w: %q is at generation %d, ack carries %d", ErrStaleGeneration, consumer, current, generation)
	}
	if throughSeq <= cursor {
		return nil
	}
	h, err := head(ctx, tx.tx)
	if err != nil {
		return err
	}
	if throughSeq > h {
		return fmt.Errorf("%w: ack through %d, log ends at %d", ErrAckBeyondHead, throughSeq, h)
	}
	_, err = tx.tx.ExecContext(ctx,
		`UPDATE consumers SET cursor_seq = ?, updated_at = ? WHERE name = ? AND generation = ?`,
		throughSeq, tx.s.now().UTC().UnixNano(), consumer, generation)
	return err
}

// Pending returns up to limit events past a consumer's cursor.
func (s *Store) Pending(ctx context.Context, consumer string, limit int) ([]Event, error) {
	var cursor int64
	err := s.reader.QueryRowContext(ctx, `SELECT cursor_seq FROM consumers WHERE name = ?`, consumer).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownConsumer, consumer)
	}
	if err != nil {
		return nil, err
	}
	return s.ReadAfter(ctx, cursor, limit)
}
