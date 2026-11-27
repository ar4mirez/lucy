package decision

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/sandbox"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Projector maintains the decision_catalog table from the Tower's flight streams (task/<org>/<id>)
// and the org container streams (sandbox/<org>). Register it with the store the daemon uses.
type Projector struct{}

// Name implements eventstore.Projector.
func (Projector) Name() string { return "decision_catalog" }

// Version implements eventstore.Projector.
func (Projector) Version() int { return 1 }

// Reset implements eventstore.Projector.
func (Projector) Reset(ctx context.Context, tx eventstore.Execer) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS decision_catalog`,
		`CREATE TABLE decision_catalog (
			seq           INTEGER PRIMARY KEY,
			kind          TEXT NOT NULL,
			org           TEXT NOT NULL,
			subject       TEXT NOT NULL,
			ref           INTEGER NOT NULL DEFAULT 0,
			summary       TEXT NOT NULL,
			body          TEXT NOT NULL DEFAULT '',
			lifecycle     TEXT NOT NULL,
			answered_by   TEXT NOT NULL DEFAULT '',
			answered_ns   INTEGER NOT NULL DEFAULT 0,
			superseded_by INTEGER NOT NULL DEFAULT 0,
			opened_ns     INTEGER NOT NULL,
			updated_ns    INTEGER NOT NULL
		)`,
		`CREATE INDEX decision_catalog_open ON decision_catalog (lifecycle, kind, org, subject)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// Apply implements eventstore.Projector.
func (Projector) Apply(ctx context.Context, tx eventstore.Execer, e eventstore.Event) error {
	at := e.Time.UnixNano()
	decode := func(v any) error { return json.Unmarshal(e.Payload, v) }
	open := func(kind, org, subject string, ref int64, summary, body string) error {
		// A newer decision of the same kind on the same subject replaces any still open.
		if _, err := tx.ExecContext(ctx, `UPDATE decision_catalog SET lifecycle = ?, superseded_by = ?, updated_ns = ?
			WHERE lifecycle = ? AND kind = ? AND org = ? AND subject = ?`, Superseded, e.Seq, at, Open, kind, org, subject); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO decision_catalog (seq, kind, org, subject, ref, summary, body, lifecycle, opened_ns, updated_ns)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, e.Seq, kind, org, subject, ref, summary, body, Open, at, at)
		return err
	}
	answer := func(kind, org, subject string) error {
		_, err := tx.ExecContext(ctx, `UPDATE decision_catalog SET lifecycle = ?, answered_by = ?, answered_ns = ?, updated_ns = ?
			WHERE lifecycle = ? AND kind = ? AND org = ? AND subject = ?`, Answered, e.Actor, at, at, Open, kind, org, subject)
		return err
	}
	archive := func(org, subject, kinds string) error {
		_, err := tx.ExecContext(ctx, `UPDATE decision_catalog SET lifecycle = ?, updated_ns = ?
			WHERE lifecycle = ? AND org = ? AND subject = ? AND instr(?, ','||kind||',') > 0`, Archived, at, Open, org, subject, ","+kinds+",")
		return err
	}

	if org, ok := strings.CutPrefix(e.Stream, sandbox.StreamPrefix); ok {
		switch e.Type {
		case sandbox.Requested{}.EventType():
			var p sandbox.Requested
			if err := decode(&p); err != nil {
				return err
			}
			if p.Outcome != policy.Holding {
				return nil
			}
			sessions := "no live session"
			if len(p.LiveSessions) > 0 {
				sessions = "sessions " + strings.Join(p.LiveSessions, ", ")
			}
			return open(KindContainer, org, string(p.Op), p.DecisionSeq,
				fmt.Sprintf("%s the container of org %s, stopping %s", p.Op, org, sessions), "")
		case sandbox.Confirmed{}.EventType():
			var p sandbox.Confirmed
			if err := decode(&p); err != nil {
				return err
			}
			return answer(KindContainer, org, string(p.Op))
		}
		return nil
	}
	rest, ok := strings.CutPrefix(e.Stream, "task/")
	if !ok {
		return nil
	}
	org, flight, ok := strings.Cut(rest, "/")
	if !ok {
		return nil
	}
	switch e.Type {
	case tower.PilotAsked{}.EventType():
		var p tower.PilotAsked
		if err := decode(&p); err != nil {
			return err
		}
		return open(KindQuestion, org, flight, 0, "The Pilot of flight "+flight+" asks the owner a question", p.Question)
	case tower.PilotAnswered{}.EventType():
		return answer(KindQuestion, org, flight)
	case tower.PilotExited{}.EventType():
		return archive(org, flight, KindQuestion)
	case tower.Cleared{}.EventType():
		var p tower.Cleared
		if err := decode(&p); err != nil {
			return err
		}
		if p.Outcome != policy.Holding {
			return nil
		}
		head := p.HeadSHA
		if len(head) > 12 {
			head = head[:12]
		}
		return open(KindMerge, org, flight, p.DecisionSeq,
			fmt.Sprintf("The merge of flight %s at head %s waits for the owner's approval (rule %s)", flight, head, p.Rule), "")
	case tower.Approved{}.EventType():
		return answer(KindMerge, org, flight)
	case tower.Takeoff{}.EventType(), tower.Grounded{}.EventType(), tower.MergedOutside{}.EventType():
		return archive(org, flight, KindQuestion+","+KindMerge)
	}
	return nil
}

const rowColumns = `seq, kind, org, subject, ref, summary, body, lifecycle, answered_by, answered_ns, superseded_by, opened_ns, updated_ns`

type rowScanner interface{ Scan(dest ...any) error }

func scan(r rowScanner) (Row, error) {
	var d Row
	var answered, opened, updated int64
	err := r.Scan(&d.Seq, &d.Kind, &d.Org, &d.Subject, &d.Ref, &d.Summary, &d.Body, &d.Lifecycle, &d.AnsweredBy, &answered,
		&d.SupersededBy, &opened, &updated)
	if err != nil {
		return Row{}, err
	}
	if answered != 0 {
		d.AnsweredAt = time.Unix(0, answered).UTC()
	}
	d.Opened, d.Updated = time.Unix(0, opened).UTC(), time.Unix(0, updated).UTC()
	return d, nil
}

// Querier reads the store.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// List returns the catalog's decisions, oldest first: only the open ones unless all is set.
func List(ctx context.Context, q Querier, all bool) ([]Row, error) {
	query := `SELECT ` + rowColumns + ` FROM decision_catalog`
	var args []any
	if !all {
		query += ` WHERE lifecycle = ?`
		args = append(args, Open)
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []Row
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return out, nil
}
