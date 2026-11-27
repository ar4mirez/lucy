package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/ar4mirez/lucy/internal/tower"
)

// Runtime is the Tower's Runtime seam over the runner protocol: starting a Pilot queues a run for
// a runner to lease, and polling it reads what the run's runner streamed. The session id the
// Tower records is the run id.
type Runtime struct {
	Broker *Broker
}

var _ tower.Runtime = Runtime{}

// Start implements tower.Runtime: it queues the flight's Pilot generation as a run. Starting a
// generation that is already queued or running returns its run rather than a second one: the
// Tower asks again when the daemon stopped after queueing the run but before recording its start.
func (r Runtime) Start(ctx context.Context, req tower.PilotRequest) (string, error) {
	run := RunID(req.Flight, req.Generation)
	err := r.Broker.Submit(ctx, Task{Run: run, Org: req.Org, Project: req.Project, Flight: req.Flight,
		Generation: req.Generation, Goal: req.Goal, Base: req.Base})
	if errors.Is(err, ErrExists) {
		ri, rerr := r.Broker.Run(ctx, run)
		if rerr == nil && ri.Org == req.Org && ri.Flight == req.Flight && ri.Generation == req.Generation {
			return run, nil
		}
	}
	if err != nil {
		return "", err
	}
	return run, nil
}

// Poll implements tower.Runtime. A run whose lease has expired is recorded as expired first, so
// a runner that went silent is seen as a lost Pilot, never as a busy one.
func (r Runtime) Poll(ctx context.Context, session string) (tower.PilotReport, error) {
	if _, err := r.Broker.expire(ctx, session); err != nil {
		return tower.PilotReport{}, err
	}
	ri, err := r.Broker.Run(ctx, session)
	if err != nil {
		return tower.PilotReport{}, err
	}
	switch ri.State {
	case StateQueued:
		return tower.PilotReport{State: tower.PilotRunning}, nil // waiting for a runner; the Pilot timeout still applies
	case StateLeased:
		if ri.Asking {
			return tower.PilotReport{State: tower.PilotAsking, Activity: ri.Activity, Question: ri.Question}, nil
		}
		return tower.PilotReport{State: tower.PilotRunning, Activity: ri.Activity}, nil
	case StateDone:
		if ri.PR <= 0 {
			return tower.PilotReport{State: tower.PilotFailed, Reason: "the run finished without a pull request"}, nil
		}
		return tower.PilotReport{State: tower.PilotDone, PR: ri.PR}, nil
	case StateFailed:
		return tower.PilotReport{State: tower.PilotFailed, Reason: ri.Reason}, nil
	case StateStopped:
		return tower.PilotReport{State: tower.PilotFailed, Reason: "the run was stopped"}, nil
	case StateLost:
		return tower.PilotReport{State: tower.PilotLost}, nil
	}
	return tower.PilotReport{}, fmt.Errorf("runner: run %s in unknown state %q", session, ri.State)
}

// Answer implements tower.Runtime. No executor takes input yet: answering a Pilot needs the
// structured harness adapter. TODO(LUCY-RUN-002): deliver the owner's answer to the run through
// the Claude Code adapter (m1 step 2).
func (Runtime) Answer(context.Context, string, string) error { return ErrNoInput }

// Stop implements tower.Runtime.
func (r Runtime) Stop(ctx context.Context, session string) error { return r.Broker.Stop(ctx, session) }
