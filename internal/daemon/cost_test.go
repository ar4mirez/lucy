package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
)

// A run's step usage and reported cost reach its flight in flights.list (LUCY-RUN-011).
func TestAFlightsCostReachesFlightsList(t *testing.T) {
	dir := shortTempDir(t)
	o := opts(dir)
	o.TowerPollInterval = 20 * time.Millisecond
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
			t.Fatal("no run was queued")
		}
	}
	if _, err := c.RunnerHeartbeat(ctx, api.HeartbeatParams{Runner: "acme-host-1", Run: l.Run.Run, Fence: l.Run.Fence,
		Events: []api.RunEvent{{Seq: 1, Kind: "usage", Text: "tokens", Tokens: api.Tokens{Input: 1200, Output: 40}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunnerFinish(ctx, api.FinishParams{Runner: "acme-host-1", Run: l.Run.Run, Fence: l.Run.Fence, Outcome: "done", PR: 4, CostMicros: 41200}); err != nil {
		t.Fatal(err)
	}
	for _, f := range flightList(t, c) {
		if f.Name != "fix-login" {
			continue
		}
		if f.Runs != 1 || f.Tokens != (api.Tokens{Input: 1200, Output: 40}) || f.CostMicros != 41200 {
			t.Fatalf("flights.list: runs %d, tokens %+v, cost %d", f.Runs, f.Tokens, f.CostMicros)
		}
		return
	}
	t.Fatal("no fix-login in flights.list")
}
