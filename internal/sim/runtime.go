package sim

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ar4mirez/lucy/internal/tower"
)

type stepKind int

const (
	stepWork stepKind = iota
	stepAsk
	stepFail
	stepHang
	stepCrash
	stepNeedsLogin
	stepOpenPR
)

// Step is one thing a scripted Pilot does. A script runs its steps in order.
type Step struct {
	kind   stepKind
	d      time.Duration
	text   string
	checks []Check
}

// Work is the Pilot working for about d of virtual time (plus up to a quarter more, drawn from
// the World's seed).
func Work(d time.Duration) Step { return Step{kind: stepWork, d: d} }

// Ask is the Pilot asking the owner a question and waiting for the answer.
func Ask(question string) Step { return Step{kind: stepAsk, text: question} }

// GiveUp is the Pilot failing with a reason.
func GiveUp(reason string) Step { return Step{kind: stepFail, text: reason} }

// Hang is the Pilot running forever without activity.
func Hang() Step { return Step{kind: stepHang} }

// Crash is the Pilot's session dying: the runtime reports it lost.
func Crash() Step { return Step{kind: stepCrash} }

// NeedsLogin is a session that does not start: its harness has no login in the org's home, so the
// run is held for the owner (LUCY-RUN-008).
func NeedsLogin() Step { return Step{kind: stepNeedsLogin} }

// OpenPR is the Pilot pushing its branch and opening a pull request that runs checks, which
// ends the session successfully.
func OpenPR(checks ...Check) Step { return Step{kind: stepOpenPR, checks: checks} }

// DefaultScript is what a Pilot does when its flight has no script: work, then open a pull
// request with one passing check.
func DefaultScript() []Step {
	return []Step{Work(10 * time.Minute), OpenPR(Passing("ci", 5*time.Minute))}
}

// Runtime is a fake Pilot runtime: each session plays its flight's script on virtual time. It
// implements tower.Runtime. Sessions live in the Runtime, outside the Tower, so they survive a
// daemon restart and the new Tower reattaches to them.
type Runtime struct {
	clock    *Clock
	rng      *rand.Rand
	forge    *Forge
	scripts  map[string][][]Step // flight -> the script of each generation
	sessions map[string]*session
	next     int

	Started  []tower.PilotRequest // every Start that started a session, in order
	Attempts []time.Time          // when every Start was called, failed ones included
	Stopped  []string             // every Stop, in order

	failStarts int // the next Starts that fail with startErr
	startErr   error
	Answers    map[string][]string // session -> the answers it got
}

type session struct {
	id       string
	req      tower.PilotRequest
	steps    []Step
	jitter   []time.Duration // per step, drawn at start
	i        int
	at       time.Time // when the current step began
	answered bool
	report   tower.PilotReport
	final    bool
}

func newRuntime(clock *Clock, rng *rand.Rand, forge *Forge) *Runtime {
	return &Runtime{clock: clock, rng: rng, forge: forge, scripts: map[string][][]Step{},
		sessions: map[string]*session{}, Answers: map[string][]string{}}
}

// Script sets what the flight's next Pilot generation does: the first call scripts generation 1,
// the next generation 2, and so on; a generation past the last script replays the last one.
func (r *Runtime) Script(flight string, steps ...Step) {
	r.scripts[flight] = append(r.scripts[flight], steps)
}

func (r *Runtime) script(flight string, gen int) []Step {
	s := r.scripts[flight]
	switch {
	case len(s) == 0:
		return DefaultScript()
	case gen > len(s):
		return s[len(s)-1]
	}
	return s[gen-1]
}

// FailStarts makes the next times Starts fail with err: an outage of the runtime, or a refusal when
// err wraps tower.ErrStartRefused.
func (r *Runtime) FailStarts(times int, err error) { r.failStarts, r.startErr = times, err }

// Start implements tower.Runtime.
func (r *Runtime) Start(_ context.Context, req tower.PilotRequest) (string, error) {
	r.Attempts = append(r.Attempts, r.clock.Now())
	if r.failStarts > 0 {
		r.failStarts--
		return "", r.startErr
	}
	r.next++
	s := &session{id: fmt.Sprintf("sim-%d", r.next), req: req, steps: r.script(req.Flight, req.Generation), at: r.clock.Now()}
	for _, st := range s.steps {
		var j time.Duration
		if st.kind == stepWork && st.d >= 4 {
			j = time.Duration(r.rng.Int64N(int64(st.d / 4)))
		}
		s.jitter = append(s.jitter, j)
	}
	s.report.State = tower.PilotRunning
	r.sessions[s.id] = s
	r.Started = append(r.Started, req)
	return s.id, nil
}

// Poll implements tower.Runtime.
func (r *Runtime) Poll(_ context.Context, id string) (tower.PilotReport, error) {
	s, ok := r.sessions[id]
	if !ok {
		return tower.PilotReport{State: tower.PilotLost}, nil
	}
	r.advance(s)
	return s.report, nil
}

func (r *Runtime) advance(s *session) {
	now := r.clock.Now()
	for !s.final && s.i < len(s.steps) {
		st := s.steps[s.i]
		switch st.kind {
		case stepWork:
			end := s.at.Add(st.d + s.jitter[s.i])
			if now.Before(end) {
				s.report = tower.PilotReport{State: tower.PilotRunning, Activity: now}
				return
			}
			s.at = end
		case stepAsk:
			if !s.answered {
				s.report = tower.PilotReport{State: tower.PilotAsking, Question: st.text}
				return
			}
			s.answered = false
		case stepFail:
			s.report, s.final = tower.PilotReport{State: tower.PilotFailed, Reason: st.text}, true
			return
		case stepNeedsLogin:
			s.report, s.final = tower.PilotReport{State: tower.PilotNeedsOwner, Need: "harness_login", Runner: "sim-runner"}, true
			return
		case stepHang: // running, silent since it hung
			s.report = tower.PilotReport{State: tower.PilotRunning, Activity: s.at}
			return
		case stepCrash:
			s.report, s.final = tower.PilotReport{State: tower.PilotLost}, true
			return
		case stepOpenPR:
			branch := fmt.Sprintf("lucy/%s-%d", s.req.Flight, s.req.Generation)
			n := r.forge.OpenPR(s.req.Org+"/"+s.req.Project, branch, s.req.Base, st.checks, s.at)
			s.report, s.final = tower.PilotReport{State: tower.PilotDone, PR: n}, true
			return
		}
		s.i++
	}
	if !s.final { // the script ran out without a pull request
		s.report, s.final = tower.PilotReport{State: tower.PilotDone}, true
	}
}

// Answer implements tower.Runtime.
func (r *Runtime) Answer(_ context.Context, id, answer string) error {
	s, ok := r.sessions[id]
	if !ok || s.final {
		return fmt.Errorf("sim: session %q is not running", id)
	}
	r.advance(s)
	if s.report.State != tower.PilotAsking {
		return fmt.Errorf("sim: session %q asked nothing", id)
	}
	s.answered, s.at = true, r.clock.Now()
	r.Answers[id] = append(r.Answers[id], answer)
	return nil
}

// Stop implements tower.Runtime.
func (r *Runtime) Stop(_ context.Context, id string) error {
	if s, ok := r.sessions[id]; ok {
		s.report, s.final = tower.PilotReport{State: tower.PilotLost}, true
	}
	r.Stopped = append(r.Stopped, id)
	return nil
}

// Kill is a fault: the session dies now, wherever its script is.
func (r *Runtime) Kill(id string) error {
	s, ok := r.sessions[id]
	if !ok {
		return fmt.Errorf("sim: no session %q", id)
	}
	s.report, s.final = tower.PilotReport{State: tower.PilotLost}, true
	return nil
}
