// Package tower is the Tower's flight state machine: it flies one flight (a task) from dispatch,
// through a Pilot session and its pull request, to a Clearance from the policy engine and the
// Takeoff (the merge), or grounds it.
//
// The machine is deterministic. It reads time only from its Clock, touches the outside world
// only through the Runtime and Forge seams (seams.go), and never sleeps or starts a goroutine: a
// driver calls Step, and each Step advances every flight that has news, in a fixed order. Every
// transition is an event appended to the event store before the Tower acts on it, and the
// Tower's state is a projection of those events, so a Tower opened on the same store after a
// restart carries on where the old one stopped (LUCY-EVT-002).
//
// Every merge passes the policy engine: the Tower builds the request and gathers the facts from
// the forge and its own records, appends the policy.decision event with its own clearance event
// in one transaction, and merges only on a recorded cleared decision, naming the head commit the
// decision was about (LUCY-POL-001, LUCY-POL-002).
//
// A Tower is not safe for concurrent use.
package tower

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/converge"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// Actors the Tower appends events as.
const (
	ActorDaemon = "daemon"
	ActorOwner  = "owner"
	ActorForge  = "forge" // a merge the forge reports that lucy did not perform
)

// State is where a flight is.
type State string

// Flight states. Takeoff and grounded are final.
const (
	StateDispatched State = "dispatched" // waiting for a Pilot session
	StateFlying     State = "flying"     // a Pilot is working
	StateAsking     State = "asking"     // the Pilot asked a question; waiting on the owner
	StateReview     State = "review"     // the pull request is open; waiting for checks and a clearance
	StateHolding    State = "holding"    // the clearance is holding: waiting on the owner's approval
	StateTakeoff    State = "takeoff"    // merged by lucy
	StateOutside    State = "outside"    // merged, but not by lucy, or by no one the forge can name
	StateGrounded   State = "grounded"   // ended without a merge
)

// Final reports whether no event can follow in this state.
func (s State) Final() bool { return s == StateTakeoff || s == StateOutside || s == StateGrounded }

// Defaults for Config.
const (
	DefaultPollInterval = time.Minute
	DefaultPilotTimeout = 30 * time.Minute
	DefaultPatience     = 24 * time.Hour
	DefaultMaxRestarts  = 1
	maxTextBytes        = 1024 // a question or a reason kept in an event
	MaxMergeErrors      = 3    // consecutive errored merges of one clearance before the flight holds for the owner
)

// Config is what a Tower runs with. Store, Runtime, Forge, Clock and ForgeIdentity are required;
// the store's registry must hold Payloads() and policy.DecisionRecord.
type Config struct {
	Store         *eventstore.Store
	Runtime       Runtime
	Forge         Forge
	Clock         Clock
	ForgeIdentity string // the forge account lucy merges as: a merge by any other is not lucy's

	PollInterval time.Duration // how often a driver should Step while a flight is active
	PilotTimeout time.Duration // a Pilot with no activity for this long is stopped and the flight grounded
	Patience     time.Duration // a pull request with no change toward a clearance for this long grounds the flight
	MaxRestarts  int           // lost Pilot sessions the Tower replaces before grounding; 0 means DefaultMaxRestarts, -1 none

	// Backoff times a flight's retries after its step fails (converge's Backoff rule); zero values
	// take converge's defaults.
	Backoff converge.Options
	// Refusal, when set, reports a step error that no backoff would change (the daemon's stub
	// forge): the step does not count as failed, and Step does not return it.
	Refusal func(error) bool
}

// Spec is what a flight is dispatched with.
type Spec struct {
	ID         string             // unique among the Tower's flights
	Org        string             // the airspace
	Project    string             // the runway
	Goal       string             // what the flight is for
	Base       string             // the branch to land on; "main" when empty
	Delivery   string             // where the result goes: DeliveryPullRequest
	Posture    policy.Posture     // the project's merge posture: how much the flight may do on its own
	Acceptance string             // the acceptance test: what must hold for the flight to be done
	Exceptions []policy.Exception // fixed-list categories the change touches
	Pipeline   *Pipeline          // how the flight is flown; nil flies it on the Tower's own defaults
}

// strictness orders the postures: a stricter one needs more human word before a merge.
var strictness = map[policy.Posture]int{policy.PostureGreen: 0, policy.PostureOwner: 1, policy.PostureClient: 2}

// Stricter reports whether posture a needs more human word before a merge than b: green, then
// owner, then client (the owner's word and the client's confirmation). An unknown a is never
// stricter, and any known a is stricter than an unknown b.
func Stricter(a, b policy.Posture) bool {
	sa, oka := strictness[a]
	sb, okb := strictness[b]
	return oka && (!okb || sa > sb)
}

// Tighten moves a flight in flight to a stricter posture, recorded as PostureTightened with why;
// its next merge is decided again under it. It does nothing, and reports false, for a final
// flight or for a posture that is not stricter than the flight's: a flight is never loosened.
func (t *Tower) Tighten(ctx context.Context, id string, to policy.Posture, why string) (bool, error) {
	f, ok := t.flights[id]
	if !ok {
		return false, fmt.Errorf("tower: no flight %q", id)
	}
	if f.state.Final() || !Stricter(to, f.spec.Posture) {
		return false, nil
	}
	return true, t.record(ctx, f, ActorDaemon, PostureTightened{From: f.spec.Posture, To: to, Reason: capText(why)})
}

// OnTimeoutAbort is the one approval timeout action the Tower flies: it grounds the flight. A
// timeout never approves a merge.
const OnTimeoutAbort = "abort"

// DeliveryPullRequest is the delivery path of a ship flight: a pull request against the runway's
// base branch. It is the only one the Tower flies so far; a research flight's report is later.
const DeliveryPullRequest = "pull-request"

// ErrNotReady refuses to dispatch a flight that is not ready (LUCY-SPEC-012).
var ErrNotReady = errors.New("tower: the flight is not ready to dispatch")

// Readiness returns what s lacks before it may be dispatched, in a fixed order, empty when it is
// ready: a goal, a delivery path (its runway and a delivery the Tower flies), an autonomy posture
// stated rather than assumed, and an acceptance test (LUCY-SPEC-012).
func Readiness(s Spec) []string {
	var missing []string
	if strings.TrimSpace(s.Goal) == "" {
		missing = append(missing, "a goal")
	}
	switch {
	case s.Project == "":
		missing = append(missing, "a delivery path (its runway)")
	case s.Delivery == "":
		missing = append(missing, "a delivery path (pull-request)")
	case s.Delivery != DeliveryPullRequest:
		missing = append(missing, fmt.Sprintf("a delivery path the Tower flies (pull-request, not %q)", s.Delivery))
	}
	switch s.Posture {
	case policy.PostureOwner, policy.PostureGreen, policy.PostureClient:
	case "":
		missing = append(missing, "an autonomy posture (owner, green or client)")
	default:
		missing = append(missing, fmt.Sprintf("a known autonomy posture (owner, green or client, not %q)", s.Posture))
	}
	if strings.TrimSpace(s.Acceptance) == "" {
		missing = append(missing, "an acceptance test")
	}
	return missing
}

// Status is a flight as the Tower sees it.
type Status struct {
	ID         string
	Org        string
	Project    string
	Posture    policy.Posture // the posture it was dispatched under, as recorded in its task.dispatched
	State      State
	Session    string
	Generation int
	Question   string // when asking
	PR         int
	Observed   PullRequest // the pull request's last recorded facts
	Reason     string      // when grounded; when holding after its merges failed, the last merge error; when outside, the attribution
	Since      time.Time   // when the flight entered its state
	StepError  string      // why its last step failed; empty after a clean step
	Failures   int         // its consecutive failed steps
	RetryAt    time.Time   // when it is stepped again after a failed step; zero after a clean one
}

type flight struct {
	spec    Spec
	stream  string
	version int64

	state      State
	session    string
	generation int
	lost       int // sessions lost so far
	question   string
	need       string    // set while the Tower, not the Pilot, asks: what the harness needs the owner for
	progress   time.Time // the Pilot's last progress: started or answered
	since      time.Time
	pr         int
	observed   PullRequest
	observedAt time.Time // when the facts last changed
	approvals  []policy.Approval
	cleared    string // the inputs the last clearance was decided on
	decision   Cleared
	unmerged   bool      // the last event is a cleared decision whose merge did not reach the forge, or failed with an error
	mergeErrs  int       // consecutive errored merges since the last clearance or observation
	mergeAt    time.Time // when the last errored merge was recorded
	reason     string

	// The flight's step backoff, kept in memory only: a restarted Tower retries every flight at once.
	stepErr  error
	failures int
	stepAt   time.Time
	// Failed Pilot starts since the last start, from the log: the start backoff, kept across restarts.
	startFails int
	startErr   string
	startAt    time.Time // when the last start failed
}

// Tower flies flights.
type Tower struct {
	cfg     Config
	flights map[string]*flight
	order   []string // flight ids, sorted, so every Step visits them in the same order
}

// New returns a Tower on cfg's store, with every flight rebuilt from the log.
func New(ctx context.Context, cfg Config) (*Tower, error) {
	if cfg.Store == nil || cfg.Runtime == nil || cfg.Forge == nil || cfg.Clock == nil || cfg.ForgeIdentity == "" {
		return nil, errors.New("tower: Config needs a Store, a Runtime, a Forge, a Clock and a ForgeIdentity")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.PilotTimeout <= 0 {
		cfg.PilotTimeout = DefaultPilotTimeout
	}
	if cfg.Patience <= 0 {
		cfg.Patience = DefaultPatience
	}
	switch {
	case cfg.MaxRestarts == 0:
		cfg.MaxRestarts = DefaultMaxRestarts
	case cfg.MaxRestarts < 0:
		cfg.MaxRestarts = 0
	}
	t := &Tower{cfg: cfg, flights: map[string]*flight{}}
	err := cfg.Store.Replay(ctx, 0, func(e eventstore.Event) error {
		if !strings.HasPrefix(e.Stream, streamPrefix) {
			return nil
		}
		p, err := cfg.Store.Registry().Decode(e)
		if err != nil {
			return err
		}
		f := t.flights[streamID(e.Stream)]
		if f == nil {
			d, ok := p.(Dispatched)
			if !ok {
				return fmt.Errorf("tower: stream %s does not start with %s", e.Stream, Dispatched{}.EventType())
			}
			f = t.add(streamID(e.Stream), d)
		}
		f.version = e.StreamVersion
		f.apply(e, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("tower: rebuild: %w", err)
	}
	return t, nil
}

const streamPrefix = "task/"

// streamID is the flight id of a stream "task/<org>/<id>".
func streamID(stream string) string {
	rest := strings.TrimPrefix(stream, streamPrefix)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[i+1:]
	}
	return rest
}

func (t *Tower) add(id string, d Dispatched) *flight {
	f := &flight{
		spec: Spec{ID: id, Org: d.Org, Project: d.Project, Goal: d.Goal, Base: d.Base, Posture: d.Posture,
			Exceptions: d.Exceptions, Delivery: d.Delivery, Acceptance: d.Acceptance, Pipeline: d.Pipeline},
		stream: streamPrefix + d.Org + "/" + id,
	}
	t.flights[id] = f
	t.order = append(t.order, id)
	slices.Sort(t.order)
	return f
}

// Dispatch starts a flight. Its Pilot starts on the next Step.
func (t *Tower) Dispatch(ctx context.Context, s Spec) error {
	if s.ID == "" || s.Org == "" {
		return errors.New("tower: a flight needs an id and an org")
	}
	if missing := Readiness(s); len(missing) > 0 {
		return fmt.Errorf("%w: it needs %s", ErrNotReady, strings.Join(missing, ", "))
	}
	if strings.ContainsAny(s.ID+s.Org, "/#") {
		return errors.New("tower: a flight id or org may not contain '/' or '#'")
	}
	if _, ok := t.flights[s.ID]; ok {
		return fmt.Errorf("tower: flight %q exists", s.ID)
	}
	if s.Base == "" {
		s.Base = "main"
	}
	if p := s.Pipeline; p != nil {
		switch {
		case p.MaxRestarts < 0:
			return errors.New("tower: a Pipeline's restarts cannot be negative")
		case p.ApprovalTimeout < 0:
			return errors.New("tower: a Pipeline's approval timeout cannot be negative")
		case p.ApprovalTimeout > 0 && p.OnTimeout != OnTimeoutAbort:
			return fmt.Errorf("tower: a Pipeline's approval timeout action %q is not abort; a timeout never approves a merge", p.OnTimeout)
		case p.ApprovalTimeout == 0 && p.OnTimeout != "":
			return errors.New("tower: a Pipeline names an approval timeout action without a timeout")
		}
	}
	d := Dispatched{Org: s.Org, Project: s.Project, Goal: s.Goal, Base: s.Base, Posture: s.Posture, Exceptions: s.Exceptions,
		Delivery: s.Delivery, Acceptance: capText(s.Acceptance), Pipeline: s.Pipeline}
	evs, err := t.cfg.Store.Append(ctx, streamPrefix+s.Org+"/"+s.ID, eventstore.NoStream, ActorDaemon, d)
	if err != nil {
		return err
	}
	f := t.add(s.ID, d)
	f.version = evs[0].StreamVersion
	f.apply(evs[0], d)
	return nil
}

// Flight returns a flight's status.
func (t *Tower) Flight(id string) (Status, bool) {
	f, ok := t.flights[id]
	if !ok {
		return Status{}, false
	}
	return f.status(t.cfg.Backoff), true
}

// Flights returns every flight's status, sorted by id.
func (t *Tower) Flights() []Status {
	out := make([]Status, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, t.flights[id].status(t.cfg.Backoff))
	}
	return out
}

func (f *flight) status(b converge.Options) Status {
	s := Status{ID: f.spec.ID, Org: f.spec.Org, Project: f.spec.Project, Posture: f.spec.Posture, State: f.state, Session: f.session, Generation: f.generation, Question: f.question,
		PR: f.pr, Observed: f.observed, Reason: f.reason, Since: f.since, Failures: f.failures, RetryAt: f.stepAt}
	if f.stepErr != nil {
		s.StepError = f.stepErr.Error()
	}
	if f.state == StateDispatched && f.startFails > 0 && f.stepErr == nil {
		s.StepError, s.Failures, s.RetryAt = "the Pilot did not start: "+f.startErr, f.startFails, f.startRetryAt(b)
	}
	return s
}

// startRetryAt is when a flight whose Pilot start failed is started again: its backoff, doubling
// with each consecutive failure, after the last one, from recorded times. Zero when none failed.
func (f *flight) startRetryAt(b converge.Options) time.Time {
	if f.startFails == 0 {
		return time.Time{}
	}
	return f.startAt.Add(b.Backoff(f.startFails))
}

// NextWake returns when a driver should Step again: one poll interval from now while any flight
// is active, or sooner when a flight's backoff ends first, and false when every flight is final.
func (t *Tower) NextWake() (time.Time, bool) {
	now := t.cfg.Clock.Now()
	next, active := now.Add(t.cfg.PollInterval), false
	for _, id := range t.order {
		f := t.flights[id]
		if f.state.Final() {
			continue
		}
		active = true
		if f.stepAt.After(now) && f.stepAt.Before(next) {
			next = f.stepAt
		}
		if at := f.startRetryAt(t.cfg.Backoff); f.state == StateDispatched && at.After(now) && at.Before(next) {
			next = at
		}
	}
	if !active {
		return time.Time{}, false
	}
	return next, true
}

// FlightError is a flight's failed step. The flight is not stepped again until RetryAt, whatever
// wakes the Tower: its backoff doubles with each consecutive failure, and a clean step resets it.
type FlightError struct {
	Flight   string
	Err      error
	Failures int           // consecutive failed steps, this one included
	Backoff  time.Duration // how long until it is stepped again
	RetryAt  time.Time
}

func (e *FlightError) Error() string { return fmt.Sprintf("tower: flight %s: %v", e.Flight, e.Err) }

func (e *FlightError) Unwrap() error { return e.Err }

// Step advances every active flight once, in id order, but a flight still in its backoff after a
// failed step. It returns a *FlightError for each flight whose step failed (joined); the other
// flights still step, on their own schedule.
func (t *Tower) Step(ctx context.Context) error {
	var errs []error
	for _, id := range t.order {
		f := t.flights[id]
		if f.state.Final() || t.cfg.Clock.Now().Before(f.stepAt) {
			continue
		}
		err := t.step(ctx, f)
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil || (t.cfg.Refusal != nil && t.cfg.Refusal(err)) {
			f.stepErr, f.failures, f.stepAt = nil, 0, time.Time{}
			continue
		}
		f.failures++
		backoff := t.cfg.Backoff.Backoff(f.failures)
		f.stepErr, f.stepAt = err, t.cfg.Clock.Now().Add(backoff)
		errs = append(errs, &FlightError{Flight: id, Err: err, Failures: f.failures, Backoff: backoff, RetryAt: f.stepAt})
	}
	return errors.Join(errs...)
}

func (t *Tower) step(ctx context.Context, f *flight) error {
	switch f.state {
	case StateDispatched:
		return t.startPilot(ctx, f)
	case StateFlying, StateAsking:
		if f.need != "" {
			return nil // the Tower's own question: no session to watch until the owner answers
		}
		return t.watchPilot(ctx, f)
	case StateReview, StateHolding:
		return t.watchPR(ctx, f)
	}
	return nil
}

// ErrStartRefused is a Runtime's refusal to start a Pilot that no retry would change: a request it
// will never accept. The Tower grounds the flight on it. Any other start error is taken as
// transient (an outage of the runner protocol or the store): the flight stays dispatched and its
// start is retried on its own backoff (LUCY-SPEC-008).
var ErrStartRefused = errors.New("tower: the runtime refuses to start this Pilot")

// startPilot starts the flight's next Pilot generation. A start that fails is recorded as
// task.pilot_start_failed and retried on the start backoff those recorded times give
// (startRetryAt), and only a refusal (ErrStartRefused) grounds the flight. A generation started
// but not recorded, because the daemon stopped in between, is started again after the restart: the
// runner protocol (runner.Runtime) returns the run it already queued, and fences any older live run
// of the flight before it queues a new one, so a flight never has two live sessions (LUCY-RUN-004).
func (t *Tower) startPilot(ctx context.Context, f *flight) error {
	if t.cfg.Clock.Now().Before(f.startRetryAt(t.cfg.Backoff)) {
		return nil // backing off after a failed start
	}
	gen := f.generation + 1
	session, err := t.cfg.Runtime.Start(ctx, PilotRequest{Flight: f.spec.ID, Org: f.spec.Org, Project: f.spec.Project,
		Goal: f.spec.Goal, Base: f.spec.Base, Generation: gen})
	if errors.Is(err, ErrStartRefused) {
		return t.record(ctx, f, ActorDaemon, Grounded{Reason: GroundStartFailed, Detail: capText(err.Error())})
	}
	if err != nil {
		return t.record(ctx, f, ActorDaemon, PilotStartFailed{Generation: gen, Error: capText(err.Error())})
	}
	return t.record(ctx, f, ActorDaemon, PilotStarted{Session: session, Generation: gen})
}

func (t *Tower) watchPilot(ctx context.Context, f *flight) error {
	rep, err := t.cfg.Runtime.Poll(ctx, f.session)
	if err != nil {
		return err // no news; the timeout still applies on the next step
	}
	exited := func(exit string, pr int, reason string) PilotExited {
		return PilotExited{Session: f.session, Generation: f.generation, Exit: exit, PR: pr, Reason: capText(reason)}
	}
	switch rep.State {
	case PilotRunning:
		last := f.progress
		if rep.Activity.After(last) {
			last = rep.Activity
		}
		if f.state == StateFlying && t.cfg.Clock.Now().Sub(last) >= t.cfg.PilotTimeout {
			if err := t.cfg.Runtime.Stop(ctx, f.session); err != nil {
				return err
			}
			return t.record(ctx, f, ActorDaemon, exited(ExitStopped, 0, "no progress within the Pilot timeout"),
				Grounded{Reason: GroundTimeout, Detail: "the Pilot made no progress for " + t.cfg.PilotTimeout.String()})
		}
	case PilotAsking:
		if q := capText(rep.Question); f.state != StateAsking || f.question != q {
			return t.record(ctx, f, ActorDaemon, PilotAsked{Session: f.session, Question: q})
		}
	case PilotDone:
		if rep.PR <= 0 {
			return t.record(ctx, f, ActorDaemon, exited(ExitFailed, 0, "finished without a pull request"),
				Grounded{Reason: GroundPilotFailed, Detail: "the Pilot finished without a pull request"})
		}
		return t.record(ctx, f, ActorDaemon, exited(ExitDone, rep.PR, ""))
	case PilotFailed:
		return t.record(ctx, f, ActorDaemon, exited(ExitFailed, 0, rep.Reason), Grounded{Reason: GroundPilotFailed})
	case PilotNeedsOwner:
		return t.record(ctx, f, ActorDaemon, exited(ExitNeedsOwner, 0, rep.Need),
			PilotAsked{Session: f.session, Question: ownerNeedQuestion(f.spec.Org, rep.Runner, rep.Need), Need: rep.Need})
	case PilotLost:
		if f.lost < t.maxRestarts(f) {
			return t.record(ctx, f, ActorDaemon, exited(ExitLost, 0, ""))
		}
		return t.record(ctx, f, ActorDaemon, exited(ExitLost, 0, ""),
			Grounded{Reason: GroundPilotLost, Detail: fmt.Sprintf("%d Pilot sessions lost", f.lost+1)})
	default:
		return fmt.Errorf("the runtime reported an unknown Pilot state %q", rep.State)
	}
	return nil
}

func (t *Tower) watchPR(ctx context.Context, f *flight) error {
	pr, err := t.cfg.Forge.PullRequest(ctx, f.repo(), f.pr)
	if err != nil {
		return err
	}
	if pr != f.observed {
		if err := t.record(ctx, f, ActorDaemon, PRObserved{pr}); err != nil {
			return err
		}
	}
	if pr.Merged {
		// A merge the Tower was cleared for but did not record (the forge merged, then the call
		// failed or the daemon stopped) is the takeoff only when the forge says lucy's account
		// merged the cleared head; any other merge happened outside lucy and cites no clearance.
		if d := f.decision; pr.MergedBy == t.cfg.ForgeIdentity && d.Outcome == policy.Cleared && d.HeadSHA == pr.HeadSHA {
			return t.record(ctx, f, ActorDaemon, Takeoff{PR: f.pr, HeadSHA: pr.HeadSHA, MergeSHA: pr.MergeSHA, DecisionSeq: d.DecisionSeq})
		}
		attribution := AttributionExternal
		if pr.MergedBy == "" {
			attribution = AttributionUnknown
		}
		return t.record(ctx, f, ActorForge, MergedOutside{PR: f.pr, HeadSHA: pr.HeadSHA, MergeSHA: pr.MergeSHA,
			MergedBy: pr.MergedBy, Attribution: attribution})
	}
	if pr.Checks != policy.ChecksPending && pr.Checks != "" {
		if f.clearanceKey() != f.cleared {
			return t.clearMerge(ctx, f)
		}
	}
	// A merge held for the owner past its Pipeline's approval timeout is grounded; a timeout never
	// approves it.
	if p := f.spec.Pipeline; f.state == StateHolding && f.decision.Outcome == policy.Holding && p != nil && p.ApprovalTimeout > 0 && p.OnTimeout == OnTimeoutAbort &&
		t.cfg.Clock.Now().Sub(f.since) >= p.ApprovalTimeout {
		return t.record(ctx, f, ActorDaemon, Grounded{Reason: GroundApprovalTimeout,
			Detail: "the merge held for the owner's approval for " + p.ApprovalTimeout.String() + "; a timeout never approves a merge"})
	}
	if f.state == StateReview && t.cfg.Clock.Now().Sub(f.observedAt) >= t.cfg.Patience {
		return t.record(ctx, f, ActorDaemon, Grounded{Reason: GroundStalled,
			Detail: "the pull request made no progress toward a clearance for " + t.cfg.Patience.String()})
	}
	// A clearance whose merge never reached the forge (the daemon stopped in between) or failed
	// with an error is merged again: the facts are unchanged, and Merge names the cleared head.
	// Each errored merge doubles the wait before the next; after MaxMergeErrors the flight holds.
	if f.unmerged && f.decision.HeadSHA == pr.HeadSHA && !t.cfg.Clock.Now().Before(f.retryAt(t.cfg.PollInterval)) {
		return t.merge(ctx, f, f.decision)
	}
	return nil
}

// maxRestarts is how many lost Pilot sessions the Tower replaces for f: its Pipeline's, else the
// Tower's own.
func (t *Tower) maxRestarts(f *flight) int {
	if p := f.spec.Pipeline; p != nil {
		return p.MaxRestarts
	}
	return t.cfg.MaxRestarts
}

// retryAt is the earliest time an errored merge may be tried again: poll, 2*poll, 4*poll, ...
// after the last one, from recorded event times so a restart keeps the same schedule.
func (f *flight) retryAt(poll time.Duration) time.Time {
	if f.mergeErrs == 0 {
		return time.Time{}
	}
	return f.mergeAt.Add(poll << (f.mergeErrs - 1))
}

// clearanceKey names the inputs a merge clearance is decided on: the observed facts and the
// approvals. The Tower decides again only when one of them changes.
func (f *flight) clearanceKey() string {
	return fmt.Sprintf("%+v/%d", f.observed, len(f.approvals))
}

func (f *flight) repo() string { return f.spec.Org + "/" + f.spec.Project }

// mergeRequest is the Tower's request to merge the flight's pull request, with the facts it
// gathered from the forge and its own records.
func (f *flight) mergeRequest() (policy.Request, policy.Facts) {
	req := policy.Request{
		Actor:  policy.Actor{ID: ActorDaemon, Role: policy.RoleDaemon},
		Kind:   policy.KindMerge,
		Target: policy.Target{Org: f.spec.Org, Project: f.spec.Project, Resource: fmt.Sprintf("pr/%d", f.pr)},
		Inputs: []policy.Input{{Source: policy.SourceTask, Ref: f.stream}},
	}
	posture := f.spec.Posture
	if p := f.spec.Pipeline; p != nil && p.ApprovalRequired && posture == policy.PostureGreen {
		posture = policy.PostureOwner // the Pipeline holds every merge for the owner; it never waives an approval
	}
	facts := policy.Facts{
		Posture: posture, Checks: f.observed.Checks, RequiredUnreported: f.observed.RequiredUnreported,
		Exceptions: f.spec.Exceptions, HeadSHA: f.observed.HeadSHA, BaseRef: f.observed.BaseRef,
		BaseSHA: f.observed.BaseSHA, Approvals: f.approvals,
	}
	return req, facts
}

func (t *Tower) clearMerge(ctx context.Context, f *flight) error {
	req, facts := f.mergeRequest()
	d := policy.Evaluate(req, facts)
	var c Cleared
	err := t.cfg.Store.Update(ctx, func(tx *eventstore.Tx) error {
		evs, err := tx.Append(ctx, policy.Stream, eventstore.AnyVersion, ActorDaemon, policy.NewRecord(req, facts, d))
		if err != nil {
			return err
		}
		c = Cleared{DecisionSeq: evs[0].Seq, Outcome: d.Outcome, Rule: d.Rule, HeadSHA: facts.HeadSHA,
			ActionSHA256: d.ActionSHA256, FactsSHA256: d.FactsSHA256}
		return t.appendTx(ctx, tx, f, ActorDaemon, c)
	})
	if err != nil {
		return err // unrecorded, so it authorizes nothing
	}
	if d.Outcome != policy.Cleared {
		return nil
	}
	return t.merge(ctx, f, c)
}

// merge merges the pull request at the head c cleared, and records the takeoff or the failure.
func (t *Tower) merge(ctx context.Context, f *flight, c Cleared) error {
	mergeSHA, err := t.cfg.Forge.Merge(ctx, f.repo(), f.pr, c.HeadSHA)
	if err != nil {
		reason := MergeError
		switch {
		case errors.Is(err, ErrConflict):
			reason = "conflict"
		case errors.Is(err, ErrHeadMoved):
			reason = "head_moved"
		}
		return t.record(ctx, f, ActorDaemon, MergeFailed{PR: f.pr, HeadSHA: c.HeadSHA, Reason: reason, Detail: capText(err.Error())})
	}
	return t.record(ctx, f, ActorDaemon, Takeoff{PR: f.pr, HeadSHA: c.HeadSHA, MergeSHA: mergeSHA, DecisionSeq: c.DecisionSeq})
}

// MergeError is a MergeFailed reason for an error that is neither a conflict nor a moved head.
const MergeError = "error"

// Answer passes the owner's answer to the Pilot's question.
func (t *Tower) Answer(ctx context.Context, id, answer string) error {
	f, ok := t.flights[id]
	if !ok {
		return fmt.Errorf("tower: no flight %q", id)
	}
	if f.state != StateAsking {
		return fmt.Errorf("tower: flight %s is %s, not asking", id, f.state)
	}
	if f.need == "" { // a question the Tower asks has no session to relay the answer to
		if err := t.cfg.Runtime.Answer(ctx, f.session, answer); err != nil {
			return err
		}
	}
	sum := sha256.Sum256([]byte(answer))
	return t.record(ctx, f, ActorOwner, PilotAnswered{Session: f.session, AnswerSHA256: hex.EncodeToString(sum[:])})
}

// Approve records the owner's approval of the flight's merge, bound to the pull request's facts
// as the Tower last recorded them: what the owner was shown. A later push or base change makes it
// stale, and the policy engine then denies the merge (LUCY-POL-002). The owner's decide command
// (m1) records approvals through the API in the same shape.
func (t *Tower) Approve(ctx context.Context, id string, by policy.ApprovalBy) error {
	f, ok := t.flights[id]
	if !ok {
		return fmt.Errorf("tower: no flight %q", id)
	}
	if f.state != StateHolding && f.state != StateReview || f.observed.HeadSHA == "" {
		return fmt.Errorf("tower: flight %s has no pull request to approve", id)
	}
	req, facts := f.mergeRequest()
	a := policy.Approval{
		Decision:     fmt.Sprintf("%s#%d", f.stream, f.version+1), // this event's own id
		By:           by,
		ActionSHA256: policy.ActionDigest(req),
		FactsSHA256:  policy.FactsDigest(req.Kind, facts),
	}
	return t.record(ctx, f, ActorOwner, Approved{a})
}

// Request decides a privileged action through the policy engine and records the decision, as
// every executor does before it acts (LUCY-POL-002). It is the seam later executors (delete,
// push, credential use: m1) call; the simulation suite drives it with requests no rule allows.
func (t *Tower) Request(ctx context.Context, actor string, req policy.Request, facts policy.Facts) (policy.Decision, int64, error) {
	d := policy.Evaluate(req, facts)
	evs, err := t.cfg.Store.Append(ctx, policy.Stream, eventstore.AnyVersion, actor, policy.NewRecord(req, facts, d))
	if err != nil {
		return policy.Decision{}, 0, err
	}
	return d, evs[0].Seq, nil
}

// record appends payloads to the flight's stream and applies them.
func (t *Tower) record(ctx context.Context, f *flight, actor string, payloads ...eventstore.Payload) error {
	return t.cfg.Store.Update(ctx, func(tx *eventstore.Tx) error {
		return t.appendTx(ctx, tx, f, actor, payloads...)
	})
}

func (t *Tower) appendTx(ctx context.Context, tx *eventstore.Tx, f *flight, actor string, payloads ...eventstore.Payload) error {
	evs, err := tx.Append(ctx, f.stream, f.version, actor, payloads...)
	if err != nil {
		return err
	}
	for i, e := range evs {
		f.version = e.StreamVersion
		f.apply(e, payloads[i]) // #nosec G602 -- Append returns one event per payload
	}
	return nil
}

// apply is the projection: the one place a flight's state changes, used both live and when
// rebuilding from the log.
func (f *flight) apply(e eventstore.Event, p eventstore.Payload) {
	enter := func(s State) {
		if f.state != s {
			f.state, f.since = s, e.Time
		}
	}
	f.unmerged = false
	switch p := p.(type) {
	case Dispatched:
		enter(StateDispatched)
	case PilotStartFailed:
		f.startFails, f.startErr, f.startAt = f.startFails+1, p.Error, e.Time
	case PilotStarted:
		f.startFails, f.startErr, f.startAt = 0, "", time.Time{}
		f.session, f.generation, f.progress, f.question = p.Session, p.Generation, e.Time, ""
		enter(StateFlying)
	case PilotAsked:
		f.question, f.need = p.Question, p.Need
		enter(StateAsking)
	case PilotAnswered:
		f.question, f.progress = "", e.Time
		if f.need != "" { // the owner did what the harness needed: start the next generation
			f.need = ""
			enter(StateDispatched)
			break
		}
		enter(StateFlying)
	case PilotExited:
		switch p.Exit {
		case ExitDone:
			f.pr, f.observedAt = p.PR, e.Time
			enter(StateReview)
		case ExitLost:
			f.lost++
			enter(StateDispatched)
		}
	case PRObserved:
		f.observed, f.observedAt = p.PullRequest, e.Time
		f.mergeErrs, f.reason = 0, ""
		enter(StateReview) // a holding clearance was for the old facts
	case Approved:
		f.approvals = append(f.approvals, p.Approval)
	case PostureTightened:
		f.spec.Posture = p.To
		f.cleared = "" // the last clearance was for the looser posture: decide again
		if f.state == StateHolding {
			enter(StateReview)
		}
	case Cleared:
		f.decision = p
		f.cleared = f.clearanceKey()
		f.unmerged = p.Outcome == policy.Cleared
		f.mergeErrs, f.reason = 0, ""
		if p.Outcome == policy.Holding {
			enter(StateHolding)
		} else {
			enter(StateReview)
		}
	case MergeFailed:
		if p.Reason != MergeError {
			f.mergeErrs = 0
			enter(StateReview)
			break
		}
		f.mergeErrs, f.mergeAt = f.mergeErrs+1, e.Time
		if f.mergeErrs >= MaxMergeErrors {
			f.reason = p.Detail
			enter(StateHolding)
			break
		}
		f.unmerged = true
		enter(StateReview)
	case Takeoff:
		enter(StateTakeoff)
	case MergedOutside:
		f.reason = p.Attribution
		enter(StateOutside)
	case Grounded:
		f.reason = p.Reason
		enter(StateGrounded)
	}
}

// capText bounds untrusted text kept in an event.
func capText(s string) string {
	if len(s) <= maxTextBytes {
		return s
	}
	cut := maxTextBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		cut--
	}
	return s[:cut]
}

// ownerNeedQuestion is the Tower's question to the owner when a Pilot could not start because its
// harness needs the owner first. It says what to do and where, in lucy's own words (the need is a
// known code, never agent text), and it never asks for a secret: lucy reads no login, and copies
// none from the owner's own home.
func ownerNeedQuestion(org, runner, need string) string {
	switch need {
	case "harness_login":
		where := "on the machine of the runner that took the run"
		if runner != "" {
			where = "on the machine of runner " + runner
		}
		return fmt.Sprintf("The Pilot could not start: org %s's home has no login for its harness. %s, log in once as that home: "+
			"for Claude Code, run claude there with HOME set to the runner's --root followed by /home, and complete its login. "+
			"Then answer here (for example: done) and lucy starts the Pilot again. "+
			"Do not paste a password, token or key into this answer: lucy never needs one here.", org, where)
	}
	return fmt.Sprintf("The Pilot could not start: its harness needs you first (%s). Answer here once it is done.", need)
}
