// Package sim is lucy's simulation harness (LUCY-SIM-001): a virtual clock, a fake forge and a
// fake Pilot runtime behind the Tower's seams (internal/tower), and a World that drives whole
// flights from dispatch to takeoff on a real event store in a temporary directory, without
// agents, network, git or wall-clock time.
//
// A World is deterministic: its only randomness is one generator seeded by the caller, which
// draws commit SHAs and work-time jitter; its clock moves only when the World advances it; and
// the Tower visits flights in a fixed order. The same seed and the same script give the same
// event log, byte for byte (the determinism test enforces it).
//
// Fault hooks for the chaos suite (LUCY-SIM-003, m1): World.Restart reopens the event store and
// the Tower mid-flight, as a daemon restart does; a Crash step or Runtime.Kill loses a Pilot
// session; the forge can lose a merge's reply, report a conflict or take a push from someone else.
//
// Nothing here is safe for concurrent use; a World runs on one goroutine.
package sim

import (
	"time"
)

// Epoch is where a World's clock starts unless told otherwise.
var Epoch = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)

// Clock is a virtual clock: it moves only when told to. It implements tower.Clock and is the
// event store's clock in a World, so every event's time is virtual too.
type Clock struct {
	now time.Time
}

// NewClock returns a clock at start.
func NewClock(start time.Time) *Clock { return &Clock{now: start.UTC()} }

// Now implements tower.Clock.
func (c *Clock) Now() time.Time { return c.now }

// Advance moves the clock forward by d; a negative d is ignored, so time never runs backwards.
func (c *Clock) Advance(d time.Duration) {
	if d > 0 {
		c.now = c.now.Add(d)
	}
}
