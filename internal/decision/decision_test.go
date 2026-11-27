package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/sandbox"
	"github.com/ar4mirez/lucy/internal/spec"
	"github.com/ar4mirez/lucy/internal/tower"
)

// The oracle is LUCY-SPEC-013 and the spec's Decision schema: every decision waiting on the
// owner is projected from the log, closes only on the owner's recorded word, and reads as a valid
// Decision resource.

var ctx = context.Background()

func open(t *testing.T) *eventstore.Store {
	t.Helper()
	dir := t.TempDir()
	reg, err := eventstore.NewRegistry(append(append(tower.Payloads(), sandbox.Payloads()...), policy.DecisionRecord{})...)
	if err != nil {
		t.Fatal(err)
	}
	st, err := eventstore.Open(ctx, filepath.Join(dir, "lucy", "lucy.db"), eventstore.Options{Registry: reg,
		Projectors: []eventstore.Projector{Projector{}}, SnapshotDir: filepath.Join(dir, "lucy", "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func appendAs(t *testing.T, st *eventstore.Store, stream, actor string, p eventstore.Payload) int64 {
	t.Helper()
	evs, err := st.Append(ctx, stream, eventstore.AnyVersion, actor, p)
	if err != nil {
		t.Fatal(err)
	}
	return evs[0].Seq
}

func list(t *testing.T, st *eventstore.Store, all bool) map[string]Row {
	t.Helper()
	rows, err := List(ctx, st, all)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Row{}
	for _, r := range rows {
		out[r.ID()] = r
	}
	return out
}

// validResource checks that r reads as the spec's Decision resource.
func validResource(t *testing.T, r Row) {
	t.Helper()
	b, err := json.Marshal(r.Resource())
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var doc any
	if err := d.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	errs, err := spec.Validate(spec.DecisionSchema, doc)
	if err != nil || len(errs) > 0 {
		t.Fatalf("%s is not a valid Decision: %v %v\n%s", r.ID(), errs, err, b)
	}
}

func TestQuestionsAndMerges(t *testing.T) {
	st := open(t)
	f1 := "task/acme/f1"
	q1 := appendAs(t, st, f1, "daemon", tower.PilotAsked{Session: "f1#1", Question: "which account?"})
	q2 := appendAs(t, st, f1, "daemon", tower.PilotAsked{Session: "f1#1", Question: "and the region?"})
	open1 := list(t, st, false)
	if len(open1) != 1 {
		t.Fatalf("open decisions %+v; a newer question replaces the older", open1)
	}
	r := open1[Row{Seq: q2}.ID()]
	if r.Kind != KindQuestion || r.Body != "and the region?" || r.Scope() != "flight:acme/f1" {
		t.Fatalf("question %+v", r)
	}
	all := list(t, st, true)
	if old := all[Row{Seq: q1}.ID()]; old.Lifecycle != Superseded || old.SupersededBy != q2 {
		t.Fatalf("the replaced question %+v", old)
	}
	appendAs(t, st, f1, "owner", tower.PilotAnswered{Session: "f1#1", AnswerSHA256: "00"})
	if open := list(t, st, false); len(open) != 0 {
		t.Fatalf("open after the owner's answer: %+v", open)
	}
	answered := list(t, st, true)[Row{Seq: q2}.ID()]
	if answered.Lifecycle != Answered || answered.AnsweredBy != "owner" {
		t.Fatalf("answered question %+v", answered)
	}

	// A merge holding for the owner opens a decision; the owner's approval answers it.
	m := appendAs(t, st, f1, "daemon", tower.Cleared{DecisionSeq: 40, Outcome: policy.Holding, Rule: "merge.posture", HeadSHA: "0123456789abcdef"})
	appendAs(t, st, f1, "daemon", tower.Cleared{DecisionSeq: 41, Outcome: policy.Cleared, Rule: "merge.posture", HeadSHA: "0123456789abcdef"})
	if open := list(t, st, false); len(open) != 1 || open[Row{Seq: m}.ID()].Ref != 40 {
		t.Fatalf("a cleared decision closed the owner's: %+v", open)
	}
	appendAs(t, st, f1, "owner", tower.Approved{Approval: policy.Approval{Decision: "40", By: policy.ByOwner}})
	merged := list(t, st, true)[Row{Seq: m}.ID()]
	if merged.Lifecycle != Answered || merged.AnsweredBy != "owner" {
		t.Fatalf("approved merge %+v", merged)
	}

	// A flight that ends archives its open decisions; nothing else closes one.
	f2 := "task/acme/f2"
	q3 := appendAs(t, st, f2, "daemon", tower.PilotAsked{Session: "f2#1", Question: "proceed?"})
	appendAs(t, st, f2, "daemon", tower.Grounded{Reason: tower.GroundPilotFailed})
	if ended := list(t, st, true)[Row{Seq: q3}.ID()]; ended.Lifecycle != Archived {
		t.Fatalf("a grounded flight's question %+v", ended)
	}
	for _, r := range list(t, st, true) {
		validResource(t, r)
	}
}

func TestContainerOperations(t *testing.T) {
	st := open(t)
	s := sandbox.Stream("acme")
	appendAs(t, st, s, "daemon", sandbox.Requested{Op: sandbox.OpRestart, Outcome: policy.Cleared, DecisionSeq: 5})
	if open := list(t, st, false); len(open) != 0 {
		t.Fatalf("a cleared operation opened a decision: %+v", open)
	}
	c := appendAs(t, st, s, "daemon", sandbox.Requested{Op: sandbox.OpRestart, Outcome: policy.Holding, DecisionSeq: 6,
		LiveSessions: []string{"f1#1"}, NeedsOwner: true})
	r := list(t, st, false)[Row{Seq: c}.ID()]
	if r.Kind != KindContainer || r.Subject != "restart" || r.Scope() != "container:acme" || r.Ref != 6 {
		t.Fatalf("container decision %+v", r)
	}
	validResource(t, r)
	appendAs(t, st, s, "owner", sandbox.Confirmed{Op: sandbox.OpRestart, DecisionSeq: 6, Approval: policy.Approval{Decision: "6", By: policy.ByOwner}})
	if got := list(t, st, true)[Row{Seq: c}.ID()]; got.Lifecycle != Answered || got.AnsweredBy != "owner" {
		t.Fatalf("confirmed operation %+v", got)
	}
}

// The catalog is a projection: a rebuild from the log gives the same decisions.
func TestCatalogRebuilds(t *testing.T) {
	st := open(t)
	appendAs(t, st, "task/acme/f1", "daemon", tower.PilotAsked{Session: "f1#1", Question: "q"})
	before := list(t, st, true)
	if err := st.Rebuild(ctx, Projector{}.Name()); err != nil {
		t.Fatal(err)
	}
	after := list(t, st, true)
	if len(after) != len(before) {
		t.Fatalf("rebuilt %+v, before %+v", after, before)
	}
	for id, r := range before {
		if after[id] != r {
			t.Fatalf("rebuilt %+v, before %+v", after[id], r)
		}
	}
}
