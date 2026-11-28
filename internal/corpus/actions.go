package corpus

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/pathguard"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/sim"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Proceed steps the Tower on virtual time until every flight is final or waits on the owner.
func Proceed() Action {
	return Action{"proceed", func(ctx context.Context, e *Env) error {
		return e.World.Run(ctx, sim.RunOptions{BeforeStep: e.beforeStep})
	}}
}

// Wait moves virtual time on by d without stepping: the owner is away.
func Wait(d time.Duration) Action {
	return Action{"wait " + d.String(), func(_ context.Context, e *Env) error {
		e.World.Clock.Advance(d)
		return nil
	}}
}

// Restart restarts the daemon now.
func Restart() Action {
	return Action{"restart", func(ctx context.Context, e *Env) error { return e.World.Restart(ctx) }}
}

// Approve is the owner (or, with ByClientViaOwner, the owner relaying the client) approving the
// flight's merge as last shown.
func Approve(flight string, by policy.ApprovalBy) Action {
	return Action{"approve " + flight + " " + string(by), func(ctx context.Context, e *Env) error {
		return e.World.Tower.Approve(ctx, flight, by)
	}}
}

// Answer is the owner answering the flight's Pilot.
func Answer(flight, answer string) Action {
	return Action{"answer " + flight, func(ctx context.Context, e *Env) error {
		return e.World.Tower.Answer(ctx, flight, answer)
	}}
}

// AnswerRefused is an answer sent to a flight whose Pilot is not asking; the Tower must refuse it.
func AnswerRefused(flight, answer string) Action {
	return Action{"answer refused " + flight, func(ctx context.Context, e *Env) error {
		if err := e.World.Tower.Answer(ctx, flight, answer); err == nil {
			return errors.New("the Tower delivered an answer to a Pilot that was not asking")
		}
		return nil
	}}
}

// ApproveRefused is an approval of a flight with no pull request to approve; the Tower must
// refuse it.
func ApproveRefused(flight string) Action {
	return Action{"approve refused " + flight, func(ctx context.Context, e *Env) error {
		if err := e.World.Tower.Approve(ctx, flight, policy.ByOwner); err == nil {
			return errors.New("the Tower recorded an approval of a flight with no pull request")
		}
		return nil
	}}
}

// OnState arms actions to run once, before the first Tower step that finds the flight in state.
func OnState(flight string, s tower.State, actions ...Action) Action {
	name := fmt.Sprintf("when %s is %s", flight, s)
	return Action{name, func(_ context.Context, e *Env) error {
		e.hooks = append(e.hooks, &hook{name: name, do: actions, when: func(_ context.Context, e *Env) (bool, error) {
			st, err := e.flight(flight)
			return err == nil && st.State == s, err
		}})
		return nil
	}}
}

// OnEvent arms actions to run once, before the first Tower step after the flight's nth event of
// type typ (without its "task." prefix) was recorded.
func OnEvent(flight, typ string, nth int, actions ...Action) Action {
	name := fmt.Sprintf("after %s's %s #%d", flight, typ, nth)
	return Action{name, func(_ context.Context, e *Env) error {
		e.hooks = append(e.hooks, &hook{name: name, do: actions, when: func(ctx context.Context, e *Env) (bool, error) {
			evs, err := e.events(ctx, flight)
			n := 0
			for _, l := range evs {
				if l.Type == "task."+typ {
					n++
				}
			}
			return n >= nth, err
		}})
		return nil
	}}
}

func (e *Env) pr(flight string) (int, error) {
	st, err := e.flight(flight)
	if err == nil && st.PR == 0 {
		err = fmt.Errorf("flight %s has no pull request", flight)
	}
	return st.PR, err
}

// Push is someone else pushing a commit to the flight's pull request.
func Push(flight string) Action {
	return Action{"push to " + flight, func(_ context.Context, e *Env) error {
		n, err := e.pr(flight)
		if err == nil {
			_, err = e.World.Forge.Push(Repo, n)
		}
		return err
	}}
}

// MoveBase is someone else landing a change on the base branch.
func MoveBase() Action {
	return Action{"move base", func(_ context.Context, e *Env) error {
		_, err := e.World.Forge.MoveBase(Repo, "main")
		return err
	}}
}

// MergeOnForge is account merging the flight's pull request on the forge, outside lucy.
func MergeOnForge(flight, account string) Action {
	return Action{"merge on forge " + flight + " as " + account, func(_ context.Context, e *Env) error {
		n, err := e.pr(flight)
		if err == nil {
			err = e.World.Forge.MergeAs(Repo, n, account)
		}
		return err
	}}
}

// Conflict makes the flight's pull request conflict with its base, or not.
func Conflict(flight string, on bool) Action {
	return Action{"conflict " + flight + " " + strconv.FormatBool(on), func(_ context.Context, e *Env) error {
		n, err := e.pr(flight)
		if err == nil {
			err = e.World.Forge.SetConflict(Repo, n, on)
		}
		return err
	}}
}

// FailStarts makes the runtime fail the next times Pilot starts with msg: an outage, from which the
// Tower recovers by retrying.
func FailStarts(times int, msg string) Action {
	return Action{fmt.Sprintf("fail the next %d Pilot starts", times), func(_ context.Context, e *Env) error {
		e.World.Runtime.FailStarts(times, errors.New(msg))
		return nil
	}}
}

// FailMerges makes the forge refuse the next times merges of the flight (every one when -1)
// with msg, before they happen.
func FailMerges(flight string, times int, msg string) Action {
	return Action{"fail merges of " + flight, func(_ context.Context, e *Env) error {
		n, err := e.pr(flight)
		if err == nil {
			err = e.World.Forge.FailMerge(Repo, n, times, errors.New(msg))
		}
		return err
	}}
}

// LoseMergeReply makes the forge perform the flight's next merge but lose its reply.
func LoseMergeReply(flight string) Action {
	return Action{"lose the merge reply of " + flight, func(_ context.Context, e *Env) error {
		n, err := e.pr(flight)
		if err == nil {
			err = e.World.Forge.LoseMergeReply(Repo, n)
		}
		return err
	}}
}

// KillPilot kills the flight's current Pilot session.
func KillPilot(flight string) Action {
	return Action{"kill the Pilot of " + flight, func(_ context.Context, e *Env) error {
		st, err := e.flight(flight)
		if err == nil {
			err = e.World.Runtime.Kill(e.World.Session(st.Session))
		}
		return err
	}}
}

// DaemonDown is a fault: the daemon is down for d of virtual time, then starts again. Runners and
// Pilot sessions carry on meanwhile, unheard.
func DaemonDown(d time.Duration) Action {
	return Action{"the daemon is down for " + d.String(), func(ctx context.Context, e *Env) error { return e.World.Down(ctx, d) }}
}

// CutRunner is a fault: runner i (from 0) is cut off from the daemon; its Pilot carries on unheard.
func CutRunner(i int) Action {
	return Action{fmt.Sprintf("cut runner %d off", i), func(_ context.Context, e *Env) error { return e.World.Cut(i) }}
}

// ReconnectRunner ends CutRunner.
func ReconnectRunner(i int) Action {
	return Action{fmt.Sprintf("reconnect runner %d", i), func(_ context.Context, e *Env) error { return e.World.Reconnect(i) }}
}

// OnLeased arms actions to run once, before the first Tower step after a runner leased the
// flight's Pilot generation gen and streamed its first event. It needs Setup.Runners.
func OnLeased(flight string, gen int, actions ...Action) Action {
	name := fmt.Sprintf("when %s generation %d is leased", flight, gen)
	return Action{name, func(_ context.Context, e *Env) error {
		e.hooks = append(e.hooks, &hook{name: name, do: actions, when: func(ctx context.Context, e *Env) (bool, error) {
			if e.World.Broker == nil {
				return false, errors.New("OnLeased needs Setup.Runners")
			}
			r, err := e.World.Broker.Run(ctx, runner.RunID(flight, gen))
			if errors.Is(err, runner.ErrNoRun) {
				return false, nil
			}
			return err == nil && r.State == runner.StateLeased && r.Accepted > 0, err
		}})
		return nil
	}}
}

// Expecting checks an assertion now, in the middle of a scenario.
func Expecting(a Assertion) Action {
	return Action{"expecting " + a.Name, a.Check}
}

// Do is a scenario-specific step.
func Do(name string, fn func(ctx context.Context, e *Env) error) Action { return Action{name, fn} }

// Actors used by request scenarios.
var (
	OwnerActor  = policy.Actor{ID: "owner", Role: policy.RoleOwner}
	DaemonActor = policy.Actor{ID: tower.ActorDaemon, Role: policy.RoleDaemon}
)

// Agent is an agent of role in org.
func Agent(role policy.Role, org string) policy.Actor {
	return policy.Actor{ID: org + "/" + string(role), Role: role, Org: org}
}

// TaskInput is the trusted basis every flight-driven request has: its task record.
var TaskInput = policy.Input{Source: policy.SourceTask, Ref: "task/" + Org + "/f1"}

// Request puts req to the policy engine under facts, through the Tower's recording seam, and
// keeps the decision under name.
func Request(name string, req policy.Request, facts policy.Facts) Action {
	return Action{"request " + name, func(ctx context.Context, e *Env) error {
		return e.request(ctx, name, req, facts)
	}}
}

func (e *Env) request(ctx context.Context, name string, req policy.Request, facts policy.Facts) error {
	if _, ok := e.built[name]; ok {
		return fmt.Errorf("request %q is named twice", name)
	}
	d, seq, err := e.World.Tower.Request(ctx, req.Actor.ID, req, facts)
	if err != nil {
		return err
	}
	e.built[name] = built{req, facts}
	e.requests[name] = decided{d, seq}
	return nil
}

// Option adjusts a request's facts before it is put to the engine.
type Option func(e *Env, req policy.Request, f *policy.Facts) error

// ApprovedAs adds the owner's approval of the earlier request named other, under the facts that
// request was decided on: what the owner was shown.
func ApprovedAs(other string) Option {
	return approvedAs(other, policy.ByOwner)
}

func approvedAs(other string, by policy.ApprovalBy) Option {
	return func(e *Env, _ policy.Request, f *policy.Facts) error {
		b, ok := e.built[other]
		if !ok {
			return fmt.Errorf("no earlier request %q", other)
		}
		f.Approvals = append(f.Approvals, policy.Approval{Decision: "decision/" + other, By: by,
			ActionSHA256: policy.ActionDigest(b.req), FactsSHA256: policy.FactsDigest(b.req.Kind, b.facts)})
		return nil
	}
}

// OwnerApproved adds the owner's approval of this very request under the facts it is decided on.
func OwnerApproved() Option {
	return func(_ *Env, req policy.Request, f *policy.Facts) error {
		f.Approvals = append(f.Approvals, policy.Approval{Decision: "decision/self", By: policy.ByOwner,
			ActionSHA256: policy.ActionDigest(req), FactsSHA256: policy.FactsDigest(req.Kind, *f)})
		return nil
	}
}

// WithFacts edits the facts directly.
func WithFacts(edit func(f *policy.Facts)) Option {
	return func(_ *Env, _ policy.Request, f *policy.Facts) error {
		edit(f)
		return nil
	}
}

func (e *Env) apply(req policy.Request, f *policy.Facts, opts []Option) error {
	for _, o := range opts {
		if err := o(e, req, f); err != nil {
			return err
		}
	}
	return nil
}

// MergeRequest is actor asking to merge the flight's pull request, backed by inputs, with the
// facts the daemon gathers for it: the flight's posture and fixed-list categories and the pull
// request as the Tower last observed it.
func MergeRequest(name, flight string, actor policy.Actor, inputs []policy.Input, opts ...Option) Action {
	return Action{"request " + name, func(ctx context.Context, e *Env) error {
		st, err := e.flight(flight)
		if err != nil {
			return err
		}
		spec, err := flightSpec(ctx, e, flight)
		if err != nil {
			return err
		}
		req := policy.Request{Actor: actor, Kind: policy.KindMerge, Inputs: inputs,
			Target: policy.Target{Org: Org, Project: Project, Resource: "pr/" + strconv.Itoa(st.PR)}}
		o := st.Observed
		facts := policy.Facts{Posture: spec.Posture, Exceptions: spec.Exceptions, Checks: o.Checks,
			RequiredUnreported: o.RequiredUnreported, HeadSHA: o.HeadSHA, BaseRef: o.BaseRef, BaseSHA: o.BaseSHA}
		if err := e.apply(req, &facts, opts); err != nil {
			return err
		}
		return e.request(ctx, name, req, facts)
	}}
}

// flightSpec reads the flight's spec back from its dispatched event: the posture and fixed-list
// categories the Tower decides its merges on.
func flightSpec(ctx context.Context, e *Env, id string) (tower.Spec, error) {
	var s tower.Spec
	err := e.World.Store.Replay(ctx, 0, func(ev eventstore.Event) error {
		if ev.Stream != e.streams[id] || ev.Type != (tower.Dispatched{}).EventType() {
			return nil
		}
		d, err := eventstore.DecodeAs[tower.Dispatched](e.World.Store.Registry(), ev)
		if err == nil {
			s = tower.Spec{ID: id, Org: d.Org, Project: d.Project, Posture: d.Posture, Exceptions: d.Exceptions, Delivery: d.Delivery, Acceptance: d.Acceptance}
		}
		return err
	})
	if err == nil && s.ID == "" {
		err = fmt.Errorf("no flight %q", id)
	}
	return s, err
}

// Delete is actor asking to delete path (with fixture placeholders), backed by the flight's task
// record, with the facts the daemon gathers: the resolved home, the roots and the path after
// symlink resolution.
func Delete(name string, actor policy.Actor, path string, opts ...Option) Action {
	return Action{"request " + name, func(ctx context.Context, e *Env) error {
		p := e.Fixture.Expand(path)
		req := policy.Request{Actor: actor, Kind: policy.KindDelete, Inputs: []policy.Input{TaskInput},
			Target: policy.Target{Org: Org, Project: Project, Resource: p}}
		facts := policy.Facts{Home: e.Fixture.Home, Roots: e.Fixture.Roots(), ResolvedPath: e.Fixture.Resolve(p)}
		if err := e.apply(req, &facts, opts); err != nil {
			return err
		}
		return e.request(ctx, name, req, facts)
	}}
}

// Git is actor asking for a force-push, branch delete or history rewrite of branch, backed by the
// flight's task record, in a project whose default branch is defaultBranch ("" when unknown).
func Git(name string, actor policy.Actor, kind policy.Kind, branch, defaultBranch string, opts ...Option) Action {
	return Action{"request " + name, func(ctx context.Context, e *Env) error {
		req := policy.Request{Actor: actor, Kind: kind, Inputs: []policy.Input{TaskInput},
			Target: policy.Target{Org: Org, Project: Project, Resource: branch}}
		facts := policy.Facts{DefaultBranch: defaultBranch}
		if err := e.apply(req, &facts, opts); err != nil {
			return err
		}
		return e.request(ctx, name, req, facts)
	}}
}

// Repoint re-points a fixture symlink.
func Repoint(link, target string) Action {
	return Action{"repoint " + link, func(_ context.Context, e *Env) error { return e.Fixture.Repoint(link, target) }}
}

// Remove runs the delete executor itself on path (with fixture placeholders): a pathguard.Guard
// over the fixture's home and roots, and its RemoveAll. What it returned is kept under name; a
// refusal is not a failed step. Every delete it makes is inside the fixture's own temporary
// directory, which is the only place the guard's roots are.
func Remove(name, path string) Action {
	return Action{"remove " + name, func(_ context.Context, e *Env) error {
		if _, ok := e.removed[name]; ok {
			return fmt.Errorf("remove %q is named twice", name)
		}
		g, err := pathguard.New(e.Fixture.Home, e.Fixture.Roots()...)
		if err != nil {
			return err
		}
		err = g.RemoveAll(e.Fixture.Expand(path))
		if err != nil && !errors.Is(err, pathguard.ErrRefused) {
			return err
		}
		e.removed[name] = err
		return nil
	}}
}
