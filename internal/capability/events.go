package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// Issued records a token's issue: its SHA-256, never the token.
type Issued struct {
	Digest    string    `json:"digest"` // hex SHA-256 of the token
	Run       string    `json:"run"`
	Fence     int64     `json:"fence"`
	Routes    []string  `json:"routes"`
	StartedAt time.Time `json:"started_at,omitzero"` // when the lease's first token was issued
	ExpiresAt time.Time `json:"expires_at"`
}

// Revoked records a token's revocation, by its id.
type Revoked struct {
	ID     string `json:"id"`
	Reason string `json:"reason,omitempty"`
}

// EventType and EventVersion implement eventstore.Payload.
func (Issued) EventType() string  { return "capability.issued" }
func (Revoked) EventType() string { return "capability.revoked" }
func (Issued) EventVersion() int  { return 1 }
func (Revoked) EventVersion() int { return 1 }

// Payloads returns a sample of every event the Issuer appends, for an eventstore.Registry.
func Payloads() []eventstore.Payload { return []eventstore.Payload{Issued{}, Revoked{}} }

// Projector maintains the capabilities table from the "capability/<org>" streams.
type Projector struct{}

// Name implements eventstore.Projector.
func (Projector) Name() string { return "capabilities" }

// Version implements eventstore.Projector.
func (Projector) Version() int { return 2 }

// Reset implements eventstore.Projector.
func (Projector) Reset(ctx context.Context, tx eventstore.Execer) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS capabilities`,
		`CREATE TABLE capabilities (
			id             TEXT PRIMARY KEY,
			digest         TEXT NOT NULL UNIQUE,
			org            TEXT NOT NULL,
			run            TEXT NOT NULL,
			fence          INTEGER NOT NULL,
			routes         TEXT NOT NULL,
			issued_ns      INTEGER NOT NULL,
			start_ns       INTEGER NOT NULL,
			expires_ns     INTEGER NOT NULL,
			revoked        INTEGER NOT NULL DEFAULT 0,
			revoked_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX capabilities_issued ON capabilities (issued_ns)`,
		`CREATE INDEX capabilities_run ON capabilities (run, fence)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// Apply implements eventstore.Projector.
func (Projector) Apply(ctx context.Context, tx eventstore.Execer, e eventstore.Event) error {
	if !strings.HasPrefix(e.Type, "capability.") {
		return nil
	}
	org, ok := strings.CutPrefix(e.Stream, StreamPrefix)
	if !ok || org == "" {
		return fmt.Errorf("capability: %s event on stream %q", e.Type, e.Stream)
	}
	switch e.Type {
	case Issued{}.EventType():
		var p Issued
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if len(p.Digest) < idHexLen {
			return fmt.Errorf("capability: issued event %d has no digest", e.Seq)
		}
		routes, err := json.Marshal(p.Routes)
		if err != nil {
			return err
		}
		start := p.StartedAt
		if start.IsZero() {
			start = e.Time
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO capabilities (id, digest, org, run, fence, routes, issued_ns, start_ns, expires_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.Digest[:idHexLen], p.Digest, org, p.Run, p.Fence, string(routes),
			e.Time.UnixNano(), start.UnixNano(), p.ExpiresAt.UnixNano())
		return err
	case Revoked{}.EventType():
		var p Revoked
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE capabilities SET revoked = 1, revoked_reason = ? WHERE id = ? AND org = ?`, p.Reason, p.ID, org)
		return err
	}
	return nil
}
