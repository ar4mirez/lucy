package sim

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// A flight demoted mid-flight is flown under the stricter posture from then on: a green flight
// whose pull request is in review when it is tightened to owner holds for the owner instead of
// merging (LUCY-POL-005), and the tightening is recorded.
func TestADemotionMidFlightBlocksAGreenMerge(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 61)
	dispatch(t, w, spec("f1", policy.PostureGreen), Work(10*time.Minute), OpenPR(Passing("ci", time.Hour)))
	tightened := false
	run(t, w, RunOptions{BeforeStep: func(ctx context.Context, w *World, _ int) error {
		if st, _ := w.Tower.Flight("f1"); !tightened && st.State == tower.StateReview {
			done, err := w.Tower.Tighten(ctx, "f1", policy.PostureOwner, "its Runway was demoted")
			tightened = done
			return err
		}
		return nil
	}})
	if !tightened {
		t.Fatal("the flight never reached review to be tightened")
	}
	wantState(t, w, "f1", tower.StateHolding, "")
	if len(w.Forge.Merges()) != 0 {
		t.Fatal("a demoted flight merged under its old green posture")
	}
	wantOutcomes(t, w, "holding:merge.posture")
	if !slices.Contains(types(t, w, "f1"), "posture_tightened") {
		t.Fatalf("no posture_tightened in %v", types(t, w, "f1"))
	}
	if err := w.Tower.Approve(ctx, "f1", policy.ByOwner); err != nil {
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	checkInvariants(t, w)
}

// A promotion never loosens a flight in flight: Tighten to a looser posture records nothing, and
// an owner flight keeps holding for the owner.
func TestAPromotionDoesNotLoosenAFlyingFlight(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 62)
	dispatch(t, w, spec("f1", policy.PostureOwner))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateHolding, "")
	for _, looser := range []policy.Posture{policy.PostureGreen, policy.PostureOwner} {
		if done, err := w.Tower.Tighten(ctx, "f1", looser, "its Runway was promoted"); done || err != nil {
			t.Fatalf("tightening to %s: %v, %v", looser, done, err)
		}
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateHolding, "")
	if len(w.Forge.Merges()) != 0 || slices.Contains(types(t, w, "f1"), "posture_tightened") {
		t.Fatalf("a promotion loosened a flying flight: merges %v, events %v", w.Forge.Merges(), types(t, w, "f1"))
	}
}

func TestStricter(t *testing.T) {
	for _, c := range []struct {
		a, b policy.Posture
		want bool
	}{
		{policy.PostureOwner, policy.PostureGreen, true},
		{policy.PostureClient, policy.PostureOwner, true},
		{policy.PostureGreen, policy.PostureOwner, false},
		{policy.PostureOwner, policy.PostureOwner, false},
		{policy.PostureGreen, "", true},
		{"", policy.PostureGreen, false},
		{"yolo", policy.PostureGreen, false},
	} {
		if got := tower.Stricter(c.a, c.b); got != c.want {
			t.Errorf("Stricter(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
