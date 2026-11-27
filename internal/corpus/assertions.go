package corpus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Ends asserts the flight's state and reason ("" for none).
func Ends(flight string, s tower.State, reason string) Assertion {
	return Assertion{fmt.Sprintf("%s ends %s %q", flight, s, reason), func(_ context.Context, e *Env) error {
		st, err := e.flight(flight)
		if err == nil && (st.State != s || st.Reason != reason) {
			err = fmt.Errorf("flight %s is %s (%q)", flight, st.State, st.Reason)
		}
		return err
	}}
}

func (e *Env) outcomes(ctx context.Context) ([]string, error) {
	ds, err := e.World.Decisions(ctx)
	var out []string
	for _, d := range ds {
		out = append(out, string(d.Record.Outcome)+":"+d.Record.Rule)
	}
	return out, err
}

// Decisions asserts every Clearance recorded, in order, as "outcome:rule".
func Decisions(want ...string) Assertion {
	return Assertion{"decisions " + strings.Join(want, " "), func(ctx context.Context, e *Env) error {
		got, err := e.outcomes(ctx)
		if err == nil && strings.Join(got, " ") != strings.Join(want, " ") {
			err = fmt.Errorf("decisions %v", got)
		}
		return err
	}}
}

// DecisionCount asserts how many recorded Clearances are "outcome:rule".
func DecisionCount(want string, n int) Assertion {
	return Assertion{fmt.Sprintf("%d decisions %s", n, want), func(ctx context.Context, e *Env) error {
		got, err := e.outcomes(ctx)
		c := 0
		for _, g := range got {
			if g == want {
				c++
			}
		}
		if err == nil && c != n {
			err = fmt.Errorf("%d, in %v", c, got)
		}
		return err
	}}
}

// Decided asserts the outcome and rule of the request kept under name.
func Decided(name string, outcome policy.Outcome, rule string) Assertion {
	return Assertion{fmt.Sprintf("%s %s by %s", name, outcome, rule), func(_ context.Context, e *Env) error {
		r, ok := e.requests[name]
		switch {
		case !ok:
			return fmt.Errorf("no request %q", name)
		case r.d.Outcome != outcome || r.d.Rule != rule:
			return fmt.Errorf("%s by %s: %s", r.d.Outcome, r.d.Rule, r.d.Reason)
		}
		return nil
	}}
}

// Tainted asserts whether the request kept under name was marked tainted.
func Tainted(name string, want bool) Assertion {
	return Assertion{fmt.Sprintf("%s tainted %v", name, want), func(_ context.Context, e *Env) error {
		r, ok := e.requests[name]
		if !ok || r.d.Tainted != want {
			return fmt.Errorf("request %q tainted %v", name, r.d.Tainted)
		}
		return nil
	}}
}

// Merges asserts how many merges the forge performed, by anyone.
func Merges(n int) Assertion {
	return Assertion{fmt.Sprintf("%d merges", n), func(_ context.Context, e *Env) error {
		if m := e.World.Forge.Merges(); len(m) != n {
			return fmt.Errorf("merges %+v", m)
		}
		return nil
	}}
}

// MergeCalls asserts how many times lucy called the forge's merge.
func MergeCalls(n int) Assertion {
	return Assertion{fmt.Sprintf("%d merge calls", n), func(_ context.Context, e *Env) error {
		if c := e.World.Forge.MergeCalls(); c != n {
			return fmt.Errorf("%d merge calls", c)
		}
		return nil
	}}
}

// Events asserts how many events of type typ (without "task.") the flight recorded.
func Events(flight, typ string, n int) Assertion {
	return Assertion{fmt.Sprintf("%s has %d %s", flight, n, typ), func(ctx context.Context, e *Env) error {
		evs, err := e.events(ctx, flight)
		c := 0
		for _, l := range evs {
			if l.Type == "task."+typ {
				c++
			}
		}
		if err == nil && c != n {
			err = fmt.Errorf("%d", c)
		}
		return err
	}}
}

// EventsEnd asserts the last event types (without "task.") of the flight, in order.
func EventsEnd(flight string, types ...string) Assertion {
	return Assertion{flight + " ends with " + strings.Join(types, " "), func(ctx context.Context, e *Env) error {
		evs, err := e.events(ctx, flight)
		if err != nil {
			return err
		}
		var got []string
		for _, l := range evs {
			got = append(got, strings.TrimPrefix(l.Type, "task."))
		}
		if len(got) < len(types) || !slices.Equal(got[len(got)-len(types):], types) {
			return fmt.Errorf("events %v", got)
		}
		return nil
	}}
}

// EventBy asserts that every event of type typ the flight recorded has actor as its actor, and
// that there is at least one.
func EventBy(flight, typ, actor string) Assertion {
	return Assertion{fmt.Sprintf("%s's %s by %s", flight, typ, actor), func(ctx context.Context, e *Env) error {
		evs, err := e.events(ctx, flight)
		n := 0
		for _, l := range evs {
			if l.Type == "task."+typ {
				n++
				if l.Actor != actor {
					return fmt.Errorf("seq %d by %s", l.Seq, l.Actor)
				}
			}
		}
		if err == nil && n == 0 {
			err = errors.New("none recorded")
		}
		return err
	}}
}

// PilotsStarted asserts how many Pilot sessions the runtime started for the flight.
func PilotsStarted(flight string, n int) Assertion {
	return Assertion{fmt.Sprintf("%s started %d Pilots", flight, n), func(_ context.Context, e *Env) error {
		c := 0
		for _, r := range e.World.Runtime.Started {
			if r.Flight == flight {
				c++
			}
		}
		if c != n {
			return fmt.Errorf("%d started", c)
		}
		return nil
	}}
}

// PilotsStopped asserts how many Pilot sessions the Tower stopped.
func PilotsStopped(n int) Assertion {
	return Assertion{fmt.Sprintf("%d Pilots stopped", n), func(_ context.Context, e *Env) error {
		if c := len(e.World.Runtime.Stopped); c != n {
			return fmt.Errorf("%d stopped", c)
		}
		return nil
	}}
}

// AnswersDelivered asserts how many answers reached any Pilot.
func AnswersDelivered(n int) Assertion {
	return Assertion{fmt.Sprintf("%d answers delivered", n), func(_ context.Context, e *Env) error {
		c := 0
		for _, as := range e.World.Runtime.Answers {
			c += len(as)
		}
		if c != n {
			return fmt.Errorf("%d delivered", c)
		}
		return nil
	}}
}

// FixtureIntact asserts nothing the delete fixture holds was removed.
func FixtureIntact() Assertion {
	return Assertion{"fixture intact", func(_ context.Context, e *Env) error { return e.Fixture.Intact() }}
}

// Holds is a scenario-specific assertion.
func Holds(name string, fn func(ctx context.Context, e *Env) error) Assertion {
	return Assertion{name, fn}
}

// Fenced asserts that the flight's Pilot generation gen lost its run to the broker (its lease
// expired, or a newer generation replaced it) and that nothing was recorded on the run after
// that: whatever its runner sent later was refused. It needs Setup.Runners.
func Fenced(flight string, gen int) Assertion {
	run := runner.RunID(flight, gen)
	return Assertion{run + " was fenced", func(ctx context.Context, e *Env) error {
		if e.World.Broker == nil {
			return errors.New("Fenced needs Setup.Runners")
		}
		r, err := e.World.Broker.Run(ctx, run)
		if err != nil {
			return err
		}
		evs, err := e.World.Store.ReadStream(ctx, runner.Stream(r.Org, run), 0, 100000)
		if err != nil {
			return err
		}
		last := ""
		if len(evs) > 0 {
			last = evs[len(evs)-1].Type
		}
		if r.State != runner.StateLost || (last != "run.expired" && last != "run.replaced") {
			return fmt.Errorf("run %s is %s, its last event %q", run, r.State, last)
		}
		return nil
	}}
}

// Runs asserts how many runs the broker queued for the flight. It needs Setup.Runners.
func Runs(flight string, n int) Assertion {
	return Assertion{fmt.Sprintf("%s had %d runs", flight, n), func(ctx context.Context, e *Env) error {
		if e.World.Broker == nil {
			return errors.New("Runs needs Setup.Runners")
		}
		runs, err := e.World.Broker.Runs(ctx, 0, 1000)
		if err != nil {
			return err
		}
		c := 0
		for _, r := range runs {
			if r.Flight == flight {
				c++
			}
		}
		if c != n {
			return fmt.Errorf("%d runs", c)
		}
		return nil
	}}
}
