package sim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/tower"
)

// A World with Options.Runners set flies its Pilots through the runner protocol: the Tower starts
// them through runner.Runtime on the daemon's broker, and that many in-process runners lease the
// runs and play them as the World's fake Pilot sessions. The runners live outside the daemon, as
// the Pilot sessions do, so a Restart or an outage (Down) reaches the broker and the Tower only.

// RunnerTTL is the lease life a World's runners ask for.
const RunnerTTL = runner.MaxTTL

// runnerStep is the most virtual time a World with runners lets pass between two steps, so each
// runner renews its lease well within RunnerTTL.
const runnerStep = time.Minute

// errDown is what a runner's call gets while the daemon is down.
var errDown = errors.New("sim: the daemon is down")

// protocol is the runners' connection to whichever daemon is up, as the unix socket is.
type protocol struct{ w *World }

func (p protocol) broker() (*runner.Broker, error) {
	if p.w.Broker == nil {
		return nil, errDown
	}
	return p.w.Broker, nil
}

func (p protocol) Queued(ctx context.Context, pl runner.Placement, org string) (int, error) {
	b, err := p.broker()
	if err != nil {
		return 0, err
	}
	return b.Queued(ctx, pl, org)
}

func (p protocol) Lease(ctx context.Context, id string, pl runner.Placement, org, proof string, ttl time.Duration) (runner.Lease, bool, error) {
	b, err := p.broker()
	if err != nil {
		return runner.Lease{}, false, err
	}
	return b.Lease(ctx, id, pl, org, proof, ttl)
}

func (p protocol) Heartbeat(ctx context.Context, id, run string, fence int64, ttl time.Duration, evs []runner.Event, acked string) (runner.Beat, error) {
	b, err := p.broker()
	if err != nil {
		return runner.Beat{}, err
	}
	return b.Heartbeat(ctx, id, run, fence, ttl, evs, acked)
}

func (p protocol) Finish(ctx context.Context, id, run string, fence int64, res runner.Result) error {
	b, err := p.broker()
	if err != nil {
		return err
	}
	return b.Finish(ctx, id, run, fence, res)
}

// executor plays a leased run as a fake Pilot session of the World's Runtime.
type executor struct{ w *World }

type execution struct {
	rt       *Runtime
	session  string
	activity time.Time
	asked    string
}

func (x executor) Start(ctx context.Context, l runner.Lease) (runner.Execution, error) {
	s, err := x.w.Runtime.Start(ctx, tower.PilotRequest{Flight: l.Flight, Org: l.Org, Project: l.Project, Goal: l.Goal,
		Base: l.Base, Generation: l.Generation})
	if err != nil {
		return nil, err
	}
	x.w.sessions[l.Run] = s
	return &execution{rt: x.w.Runtime, session: s}, nil
}

// Poll reports the session's progress as runner events: activity when it did something, its
// question when it asks one, and its result once it ended.
func (x *execution) Poll() runner.Update {
	rep, _ := x.rt.Poll(context.Background(), x.session) // a fake session's Poll never fails
	var u runner.Update
	if rep.Activity.After(x.activity) {
		x.activity = rep.Activity
		u.Events = append(u.Events, runner.Event{Kind: runner.KindActivity, Text: "working"})
	}
	switch rep.State {
	case tower.PilotAsking:
		if rep.Question != x.asked {
			x.asked = rep.Question
			u.Events = append(u.Events, runner.Event{Kind: runner.KindAsking, Text: rep.Question, Question: rep.Question})
		}
	case tower.PilotDone:
		u.Result = &runner.Result{Outcome: runner.OutcomeDone, PR: rep.PR}
	case tower.PilotFailed:
		u.Result = &runner.Result{Outcome: runner.OutcomeFailed, Reason: rep.Reason}
	case tower.PilotLost:
		u.Result = &runner.Result{Outcome: runner.OutcomeLost, Reason: "the session crashed"}
	}
	return u
}

// Answer gives the session the owner's answer, relayed by the runner.
func (x *execution) Answer(text string) error {
	return x.rt.Answer(context.Background(), x.session, text)
}

// Stop ends the session.
func (x *execution) Stop() { _ = x.rt.Stop(context.Background(), x.session) } // a fake session's Stop never fails

// newRunners makes n runners for org's runs, named runner-1, runner-2, ...
func (w *World) newRunners(n int, org string) error {
	for i := range n {
		r, err := runner.New(runner.Config{ID: fmt.Sprintf("runner-%d", i+1), Org: org, Protocol: protocol{w},
			Executor: executor{w}, Now: w.Clock.Now, TTL: RunnerTTL, Log: slog.New(slog.DiscardHandler)})
		if err != nil {
			return err
		}
		w.Runners = append(w.Runners, r)
	}
	return nil
}

// stepRunners steps every runner not cut off, then expires what the broker finds silent, as the
// daemon's timer does.
func (w *World) stepRunners(ctx context.Context) error {
	for i, r := range w.Runners {
		if w.cut[i] {
			continue
		}
		if err := r.Step(ctx); err != nil {
			return fmt.Errorf("sim: runner-%d: %w", i+1, err)
		}
	}
	_, err := w.Broker.Expire(ctx)
	return err
}

// Cut is a fault: runner i (from 0) is cut off from the daemon until Reconnect. Its Pilot session
// carries on, unheard.
func (w *World) Cut(i int) error {
	if i < 0 || i >= len(w.Runners) {
		return fmt.Errorf("sim: no runner %d", i)
	}
	w.cut[i] = true
	return nil
}

// Reconnect ends Cut.
func (w *World) Reconnect(i int) error {
	if i < 0 || i >= len(w.Runners) {
		return fmt.Errorf("sim: no runner %d", i)
	}
	delete(w.cut, i)
	return nil
}

// Down is a fault: the daemon is down for d of virtual time, then starts again, as Restart does.
// Runners and Pilot sessions carry on meanwhile, unheard.
func (w *World) Down(ctx context.Context, d time.Duration) error {
	if err := w.Close(); err != nil {
		return err
	}
	w.Clock.Advance(d)
	return w.open(ctx)
}

// Session returns the fake Pilot session behind a Tower session: itself, or with runners the
// session playing the run.
func (w *World) Session(id string) string {
	if s, ok := w.sessions[id]; ok {
		return s
	}
	return id
}
