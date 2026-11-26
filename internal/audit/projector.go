package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// Projector writes one audit entry per event into the audit_entries table, inside the append
// transaction, so an event and its entry commit together. The table refuses UPDATE and DELETE.
//
// The mapping from events to entries is versioned with the projector: a changed mapping is a new
// Version, and the store rebuilds the chain from the log, which gives the same entries for the
// same events every time.
type Projector struct{}

// Name implements eventstore.Projector.
func (Projector) Name() string { return "audit" }

// Version implements eventstore.Projector.
func (Projector) Version() int { return 1 }

// Reset implements eventstore.Projector.
func (Projector) Reset(ctx context.Context, tx eventstore.Execer) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS audit_entries`,
		`CREATE TABLE audit_entries (
			seq   INTEGER PRIMARY KEY,
			id    TEXT NOT NULL UNIQUE,
			hash  TEXT NOT NULL,
			entry TEXT NOT NULL
		) STRICT`,
		`CREATE TRIGGER audit_entries_no_update BEFORE UPDATE ON audit_entries
			BEGIN SELECT RAISE(ABORT, 'audit entries are append-only'); END`,
		`CREATE TRIGGER audit_entries_no_delete BEFORE DELETE ON audit_entries
			BEGIN SELECT RAISE(ABORT, 'audit entries are append-only'); END`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// Apply implements eventstore.Projector.
func (Projector) Apply(ctx context.Context, tx eventstore.Execer, e eventstore.Event) error {
	var prev string
	err := tx.QueryRowContext(ctx, `SELECT hash FROM audit_entries ORDER BY seq DESC LIMIT 1`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	entry, line, err := sealEvent(e, prev)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_entries (seq, id, hash, entry) VALUES (?, ?, ?, ?)`,
		e.Seq, entry.ID, entry.Hash, string(line))
	return err
}

func sealEvent(e eventstore.Event, prev string) (Entry, []byte, error) {
	entry, err := FromEvent(e)
	if err != nil {
		return Entry{}, nil, err
	}
	sealed, err := Seal(entry, prev)
	if err != nil {
		return Entry{}, nil, err
	}
	line, err := sealed.Line()
	if err != nil {
		return Entry{}, nil, err
	}
	return sealed, line, nil
}

// eventRef ties an entry to the event it was made from; payloadSha256 covers the payload as
// stored, so editing the event breaks the match even where the entry shows none of the payload.
type eventRef struct {
	Seq           int64  `json:"seq"`
	Stream        string `json:"stream"`
	StreamVersion int64  `json:"streamVersion"`
	Type          string `json:"type"`
	TypeVersion   int    `json:"typeVersion"`
	PayloadSHA256 string `json:"payloadSha256"`
}

type details struct {
	ActorType      string   `json:"actorType"`
	Event          eventRef `json:"event"`
	Outcome        string   `json:"outcome,omitempty"`
	RuleSetVersion int      `json:"rulesetVersion,omitempty"`
	RuleSetSHA256  string   `json:"rulesetSha256,omitempty"`
	Role           string   `json:"role,omitempty"`
	Tainted        bool     `json:"tainted,omitempty"`
	ApprovalsUsed  []string `json:"approvalsUsed,omitempty"`
}

// FromEvent maps one event to its unsealed audit entry. Every event is an action: the actor is the
// event's, the action its type and the resource its stream, and the decision is allowed, since an
// event is appended only once lucy has done what it records. A policy decision is the exception:
// its entry names the action decided on, the target, the rule and lucy's outcome (cleared is
// allowed; denied and holding are denied, as nothing happened yet).
func FromEvent(e eventstore.Event) (Entry, error) {
	sum := sha256.Sum256(e.Payload)
	d := details{
		ActorType: actorType(e.Actor),
		Event: eventRef{
			Seq: e.Seq, Stream: e.Stream, StreamVersion: e.StreamVersion, Type: e.Type,
			TypeVersion: e.TypeVersion, PayloadSHA256: hex.EncodeToString(sum[:]),
		},
	}
	entry := Entry{
		ID:        "lucy-event-" + strconv.FormatInt(e.Seq, 10),
		Timestamp: e.Time.UTC().Format(TimeFormat),
		Actor:     e.Actor,
		Action:    e.Type,
		Resource:  "stream/" + e.Stream,
		Decision:  DecisionAllowed,
	}
	if e.Type == (policy.DecisionRecord{}).EventType() {
		var r policy.DecisionRecord
		if err := json.Unmarshal(e.Payload, &r); err != nil {
			return Entry{}, fmt.Errorf("audit: policy decision at seq %d: %w", e.Seq, err)
		}
		entry.Action = string(r.Kind)
		entry.Resource = targetResource(r.Target)
		entry.Policy = r.Rule
		entry.Decision = DecisionDenied
		if r.Outcome == policy.Cleared {
			entry.Decision = DecisionAllowed
		}
		d.Outcome = string(r.Outcome)
		d.RuleSetVersion, d.RuleSetSHA256 = r.RuleSetVersion, r.RuleSetSHA256
		d.Role, d.Tainted, d.ApprovalsUsed = string(r.Actor.Role), r.Tainted, r.ApprovalsUsed
		d.ActorType = roleActorType(r.Actor.Role)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return Entry{}, err
	}
	entry.Details = raw
	return entry, nil
}

// targetResource names a policy target as kind/name pairs: org/acme/project/web/pr/12.
func targetResource(t policy.Target) string {
	s := "org/" + t.Org
	if t.Project != "" {
		s += "/project/" + t.Project
	}
	if t.Resource != "" {
		s += "/" + strings.TrimPrefix(t.Resource, "/")
	}
	return s
}

// actorType classifies an event actor as the spec's metrics section does: the daemon is a
// service account, the owner's surfaces (the local socket, paired devices) are human, and
// everything else is an agent.
func actorType(actor string) string {
	switch {
	case actor == "daemon":
		return "service-account"
	case strings.HasPrefix(actor, "local/"), strings.HasPrefix(actor, "https/"), actor == "owner":
		return "human"
	default:
		return "ai-agent"
	}
}

func roleActorType(r policy.Role) string {
	switch r {
	case policy.RoleOwner:
		return "human"
	case policy.RoleDaemon, policy.RoleGateway:
		return "service-account"
	default:
		return "ai-agent"
	}
}

// StoredEntry is one entry as the audit table holds it.
type StoredEntry struct {
	Seq   int64
	Entry json.RawMessage // canonical JSON, hash included
}

// entries pages the stored chain: entries for events after afterSeq up to throughSeq, at most
// limit of them.
func entries(ctx context.Context, store *eventstore.Store, afterSeq, throughSeq int64, limit int) ([]StoredEntry, error) {
	rows, err := store.QueryContext(ctx, `SELECT seq, entry FROM audit_entries WHERE seq > ? AND seq <= ? ORDER BY seq LIMIT ?`,
		afterSeq, throughSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("audit: read entries: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []StoredEntry
	for rows.Next() {
		var s StoredEntry
		var entry string
		if err := rows.Scan(&s.Seq, &entry); err != nil {
			return nil, err
		}
		s.Entry = json.RawMessage(entry)
		out = append(out, s)
	}
	return out, rows.Err()
}

const verifyBatch = 500

// VerifyStore verifies lucy's own chain: every entry against the spec's rules (Verifier), every
// event has exactly one entry, and every entry is exactly the one its event maps to, so a changed
// event, a changed entry, a missing one or an extra one is reported with where it happened.
//
// It verifies the log up to its head when called. An event and its entry commit together, so
// appends that land while it runs are left for the next run; an entry past that head is reported
// only if no event has its sequence number.
func VerifyStore(ctx context.Context, store *eventstore.Store) (Report, error) {
	head, err := store.Head(ctx)
	if err != nil {
		return Report{}, err
	}
	var v Verifier
	var after int64
	prev := ""
	for {
		evs, err := store.ReadAfter(ctx, after, verifyBatch)
		if err != nil {
			return Report{}, err
		}
		for len(evs) > 0 && evs[len(evs)-1].Seq > head {
			evs = evs[:len(evs)-1]
		}
		stored, err := entries(ctx, store, after, head, verifyBatch)
		if err != nil {
			return Report{}, err
		}
		if len(evs) == 0 && len(stored) == 0 {
			if err := orphanAfter(ctx, store, head, v.n+1); err != nil {
				return Report{}, err
			}
			return v.Report(), nil
		}
		for i := 0; i < len(evs) || i < len(stored); i++ {
			index := v.n + 1
			switch {
			case i >= len(stored):
				return Report{}, &VerifyError{Index: index, Seq: evs[i].Seq, Reason: "the event has no audit entry"}
			case i >= len(evs) || stored[i].Seq < evs[i].Seq:
				return Report{}, &VerifyError{Index: index, Seq: stored[i].Seq, Reason: "an audit entry for an event the log does not have"}
			case stored[i].Seq > evs[i].Seq:
				return Report{}, &VerifyError{Index: index, Seq: evs[i].Seq, Reason: "the event has no audit entry"}
			}
			e, err := v.Next(stored[i].Entry)
			if err != nil {
				var ve *VerifyError
				if errors.As(err, &ve) {
					ve.Seq = evs[i].Seq
				}
				return Report{}, err
			}
			_, want, err := sealEvent(evs[i], prev)
			if err != nil {
				return Report{}, &VerifyError{Index: index, Seq: evs[i].Seq, Reason: err.Error()}
			}
			if string(want) != string(stored[i].Entry) {
				return Report{}, &VerifyError{Index: index, Seq: evs[i].Seq, Reason: "the entry does not match its event: the event or the entry was changed"}
			}
			prev = e.Hash
			after = evs[i].Seq
		}
	}
}

// orphanAfter reports an entry past head that no event has, in one statement so that appends
// committing meanwhile are seen whole or not at all.
func orphanAfter(ctx context.Context, store *eventstore.Store, head int64, index int) error {
	var seq sql.NullInt64
	err := store.QueryRowContext(ctx, `SELECT MIN(a.seq) FROM audit_entries a
		WHERE a.seq > ? AND NOT EXISTS (SELECT 1 FROM events e WHERE e.seq = a.seq)`, head).Scan(&seq)
	if err != nil {
		return fmt.Errorf("audit: read entries: %w", err)
	}
	if seq.Valid {
		return &VerifyError{Index: index, Seq: seq.Int64, Reason: "an audit entry for an event the log does not have"}
	}
	return nil
}
