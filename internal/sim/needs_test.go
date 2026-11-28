package sim

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// A Pilot whose harness has no login is held for the owner, not wedged (LUCY-RUN-008): the Tower
// asks, in its own words, what to do and where, never for a secret; no timeout grounds the flight
// while it waits; and the owner's answer starts the next generation, which flies on.
func TestAFlightNeedingALoginIsHeldForTheOwner(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 71)
	w.Runtime.Script("f1", NeedsLogin())
	w.Runtime.Script("f1", DefaultScript()...)
	dispatch(t, w, spec("f1", policy.PostureGreen))
	run(t, w, RunOptions{})
	st := state(t, w, "f1")
	if st.State != tower.StateAsking || st.Generation != 1 {
		t.Fatalf("flight %+v, want asking after generation 1", st)
	}
	for _, want := range []string{"no login for its harness", "runner sim-runner", "HOME", "answer here", "Do not paste a password, token or key"} {
		if !strings.Contains(st.Question, want) {
			t.Errorf("the question lacks %q: %s", want, st.Question)
		}
	}
	// Waiting on the owner is never timed out.
	stepAfter(t, w, 30*24*time.Hour)
	wantState(t, w, "f1", tower.StateAsking, "")
	if err := w.Tower.Answer(ctx, "f1", "done"); err != nil {
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	if st := state(t, w, "f1"); st.Generation != 2 || len(w.Runtime.Started) != 2 {
		t.Fatalf("flight %+v, starts %d; want the answer to start generation 2", st, len(w.Runtime.Started))
	}
	if got := types(t, w, "f1"); !slices.Contains(got, "pilot_asked") || !slices.Contains(got, "pilot_answered") || slices.Contains(got, "grounded") {
		t.Fatalf("events %v", got)
	}
	checkInvariants(t, w)
}
