package sim

import (
	"context"
	"errors"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// DemoOutcomes is how each flight of Demo ends: its state, and the grounding reason.
var DemoOutcomes = map[string]string{
	"a-takeoff": "takeoff", "b-owner": "takeoff", "c-red": "grounded/stalled", "d-flaky": "takeoff",
	"e-ask": "takeoff", "f-crash": "takeoff", "g-hang": "grounded/pilot_timeout",
}

// Demo flies a busy airspace in w: one flight of each kind (green posture, owner posture, red
// checks, a flaky check, a Pilot that asks, one that crashes and is replaced, one that hangs),
// with an owner who answers every question and approves every holding merge. opts applies to
// every Run, so a fault hook sees the whole demo.
func Demo(ctx context.Context, w *World, opts RunOptions) error {
	flight := func(id string, posture policy.Posture) tower.Spec {
		return tower.Spec{ID: id, Org: "acme", Project: "web", Goal: "demo flight " + id, Posture: posture}
	}
	w.Runtime.Script("f-crash", Work(7*time.Minute), Crash())
	w.Runtime.Script("f-crash", DefaultScript()...)
	for _, d := range []struct {
		spec  tower.Spec
		steps []Step
	}{
		{flight("a-takeoff", policy.PostureGreen), nil},
		{flight("b-owner", policy.PostureOwner), []Step{Work(30 * time.Minute), OpenPR(Passing("ci", 10*time.Minute), Passing("lint", time.Minute))}},
		{flight("c-red", policy.PostureGreen), []Step{Work(time.Hour), OpenPR(Failing("ci", 5*time.Minute))}},
		{flight("d-flaky", policy.PostureGreen), []Step{Work(15 * time.Minute), OpenPR(Flaky("e2e", 20*time.Minute))}},
		{flight("e-ask", policy.PostureGreen), []Step{Work(5 * time.Minute), Ask("which region?"), Work(10 * time.Minute), OpenPR(Passing("ci", 3*time.Minute))}},
		{flight("f-crash", policy.PostureGreen), nil},
		{flight("g-hang", policy.PostureGreen), []Step{Hang()}},
	} {
		if err := w.Dispatch(ctx, d.spec, d.steps...); err != nil {
			return err
		}
	}
	for range 10 {
		if err := w.Run(ctx, opts); err != nil {
			return err
		}
		if _, active := w.Tower.NextWake(); !active {
			return nil
		}
		for _, f := range w.Tower.Flights() {
			var err error
			switch f.State {
			case tower.StateAsking:
				err = w.Tower.Answer(ctx, f.ID, "eu-west")
			case tower.StateHolding:
				err = w.Tower.Approve(ctx, f.ID, policy.ByOwner)
			}
			if err != nil {
				return err
			}
		}
	}
	return errors.New("sim: the demo did not finish")
}
