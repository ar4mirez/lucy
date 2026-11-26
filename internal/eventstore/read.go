package eventstore

import (
	"context"
	"fmt"
	"time"
)

// Read limits: a limit of 0 or less means DefaultReadLimit; more than MaxReadLimit is capped.
const (
	DefaultReadLimit = 500
	MaxReadLimit     = 5000
)

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultReadLimit
	case n > MaxReadLimit:
		return MaxReadLimit
	}
	return n
}

const eventColumns = `seq, stream, stream_version, type, type_version, envelope_version, occurred_at, actor, payload`

// ReadAfter returns up to limit events with a sequence number greater than afterSeq, in log order.
// Reading from 0 replays the whole log.
func (s *Store) ReadAfter(ctx context.Context, afterSeq int64, limit int) ([]Event, error) {
	return readAfter(ctx, s.reader, afterSeq, limit)
}

// ReadStream returns up to limit events of one stream after afterVersion, in stream order.
func (s *Store) ReadStream(ctx context.Context, stream string, afterVersion int64, limit int) ([]Event, error) {
	rows, err := s.reader.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE stream = ? AND stream_version > ? ORDER BY stream_version LIMIT ?`,
		stream, afterVersion, clampLimit(limit))
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// Replay calls fn for every event after afterSeq, in log order, reading in batches.
func (s *Store) Replay(ctx context.Context, afterSeq int64, fn func(Event) error) error {
	for {
		batch, err := s.ReadAfter(ctx, afterSeq, MaxReadLimit)
		if err != nil {
			return err
		}
		for _, e := range batch {
			if err := fn(e); err != nil {
				return err
			}
		}
		if len(batch) < MaxReadLimit {
			return nil
		}
		afterSeq = batch[len(batch)-1].Seq
	}
}

// Head returns the sequence number of the last event in the log; 0 if it is empty.
func (s *Store) Head(ctx context.Context) (int64, error) {
	return head(ctx, s.reader)
}

// StreamVersion returns a stream's current version; 0 if it does not exist.
func (s *Store) StreamVersion(ctx context.Context, stream string) (int64, error) {
	return streamVersion(ctx, s.reader, stream)
}

func head(ctx context.Context, q querier) (int64, error) {
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events`).Scan(&seq)
	return seq, err
}

func readAfter(ctx context.Context, q querier, afterSeq int64, limit int) ([]Event, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM events WHERE seq > ? ORDER BY seq LIMIT ?`,
		afterSeq, clampLimit(limit))
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

func scanEvents(rows rowScanner) ([]Event, error) {
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var at int64
		var payload string
		if err := rows.Scan(&e.Seq, &e.Stream, &e.StreamVersion, &e.Type, &e.TypeVersion, &e.EnvelopeVersion, &at, &e.Actor, &payload); err != nil {
			return nil, err
		}
		if e.EnvelopeVersion > EnvelopeVersion {
			return nil, fmt.Errorf("%w: event %d has envelope version %d, this build reads up to %d",
				ErrSchemaTooNew, e.Seq, e.EnvelopeVersion, EnvelopeVersion)
		}
		e.Time = time.Unix(0, at).UTC()
		e.Payload = []byte(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}
