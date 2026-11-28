package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
)

// The Tower is a converge loop woken by the event log (LUCY-SPEC-008): with an hour between polls,
// a Pilot's question, which reaches the log on a runner's heartbeat, reaches lucy questions within
// seconds, because the heartbeat's commit wakes the Tower.
func TestTheLogWakesTheTower(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	o.TowerPollInterval = time.Hour
	o.Dispatch = true
	writeFlights(t, o, "spec: {}")
	c := startClient(t, o)
	ctx := context.Background()
	var l api.LeaseResult
	for deadline := time.Now().Add(10 * time.Second); !l.Leased; time.Sleep(20 * time.Millisecond) {
		var err error
		if l, err = c.RunnerLease(ctx, api.LeaseParams{Runner: "acme-host-1", Org: "acme"}); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the dispatched flight's run was not queued in 10 s")
		}
	}
	if _, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: l.Run.Run, Fence: l.Run.Fence,
		Events: []api.RunEvent{{Seq: 1, Kind: "asking", Text: "which account?", Question: "q-1"}}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		q, err := c.Questions(ctx)
		if err == nil && len(q.Questions) == 1 && q.Questions[0].Question == "which account?" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the question did not reach lucy questions within 5 s of the heartbeat, with an hour between polls: %+v, %v", q, err)
		}
	}
}
