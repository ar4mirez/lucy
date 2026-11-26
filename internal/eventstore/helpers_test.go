package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Test payload types.

type taskCreated struct {
	Title string `json:"title"`
	Org   string `json:"org"`
}

func (taskCreated) EventType() string { return "task.created" }
func (taskCreated) EventVersion() int { return 1 }

type taskNoted struct {
	Note   string `json:"note"`
	Tokens int    `json:"token_count"`
}

func (taskNoted) EventType() string { return "task.noted" }
func (taskNoted) EventVersion() int { return 1 }

// taskNotedV2 is a later schema of task.noted, registered next to v1.
type taskNotedV2 struct {
	Note   string `json:"note"`
	Author string `json:"author"`
}

func (taskNotedV2) EventType() string { return "task.noted" }
func (taskNotedV2) EventVersion() int { return 2 }

type freeform struct {
	Fields map[string]string `json:"fields"`
}

func (freeform) EventType() string { return "test.freeform" }
func (freeform) EventVersion() int { return 1 }

type blob struct {
	Data string `json:"data"`
}

func (blob) EventType() string { return "test.blob" }
func (blob) EventVersion() int { return 1 }

func testRegistry(t testing.TB) *Registry {
	t.Helper()
	r, err := NewRegistry(taskCreated{}, taskNoted{}, taskNotedV2{}, freeform{}, blob{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// fakeClock advances one millisecond per reading, so every event has a distinct, known time.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Millisecond)
	return c.t
}

// tally is a test projection: events and the last type per stream.
type tally struct {
	version int
	failOn  string // Apply fails for events of this type
}

func (p *tally) Name() string { return "tally" }
func (p *tally) Version() int {
	if p.version == 0 {
		return 1
	}
	return p.version
}

func (p *tally) Reset(ctx context.Context, tx Execer) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS tally_streams`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE TABLE tally_streams (stream TEXT PRIMARY KEY, events INTEGER NOT NULL, last_type TEXT NOT NULL) STRICT`)
	return err
}

func (p *tally) Apply(ctx context.Context, tx Execer, e Event) error {
	if p.failOn != "" && e.Type == p.failOn {
		return errors.New("tally: refusing " + e.Type)
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO tally_streams (stream, events, last_type) VALUES (?, 1, ?)
		 ON CONFLICT (stream) DO UPDATE SET events = events + 1, last_type = excluded.last_type`,
		e.Stream, e.Type)
	return err
}

// tallyRows reads the tally projection as stream -> events.
func tallyRows(t testing.TB, s *Store) map[string]int64 {
	t.Helper()
	rows, err := s.QueryContext(context.Background(), `SELECT stream, events FROM tally_streams ORDER BY stream`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			t.Fatal(err)
		}
		out[k] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func openTest(t testing.TB, path string, ps ...Projector) *Store {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "lucy", DefaultFile)
	}
	s, err := Open(context.Background(), path, Options{Registry: testRegistry(t), Projectors: ps, Now: newFakeClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// rawDB opens a database file directly, bypassing the store, for tests that inspect or forge it.
func rawDB(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func mustAppend(t testing.TB, s *Store, stream string, expected int64, ps ...Payload) []Event {
	t.Helper()
	evs, err := s.Append(context.Background(), stream, expected, "test", ps...)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}
