package sim

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// A Pipeline whose merge stage requires approval holds a merge the green posture would clear, and
// the owner's approval then clears it (LUCY-SPEC-014): a Pipeline adds an approval, never waives one.
func TestPipelineApprovalRequiredHoldsGreen(t *testing.T) {
	w := newWorld(t, 41)
	s := spec("f1", policy.PostureGreen)
	s.Pipeline = &tower.Pipeline{Name: "acme/ship", MaxRestarts: 1, ApprovalRequired: true}
	dispatch(t, w, s)
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateHolding, "")
	if err := w.Tower.Approve(context.Background(), "f1", policy.ByOwner); err != nil {
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	wantOutcomes(t, w, "holding:merge.posture", "cleared:merge.posture")
	checkInvariants(t, w)
}

// stepAfter advances the clock by d and steps the Tower once, as the daemon's driver does while a
// flight holds.
func stepAfter(t *testing.T, w *World, d time.Duration) {
	t.Helper()
	w.Clock.Advance(d)
	if err := w.Tower.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A timeout never approves a merge (LUCY-SPEC-014). Under onTimeout abort, a merge held past its
// approval timeout grounds the flight; before the timeout it keeps holding. Nothing merges and no
// approval is recorded.
func TestApprovalTimeoutAbortGrounds(t *testing.T) {
	w := newWorld(t, 42)
	s := spec("f1", policy.PostureOwner)
	s.Pipeline = &tower.Pipeline{Name: "acme/ship", MaxRestarts: 1, ApprovalTimeout: 2 * time.Hour, OnTimeout: tower.OnTimeoutAbort}
	dispatch(t, w, s)
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateHolding, "")
	stepAfter(t, w, time.Hour)
	wantState(t, w, "f1", tower.StateHolding, "")
	stepAfter(t, w, time.Hour)
	wantState(t, w, "f1", tower.StateGrounded, tower.GroundApprovalTimeout)
	if len(w.Forge.Merges()) != 0 || slices.Contains(types(t, w, "f1"), "approved") {
		t.Fatalf("a timed-out merge was approved or merged: %v", types(t, w, "f1"))
	}
	wantOutcomes(t, w, "holding:merge.posture")
	checkInvariants(t, w)
}

// Under onTimeout escalate a merge keeps waiting on the owner past its timeout, however long, and
// merges only on the owner's approval.
func TestApprovalTimeoutEscalateKeepsHolding(t *testing.T) {
	w := newWorld(t, 43)
	s := spec("f1", policy.PostureOwner)
	s.Pipeline = &tower.Pipeline{Name: "acme/ship", MaxRestarts: 1, ApprovalTimeout: time.Hour, OnTimeout: tower.OnTimeoutEscalate}
	dispatch(t, w, s)
	run(t, w, RunOptions{})
	for range 3 {
		stepAfter(t, w, 10*24*time.Hour)
		wantState(t, w, "f1", tower.StateHolding, "")
	}
	if len(w.Forge.Merges()) != 0 {
		t.Fatal("merged on a timeout")
	}
	if err := w.Tower.Approve(context.Background(), "f1", policy.ByOwner); err != nil {
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	checkInvariants(t, w)
}

// The Tower refuses a Pipeline whose timeout would do anything but abort or escalate.
func TestDispatchRefusesAnAutoApprovingPipeline(t *testing.T) {
	w := newWorld(t, 44)
	for _, p := range []tower.Pipeline{
		{Name: "acme/ship", ApprovalTimeout: time.Hour, OnTimeout: "auto-approve"},
		{Name: "acme/ship", ApprovalTimeout: time.Hour},
		{Name: "acme/ship", OnTimeout: tower.OnTimeoutAbort},
		{Name: "acme/ship", MaxRestarts: -1},
	} {
		s := spec("f1", policy.PostureOwner)
		s.Pipeline = &p
		if err := w.Tower.Dispatch(context.Background(), s); err == nil {
			t.Errorf("dispatched with %+v", p)
		}
	}
	if _, ok := w.Tower.Flight("f1"); ok {
		t.Fatal("a refused flight was recorded")
	}
}

// The pilot stage's FailurePolicy decides how many lost Pilots are replaced: abort (none) grounds
// the flight on the first loss, which the Tower's default would replace.
func TestPipelineAbortDoesNotReplaceALostPilot(t *testing.T) {
	w := newWorld(t, 45)
	w.Runtime.Script("f1", Work(10*time.Minute), Crash())
	w.Runtime.Script("f1", DefaultScript()...)
	s := spec("f1", policy.PostureGreen)
	s.Pipeline = &tower.Pipeline{Name: "acme/ship", MaxRestarts: 0}
	dispatch(t, w, s)
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateGrounded, tower.GroundPilotLost)
	if len(w.Runtime.Started) != 1 {
		t.Fatalf("starts %v", w.Runtime.Started)
	}
	checkInvariants(t, w)
}

// A flight keeps its Pipeline across a daemon restart: it is recorded with the dispatch.
func TestPipelineSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 46)
	s := spec("f1", policy.PostureGreen)
	s.Pipeline = &tower.Pipeline{Name: "acme/ship", MaxRestarts: 1, ApprovalRequired: true}
	dispatch(t, w, s)
	if err := w.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateHolding, "")
}
