package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

type note struct {
	Text string `json:"text"`
}

func (note) EventType() string { return "test.note" }
func (note) EventVersion() int { return 1 }

func openStore(t *testing.T, path string) *eventstore.Store {
	t.Helper()
	reg, err := eventstore.NewRegistry(note{}, policy.DecisionRecord{})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 26, 9, 0, 0, 123456789, time.UTC)
	s, err := eventstore.Open(context.Background(), path, eventstore.Options{
		Registry:    reg,
		Projectors:  []eventstore.Projector{Projector{}},
		Now:         func() time.Time { clock = clock.Add(time.Second); return clock },
		SnapshotDir: filepath.Dir(path),
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func seed(t *testing.T, s *eventstore.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Append(ctx, "daemon", eventstore.AnyVersion, "daemon", note{"started"}, note{"loaded"}); err != nil {
		t.Fatal(err)
	}
	req := policy.Request{
		Actor:  policy.Actor{ID: "local/uid/1000", Role: policy.RoleOwner},
		Kind:   policy.KindMerge,
		Target: policy.Target{Org: "acme", Project: "web", Resource: "pr/12"},
		Inputs: []policy.Input{{Source: policy.SourceOwner, Ref: "api/local"}},
	}
	rec := policy.NewRecord(req, policy.Facts{}, policy.Evaluate(req, policy.Facts{}))
	if _, err := s.Append(ctx, policy.Stream, eventstore.AnyVersion, "local/uid/1000", rec); err != nil {
		t.Fatal(err)
	}
}

func TestProjectorChainsEveryEvent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state", "lucy.db")
	s := openStore(t, path)
	defer s.Close()
	seed(t, s)

	rep, err := VerifyStore(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Entries != 3 || rep.Head == "" {
		t.Fatalf("report %+v", rep)
	}
	stored, err := Entries(ctx, s, 0, 10)
	if err != nil || len(stored) != 3 {
		t.Fatalf("entries %v %v", stored, err)
	}
	var first, decision Entry
	if err := json.Unmarshal(stored[0].Entry, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stored[2].Entry, &decision); err != nil {
		t.Fatal(err)
	}
	if first.PreviousHash != "" || first.ID != "lucy-event-1" || first.Action != "test.note" || first.Resource != "stream/daemon" ||
		first.Decision != DecisionAllowed || !strings.HasSuffix(first.Timestamp, ".123Z") || first.Actor != "daemon" {
		t.Fatalf("first entry %s", stored[0].Entry)
	}
	if !strings.Contains(string(first.Details), `"actorType":"service-account"`) || !strings.Contains(string(first.Details), `"payloadSha256"`) {
		t.Fatalf("first entry details %s", first.Details)
	}
	// The owner's merge with no facts is held for the owner: denied for now, outcome in details.
	if decision.Action != "merge" || decision.Resource != "org/acme/project/web/pr/12" || decision.Policy == "" ||
		decision.Decision != DecisionDenied || !strings.Contains(string(decision.Details), `"actorType":"human"`) ||
		!strings.Contains(string(decision.Details), `"outcome":"`) {
		t.Fatalf("decision entry %s", stored[2].Entry)
	}

	// Entries can be exported and verified as JSON Lines, as any SDK's chain is.
	var jsonl strings.Builder
	for _, e := range stored {
		jsonl.Write(e.Entry)
		jsonl.WriteByte('\n')
	}
	if r, err := VerifyJSONL(strings.NewReader(jsonl.String())); err != nil || r != rep {
		t.Fatalf("exported chain: %+v %v, want %+v", r, err, rep)
	}

	// The table is append-only.
	if _, err := s.QueryContext(ctx, `DELETE FROM audit_entries`); err == nil {
		t.Fatal("delete through the read-only connection succeeded")
	}

	// Rebuilding the projection from the log gives the same chain.
	if err := s.Rebuild(ctx, Projector{}.Name()); err != nil {
		t.Fatal(err)
	}
	if again, err := VerifyStore(ctx, s); err != nil || again != rep {
		t.Fatalf("after rebuild %+v %v, want %+v", again, err, rep)
	}
}

// tamper runs SQL against the closed database with the append-only triggers lifted, as someone
// editing the file directly would.
func tamper(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var triggers []string
	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE type = 'trigger'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, q)
	}
	rows.Close()
	for _, q := range []string{
		`DROP TRIGGER events_append_only_update`, `DROP TRIGGER events_append_only_delete`,
		`DROP TRIGGER audit_entries_no_update`, `DROP TRIGGER audit_entries_no_delete`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, q := range triggers {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTamperedStoreFails edits the database file behind lucy's back, one way per case; each
// edit must fail verification at the entry it touched.
func TestTamperedStoreFails(t *testing.T) {
	cases := map[string]struct {
		stmts []string
		seq   int64
	}{
		"event payload edited": {[]string{`UPDATE events SET payload = '{"text":"stopped"}' WHERE seq = 2`}, 2},
		"event actor edited":   {[]string{`UPDATE events SET actor = 'someone' WHERE seq = 1`}, 1},
		"entry edited": {[]string{
			`UPDATE audit_entries SET entry = replace(entry, '"decision":"denied"', '"decision":"allowed"') WHERE seq = 3`,
		}, 3},
		"entry actor edited": {[]string{
			`UPDATE audit_entries SET entry = replace(entry, '"actor":"daemon"', '"actor":"owner"') WHERE seq = 1`,
		}, 1},
		"entry removed": {[]string{`DELETE FROM audit_entries WHERE seq = 2`}, 2},
		"event removed": {[]string{`DELETE FROM events WHERE seq = 2`}, 2},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state", "lucy.db")
			s := openStore(t, path)
			seed(t, s)
			if _, err := VerifyStore(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			tamper(t, path, c.stmts...)
			s = openStore(t, path)
			defer s.Close()
			_, err := VerifyStore(context.Background(), s)
			var ve *VerifyError
			if !errors.As(err, &ve) || !errors.Is(err, ErrBroken) {
				t.Fatalf("VerifyStore = %v, want a broken chain", err)
			}
			if ve.Seq != c.seq {
				t.Fatalf("broken at event %d (%s), want %d", ve.Seq, ve.Reason, c.seq)
			}
		})
	}
}
