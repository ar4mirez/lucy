package eventstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAppendAndRead(t *testing.T) {
	s := openTest(t, "")
	ctx := context.Background()

	a := mustAppend(t, s, "task-1", NoStream, taskCreated{Title: "one", Org: "ar4mirez"}, taskNoted{Note: "n"})
	b := mustAppend(t, s, "task-2", NoStream, taskCreated{Title: "two"})
	c := mustAppend(t, s, "task-1", 2, taskNoted{Note: "m"})

	if a[0].Seq != 1 || a[1].Seq != 2 || b[0].Seq != 3 || c[0].Seq != 4 {
		t.Fatalf("seqs = %d %d %d %d, want 1 2 3 4", a[0].Seq, a[1].Seq, b[0].Seq, c[0].Seq)
	}
	if a[1].StreamVersion != 2 || c[0].StreamVersion != 3 || b[0].StreamVersion != 1 {
		t.Fatal("stream versions wrong")
	}
	if a[0].Actor != "test" || a[0].EnvelopeVersion != EnvelopeVersion || a[0].Type != "task.created" || a[0].TypeVersion != 1 {
		t.Fatalf("envelope = %+v", a[0])
	}
	if a[0].Time.IsZero() || !a[1].Time.Equal(a[0].Time) || !c[0].Time.After(a[0].Time) {
		t.Fatal("append times wrong: one append shares a time, later appends are later")
	}

	all, err := s.ReadAfter(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(append([]Event{}, a...), b...), c...)
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("ReadAfter(0) =\n%+v\nwant\n%+v", all, want)
	}
	tail, _ := s.ReadAfter(ctx, 2, 1)
	if len(tail) != 1 || tail[0].Seq != 3 {
		t.Fatalf("ReadAfter(2, 1) = %+v", tail)
	}
	st, _ := s.ReadStream(ctx, "task-1", 1, 0)
	if len(st) != 2 || st[0].Seq != 2 || st[1].Seq != 4 {
		t.Fatalf("ReadStream(task-1, 1) = %+v", st)
	}
	if h, _ := s.Head(ctx); h != 4 {
		t.Fatalf("Head = %d, want 4", h)
	}
	if v, _ := s.StreamVersion(ctx, "task-1"); v != 3 {
		t.Fatalf("StreamVersion = %d, want 3", v)
	}
	var n int
	if err := s.Replay(ctx, 1, func(Event) error { n++; return nil }); err != nil || n != 3 {
		t.Fatalf("Replay(1) visited %d, %v; want 3", n, err)
	}
}

func TestOptimisticConcurrency(t *testing.T) {
	s := openTest(t, "")
	ctx := context.Background()
	mustAppend(t, s, "s", NoStream, taskCreated{})

	_, err := s.Append(ctx, "s", NoStream, "test", taskNoted{})
	var ce *ConflictError
	if !errors.As(err, &ce) || !errors.Is(err, ErrConflict) || ce.Actual != 1 || ce.Expected != 0 || ce.Stream != "s" {
		t.Fatalf("NoStream on an existing stream: %v", err)
	}
	if _, err := s.Append(ctx, "s", 5, "test", taskNoted{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected 5 at version 1: %v", err)
	}
	if _, err := s.Append(ctx, "new", 1, "test", taskNoted{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected 1 on a new stream: %v", err)
	}
	mustAppend(t, s, "s", 1, taskNoted{})
	mustAppend(t, s, "s", AnyVersion, taskNoted{})
	if v, _ := s.StreamVersion(ctx, "s"); v != 3 {
		t.Fatalf("version = %d, want 3", v)
	}
	if h, _ := s.Head(ctx); h != 3 {
		t.Fatalf("head = %d, want 3: conflicts must append nothing", h)
	}
}

func TestUpdateIsAtomicAcrossStreams(t *testing.T) {
	s := openTest(t, "", &tally{})
	ctx := context.Background()
	mustAppend(t, s, "b", NoStream, taskCreated{})

	err := s.Update(ctx, func(tx *Tx) error {
		if _, err := tx.Append(ctx, "a", NoStream, "test", taskCreated{}); err != nil {
			return err
		}
		_, err := tx.Append(ctx, "b", NoStream, "test", taskCreated{}) // conflicts
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if v, _ := s.StreamVersion(ctx, "a"); v != 0 {
		t.Fatalf("stream a version = %d: the first append in a failed transaction must roll back", v)
	}
	if got := tallyRows(t, s); !reflect.DeepEqual(got, map[string]int64{"b": 1}) {
		t.Fatalf("tally = %v: projection must roll back with the events", got)
	}

	err = s.Update(ctx, func(tx *Tx) error {
		if _, err := tx.Append(ctx, "a", NoStream, "test", taskCreated{}); err != nil {
			return err
		}
		if v, err := tx.StreamVersion(ctx, "a"); err != nil || v != 1 {
			t.Errorf("in-transaction StreamVersion = %d, %v", v, err)
		}
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT events FROM tally_streams WHERE stream = ?`, "a").Scan(&n); err != nil || n != 1 {
			t.Errorf("in-transaction projection read = %d, %v", n, err)
		}
		_, err := tx.Append(ctx, "b", 1, "test", taskNoted{})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := tallyRows(t, s); !reflect.DeepEqual(got, map[string]int64{"a": 1, "b": 2}) {
		t.Fatalf("tally = %v", got)
	}
}

func TestFailedWriteCannotCommit(t *testing.T) {
	p := &tally{failOn: "task.noted"}
	s := openTest(t, "", p)
	ctx := context.Background()

	// The projector fails after the event row is written. Even if the caller swallows the error,
	// the transaction must not commit a half-applied append.
	err := s.Update(ctx, func(tx *Tx) error {
		if _, err := tx.Append(ctx, "a", NoStream, "test", taskCreated{}); err != nil {
			return err
		}
		if _, err := tx.Append(ctx, "a", 1, "test", taskNoted{}); err == nil {
			t.Error("projector failure was not reported")
		}
		if _, err := tx.Append(ctx, "z", NoStream, "test", taskCreated{}); err == nil {
			t.Error("append on a broken transaction succeeded")
		}
		return nil // swallowed
	})
	if err == nil {
		t.Fatal("Update committed after a failed write")
	}
	if h, _ := s.Head(ctx); h != 0 {
		t.Fatalf("head = %d, want 0", h)
	}
	if got := tallyRows(t, s); len(got) != 0 {
		t.Fatalf("tally = %v, want empty", got)
	}

	var saved *Tx
	if err := s.Update(ctx, func(tx *Tx) error { saved = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := saved.Append(ctx, "a", AnyVersion, "test", taskCreated{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("append after Update returned: %v, want ErrClosed", err)
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	s := openTest(t, "")
	mustAppend(t, s, "s", NoStream, taskCreated{Title: "t"})
	ctx := context.Background()
	if _, err := s.writer.ExecContext(ctx, `UPDATE events SET actor = 'mallory'`); err == nil {
		t.Fatal("UPDATE on events succeeded")
	}
	if _, err := s.writer.ExecContext(ctx, `DELETE FROM events`); err == nil {
		t.Fatal("DELETE on events succeeded")
	}
	if _, err := s.writer.ExecContext(ctx,
		`INSERT INTO events (stream, stream_version, type, type_version, envelope_version, occurred_at, actor, payload)
		 VALUES ('s', 1, 'task.created', 1, 1, 0, '', '{}')`); err == nil {
		t.Fatal("duplicate stream version accepted")
	}
	if _, err := s.writer.ExecContext(ctx,
		`INSERT INTO events (stream, stream_version, type, type_version, envelope_version, occurred_at, actor, payload)
		 VALUES ('s', 2, 'task.created', 1, 1, 0, '', 'not json')`); err == nil {
		t.Fatal("invalid JSON payload accepted")
	}
}

func TestProjectionRebuild(t *testing.T) {
	path := ""
	{
		s := openTest(t, path, &tally{})
		path = s.Path()
		mustAppend(t, s, "a", NoStream, taskCreated{}, taskNoted{})
		mustAppend(t, s, "b", NoStream, taskCreated{})
		s.Close()
	}
	// Opened without the projector, the store still appends; the projection falls behind.
	{
		s := openTest(t, path)
		mustAppend(t, s, "c", NoStream, taskCreated{})
		s.Close()
	}
	want := map[string]int64{"a": 2, "b": 1, "c": 1}
	// Reopened with it, the projection is behind the log and is rebuilt.
	s := openTest(t, path, &tally{})
	if got := tallyRows(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("after catch-up rebuild: %v, want %v", got, want)
	}
	s.Close()

	// A new projector version triggers a rebuild too.
	s = openTest(t, path, &tally{version: 2})
	if got := tallyRows(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("after version rebuild: %v, want %v", got, want)
	}
	var v, last int64
	if err := s.QueryRowContext(context.Background(), `SELECT version, last_seq FROM projections WHERE name = 'tally'`).Scan(&v, &last); err != nil || v != 2 || last != 4 {
		t.Fatalf("projections row = %d %d, %v", v, last, err)
	}
	if err := s.Rebuild(context.Background(), "tally"); err != nil {
		t.Fatal(err)
	}
	if got := tallyRows(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("after explicit rebuild: %v, want %v", got, want)
	}
	if err := s.Rebuild(context.Background(), "nope"); !errors.Is(err, ErrUnknownProjection) {
		t.Fatalf("Rebuild(nope) = %v", err)
	}
}

func TestProjectorValidation(t *testing.T) {
	ctx := context.Background()
	reg := testRegistry(t)
	for _, ps := range [][]Projector{{nil}, {&tally{}, &tally{}}, {&tally{version: -1}}} {
		if _, err := Open(ctx, t.TempDir()+"/lucy.db", Options{Registry: reg, Projectors: ps}); err == nil {
			t.Errorf("Open with projectors %v succeeded", ps)
		}
	}
}
