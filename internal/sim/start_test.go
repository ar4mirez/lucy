package sim

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// A Pilot start the runtime refuses for good (tower.ErrStartRefused) grounds the flight at once,
// as before; nothing is retried (LUCY-SPEC-008).
func TestARefusedStartGrounds(t *testing.T) {
	w := newWorld(t, 51)
	w.Runtime.FailStarts(1, fmt.Errorf("%w: the run id is held by another flight", tower.ErrStartRefused))
	dispatch(t, w, spec("f1", policy.PostureGreen))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateGrounded, tower.GroundStartFailed)
	if len(w.Runtime.Attempts) != 1 {
		t.Fatalf("a refused start was tried %d times", len(w.Runtime.Attempts))
	}
}

// While its start keeps failing, a flight stays dispatched, each failure recorded, and its status
// says why and when it is tried next; the start succeeds once the outage ends.
func TestAFailedStartIsRecordedAndShown(t *testing.T) {
	w := newWorld(t, 52)
	w.Runtime.FailStarts(2, errors.New("the store is busy"))
	dispatch(t, w, spec("f1", policy.PostureGreen))
	stepAfter(t, w, 0)
	st := state(t, w, "f1")
	if st.State != tower.StateDispatched || !strings.Contains(st.StepError, "the store is busy") || st.Failures != 1 ||
		!st.RetryAt.Equal(w.Clock.Now().Add(time.Second)) {
		t.Fatalf("after one failed start: %+v", st)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	if got := strings.Join(types(t, w, "f1")[:4], " "); got != "dispatched pilot_start_failed pilot_start_failed pilot_started" {
		t.Fatalf("events %s", got)
	}
}
