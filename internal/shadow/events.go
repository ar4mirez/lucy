package shadow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// StreamPrefix starts each org's shadow stream.
const StreamPrefix = "shadow/"

// Stream is org's shadow stream.
func Stream(org string) string { return StreamPrefix + org }

// Imported records one task of the existing supervisor, as an import read it.
type Imported struct {
	Task   Task   `json:"task"`
	Digest string `json:"digest"` // hex SHA-256 of the task as recorded, to tell a re-import apart
}

// EventType and EventVersion implement eventstore.Payload.
func (Imported) EventType() string { return "shadow.task_imported" }
func (Imported) EventVersion() int { return 1 }

// Payloads are shadow mode's events, for an eventstore.Registry.
func Payloads() []eventstore.Payload { return []eventstore.Payload{Imported{}} }

// Digest is a task's digest, over its canonical JSON.
func Digest(t Task) string {
	b, _ := json.Marshal(t) // a Task always encodes
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}

// Projector keeps the imported tasks.
type Projector struct{}

// Name implements eventstore.Projector.
func (Projector) Name() string { return "shadow_tasks" }

// Version implements eventstore.Projector.
func (Projector) Version() int { return 1 }

// Reset implements eventstore.Projector.
func (Projector) Reset(ctx context.Context, tx eventstore.Execer) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS shadow_tasks`,
		`CREATE TABLE shadow_tasks (org TEXT NOT NULL, task TEXT NOT NULL, class TEXT NOT NULL, posture TEXT NOT NULL,
			actions INTEGER NOT NULL, digest TEXT NOT NULL, seq INTEGER NOT NULL, PRIMARY KEY (org, task))`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// Apply implements eventstore.Projector.
func (Projector) Apply(ctx context.Context, tx eventstore.Execer, e eventstore.Event) error {
	if e.Type != (Imported{}).EventType() {
		return nil
	}
	org, ok := strings.CutPrefix(e.Stream, StreamPrefix)
	if !ok || org == "" {
		return fmt.Errorf("shadow: %s on stream %q", e.Type, e.Stream)
	}
	var p Imported
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO shadow_tasks (org, task, class, posture, actions, digest, seq) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		org, p.Task.ID, p.Task.Class, p.Task.Posture, len(p.Task.Actions), p.Digest, e.Seq)
	return err
}

// Row is an imported task, as listed.
type Row struct {
	Org, Task, Class, Posture string
	Actions                   int
	Seq                       int64
}

// Querier reads the store.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// List lists the imported tasks, of org when it is not empty, by org, class and task.
func List(ctx context.Context, q Querier, org string) ([]Row, error) {
	rows, err := q.QueryContext(ctx, `SELECT org, task, class, posture, actions, seq FROM shadow_tasks
		WHERE ? = '' OR org = ? ORDER BY org, class, task`, org, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Org, &r.Task, &r.Class, &r.Posture, &r.Actions, &r.Seq); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Outcome is what an import did with one task.
type Outcome string

// Import outcomes.
const (
	Recorded  Outcome = "recorded"  // a new task, recorded
	Unchanged Outcome = "unchanged" // the same task was imported before: nothing recorded
)

// ErrConflict is an import of a task id already imported with other contents.
var ErrConflict = errors.New("shadow: the task was imported before with other contents")

// ErrUnknownOrg is an import for an org the daemon's config does not admit.
var ErrUnknownOrg = errors.New("shadow: no admitted Airspace for the org")

// Recorder records imports in the daemon's log.
type Recorder struct {
	Store *eventstore.Store
	// Known reports whether org has an admitted Airspace: shadow mode never records another org's
	// tasks.
	Known func(org string) bool
}

// Import validates t again and records it, once: the same task again is Unchanged, and a task id
// imported before with other contents is ErrConflict.
func (r *Recorder) Import(ctx context.Context, actor string, t Task) (Outcome, error) {
	if err := Validate(t); err != nil {
		return "", err
	}
	if r.Known == nil || !r.Known(t.Org) {
		return "", fmt.Errorf("%w %q", ErrUnknownOrg, t.Org)
	}
	digest := Digest(t)
	var out Outcome
	err := r.Store.Update(ctx, func(tx *eventstore.Tx) error {
		var prev string
		err := tx.QueryRowContext(ctx, `SELECT digest FROM shadow_tasks WHERE org = ? AND task = ?`, t.Org, t.ID).Scan(&prev)
		switch {
		case err == nil && prev == digest:
			out = Unchanged
			return nil
		case err == nil:
			return ErrConflict
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.Append(ctx, Stream(t.Org), eventstore.AnyVersion, actor, Imported{Task: t, Digest: digest}); err != nil {
			return err
		}
		out = Recorded
		return nil
	})
	return out, err
}
