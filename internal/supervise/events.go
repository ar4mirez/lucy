package supervise

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// StreamPrefix starts each org's supervisor stream.
const StreamPrefix = "supervisor/"

// Stream is org's supervisor stream.
func Stream(org string) string { return StreamPrefix + org }

// Called records one model call the supervisor made for an org, with the tokens it spent.
type Called struct {
	Flight       string `json:"flight"`
	Tier         string `json:"tier"` // small or large
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Day          string `json:"day"` // the UTC day it counts against
}

// Declined records that the supervisor left a question to the owner, and why: the budget is spent,
// a call was refused or failed, or neither model was confident.
type Declined struct {
	Flight string `json:"flight"`
	Reason string `json:"reason"`
	Day    string `json:"day"`
}

// EventType and EventVersion implement eventstore.Payload.
func (Called) EventType() string   { return "supervisor.called" }
func (Called) EventVersion() int   { return 1 }
func (Declined) EventType() string { return "supervisor.declined" }
func (Declined) EventVersion() int { return 1 }

// Payloads are the supervisor's events, for an eventstore.Registry.
func Payloads() []eventstore.Payload { return []eventstore.Payload{Called{}, Declined{}} }

// Projector keeps the tokens each org's supervisor spent, by UTC day.
type Projector struct{}

// Name implements eventstore.Projector.
func (Projector) Name() string { return "supervisor_usage" }

// Version implements eventstore.Projector.
func (Projector) Version() int { return 1 }

// Reset implements eventstore.Projector.
func (Projector) Reset(ctx context.Context, tx eventstore.Execer) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS supervisor_usage`,
		`CREATE TABLE supervisor_usage (org TEXT NOT NULL, day TEXT NOT NULL, tokens INTEGER NOT NULL, PRIMARY KEY (org, day))`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// Apply implements eventstore.Projector.
func (Projector) Apply(ctx context.Context, tx eventstore.Execer, e eventstore.Event) error {
	if e.Type != (Called{}).EventType() || !strings.HasPrefix(e.Stream, StreamPrefix) {
		return nil
	}
	var p Called
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO supervisor_usage (org, day, tokens) VALUES (?, ?, ?)
		ON CONFLICT (org, day) DO UPDATE SET tokens = tokens + excluded.tokens`,
		strings.TrimPrefix(e.Stream, StreamPrefix), p.Day, p.InputTokens+p.OutputTokens)
	return err
}

// Querier reads the store.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Used is the tokens org's supervisor spent on day.
func Used(ctx context.Context, q Querier, org, day string) (int64, error) {
	var n int64
	err := q.QueryRowContext(ctx, `SELECT tokens FROM supervisor_usage WHERE org = ? AND day = ?`, org, day).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
