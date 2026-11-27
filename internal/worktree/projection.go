package worktree

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// Slot states.
const (
	StateFree   = "free"
	StateLeased = "leased"
	StateBroken = "broken"
)

// SlotInfo is a slot as the projection holds it.
type SlotInfo struct {
	Org, Repo string
	Slot      int
	Path      string
	State     string
	Run       string // when leased: the run holding it; when free: the last
	Branch    string
	Base      string
	BaseSHA   string
	Head      string // when free: the head it was released with
	Reason    string // when broken
	Version   int64  // the stream's version at this slot's last event
}

// Projector maintains the worktree_slots table from the pool streams. Register it with the store
// the Pool uses.
type Projector struct{}

// Name implements eventstore.Projector.
func (Projector) Name() string { return "worktree_slots" }

// Version implements eventstore.Projector.
func (Projector) Version() int { return 1 }

// Reset implements eventstore.Projector.
func (Projector) Reset(ctx context.Context, tx eventstore.Execer) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS worktree_slots`,
		`CREATE TABLE worktree_slots (
			org      TEXT NOT NULL,
			repo     TEXT NOT NULL,
			slot     INTEGER NOT NULL,
			path     TEXT NOT NULL,
			state    TEXT NOT NULL,
			run      TEXT NOT NULL DEFAULT '',
			branch   TEXT NOT NULL DEFAULT '',
			base     TEXT NOT NULL DEFAULT '',
			base_sha TEXT NOT NULL DEFAULT '',
			head     TEXT NOT NULL DEFAULT '',
			reason   TEXT NOT NULL DEFAULT '',
			version  INTEGER NOT NULL,
			PRIMARY KEY (org, repo, slot)
		)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// Apply implements eventstore.Projector.
func (Projector) Apply(ctx context.Context, tx eventstore.Execer, e eventstore.Event) error {
	if !strings.HasPrefix(e.Type, "worktree.") {
		return nil
	}
	rest, ok := strings.CutPrefix(e.Stream, StreamPrefix)
	org, repo, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 {
		return fmt.Errorf("worktree: %s event on stream %q", e.Type, e.Stream)
	}
	var err error
	switch e.Type {
	case Added{}.EventType():
		var p Added
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO worktree_slots (org, repo, slot, path, state, version) VALUES (?, ?, ?, ?, ?, ?)`,
			org, repo, p.Slot, p.Path, StateFree, e.StreamVersion)
	case Leased{}.EventType():
		var p Leased
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE worktree_slots SET state = ?, run = ?, branch = ?, base = ?, base_sha = ?, head = '', version = ?
			WHERE org = ? AND repo = ? AND slot = ?`, StateLeased, p.Run, p.Branch, p.Base, p.BaseSHA, e.StreamVersion, org, repo, p.Slot)
	case Released{}.EventType():
		var p Released
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE worktree_slots SET state = ?, head = ?, version = ? WHERE org = ? AND repo = ? AND slot = ?`,
			StateFree, p.Head, e.StreamVersion, org, repo, p.Slot)
	case Broken{}.EventType():
		var p Broken
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE worktree_slots SET state = ?, reason = ?, version = ? WHERE org = ? AND repo = ? AND slot = ?`,
			StateBroken, p.Reason, e.StreamVersion, org, repo, p.Slot)
	}
	return err
}

const slotColumns = `org, repo, slot, path, state, run, branch, base, base_sha, head, reason, version`

type rowScanner interface{ Scan(dest ...any) error }

func scanSlot(r rowScanner) (SlotInfo, error) {
	var s SlotInfo
	err := r.Scan(&s.Org, &s.Repo, &s.Slot, &s.Path, &s.State, &s.Run, &s.Branch, &s.Base, &s.BaseSHA, &s.Head, &s.Reason, &s.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return SlotInfo{}, ErrNotHeld
	}
	return s, err
}
