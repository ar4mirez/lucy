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

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
)

// Actors the Tower appends events as.
const (
	ActorDaemon = "daemon"
	ActorOwner  = "owner"
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
	StateTakeoff    State = "takeoff"    // merged
	StateGrounded   State = "grounded"   // ended without a merge
)

// Final reports whether no event can follow in this state.
func (s State) Final() bool { return s == StateTakeoff || s == StateGrounded }

// Defaults for Config.
const (
	DefaultPollInterval = time.Minute
	DefaultPilotTimeout = 30 * time.Minute
	DefaultPatience     = 24 * time.Hour
	DefaultMaxRestarts  = 1
	maxTextBytes        = 1024 // a question or a reason kept in an event
)

// Config is what a Tower runs with. Store, Runtime, Forge and Clock are required; the store's
// registry must hold Payloads() and policy.DecisionRecord.
type Config struct {
	Store   *eventstore.Store
	Runtime Runtime
	Forge   Forge
	Clock   Clock

	PollInterval time.Duration // how often a driver should Step while a flight is active
	PilotTimeout time.Duration // a Pilot with no activity for this long is stopped and the flight grounded
	Patience     time.Duration // a pull request with no change toward a clearance for this long grounds the flight
	MaxRestarts  int           // lost Pilot sessions the Tower replaces before grounding; 0 means DefaultMaxRestarts, -1 none
}

// Spec is what a flight is dispatched with.
type Spec struct {
	ID         string             // unique among the Tower's flights
	Org        string             // the airspace
	Project    string             // the runway
	Goal       string             // what the flight is for
	Base       string             // the branch to land on; "main" when empty
	Posture    policy.Posture     // the project's merge posture; owner when empty
	Exceptions []policy.Exception // fixed-list categories the change touches
}

// Status is a flight as the Tower sees it.
type Status struct {
	ID         string
	State      State
	Session    string
	Generation int
	Question   string // when asking
	PR         int
	Observed   PullRequest // the pull request's last recorded facts
	Reason     string      // when grounded
	Since      time.Time   // when the flight entered its state
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
	progress   time.Time // the Pilot's last progress: started or answered
	since      time.Time
	pr         int
	observed   PullRequest
	observedAt time.Time // when the facts last changed
	approvals  []policy.Approval
	cleared    string // the inputs the last clearance was decided on
	decision   Cleared
	reason     string
}

// Tower flies flights.
type Tower struct {
	cfg     Config
	flights map[string]*flight
	order   []string // flight ids, sorted, so every Step visits them in the same order
}

// New returns a Tower on cfg's store, with every flight rebuilt from the log.
func New(ctx context.Context, cfg Config) (*Tower, error) {
	if cfg.Store == nil || cfg.Runtime == nil || cfg.Forge == nil || cfg.Clock == nil {
		return nil, errors.New("tower: Config needs a Store, a Runtime, a Forge and a Clock")
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
			Exceptions: d.Exceptions},
		stream: streamPrefix + d.Org + "/" + id,
	}
	t.flights[id] = f
	t.order = append(t.order, id)
	slices.Sort(t.order)
	return f
}

// Dispatch starts a flight. Its Pilot starts on the next Step.
func (t *Tower) Dispatch(ctx context.Context, s Spec) error {
	if s.ID == "" || s.Org == "" || s.Project == "" || s.Goal == "" {
		return errors.New("tower: a flight needs an id, an org, a project and a goal")
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
	if s.Posture == "" {
		s.Posture = policy.PostureOwner
	}
	d := Dispatched{Org: s.Org, Project: s.Project, Goal: s.Goal, Base: s.Base, Posture: s.Posture, Exceptions: s.Exceptions}
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
	return f.status(), true
}

// Flights returns every flight's status, sorted by id.
func (t *Tower) Flights() []Status {
	out := make([]Status, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, t.flights[id].status())
	}
	return out
}

func (f *flight) status() Status {
	return Status{ID: f.spec.ID, State: f.state, Session: f.session, Generation: f.generation, Question: f.question,
		PR: f.pr, Observed: f.observed, Reason: f.reason, Since: f.since}
}

// NextWake returns when a driver should Step again: one poll interval from now while any flight
// is active, and false when every flight is final.
func (t *Tower) NextWake() (time.Time, bool) {
	for _, id := range t.order {
		if !t.flights[id].state.Final() {
			return t.cfg.Clock.Now().Add(t.cfg.PollInterval), true
		}
	}
	return time.Time{}, false
}

// Step advances every active flight once, in id order. It returns the first error that stopped a
// flight's step; the other flights still step.
func (t *Tower) Step(ctx context.Context) error {
	var errs []error
	for _, id := range t.order {
		f := t.flights[id]
		if f.state.Final() {
			continue
		}
		if err := t.step(ctx, f); err != nil {
			errs = append(errs, fmt.Errorf("tower: flight %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func (t *Tower) step(ctx context.Context, f *flight) error {
	switch f.state {
	case StateDispatched:
		return t.startPilot(ctx, f)
	case StateFlying, StateAsking:
		return t.watchPilot(ctx, f)
	case StateReview, StateHolding:
		return t.watchPR(ctx, f)
	}
	return nil
}

// startPilot starts the flight's next Pilot generation. A session started but not recorded,
// because the daemon stopped in between, is orphaned and a new one starts after the restart;
// TODO(LUCY-RUN-001): reattach before spawn, through the runner protocol's leases (m1).
func (t *Tower) startPilot(ctx context.Context, f *flight) error {
	gen := f.generation + 1
	session, err := t.cfg.Runtime.Start(ctx, PilotRequest{Flight: f.spec.ID, Org: f.spec.Org, Project: f.spec.Project,
		Goal: f.spec.Goal, Base: f.spec.Base, Generation: gen})
	if err != nil {
		return t.record(ctx, f, ActorDaemon, Grounded{Reason: GroundStartFailed, Detail: capText(err.Error())})
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
	case PilotLost:
		if f.lost < t.cfg.MaxRestarts {
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
		// failed or the daemon stopped) is the takeoff; any other merge happened outside lucy.
		if d := f.decision; d.Outcome == policy.Cleared && d.HeadSHA == pr.HeadSHA {
			return t.record(ctx, f, ActorDaemon, Takeoff{PR: f.pr, HeadSHA: pr.HeadSHA, MergeSHA: pr.MergeSHA, DecisionSeq: d.DecisionSeq})
		}
		return t.record(ctx, f, ActorDaemon, Grounded{Reason: GroundClosed, Detail: "the pull request was merged outside lucy"})
	}
	if pr.Checks != policy.ChecksPending && pr.Checks != "" {
		if f.clearanceKey() != f.cleared {
			return t.clearMerge(ctx, f)
		}
	}
	if f.state == StateReview && t.cfg.Clock.Now().Sub(f.observedAt) >= t.cfg.Patience {
		return t.record(ctx, f, ActorDaemon, Grounded{Reason: GroundStalled,
			Detail: "the pull request made no progress toward a clearance for " + t.cfg.Patience.String()})
	}
	return nil
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
	facts := policy.Facts{
		Posture: f.spec.Posture, Checks: f.observed.Checks, RequiredUnreported: f.observed.RequiredUnreported,
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
	mergeSHA, err := t.cfg.Forge.Merge(ctx, f.repo(), f.pr, facts.HeadSHA)
	if err != nil {
		reason := "error"
		switch {
		case errors.Is(err, ErrConflict):
			reason = "conflict"
		case errors.Is(err, ErrHeadMoved):
			reason = "head_moved"
		}
		return t.record(ctx, f, ActorDaemon, MergeFailed{PR: f.pr, HeadSHA: facts.HeadSHA, Reason: reason})
	}
	return t.record(ctx, f, ActorDaemon, Takeoff{PR: f.pr, HeadSHA: facts.HeadSHA, MergeSHA: mergeSHA, DecisionSeq: c.DecisionSeq})
}

// Answer passes the owner's answer to the Pilot's question.
func (t *Tower) Answer(ctx context.Context, id, answer string) error {
	f, ok := t.flights[id]
	if !ok {
		return fmt.Errorf("tower: no flight %q", id)
	}
	if f.state != StateAsking {
		return fmt.Errorf("tower: flight %s is %s, not asking", id, f.state)
	}
	if err := t.cfg.Runtime.Answer(ctx, f.session, answer); err != nil {
		return err
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
		f.apply(e, payloads[i])
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
	switch p := p.(type) {
	case Dispatched:
		enter(StateDispatched)
	case PilotStarted:
		f.session, f.generation, f.progress, f.question = p.Session, p.Generation, e.Time, ""
		enter(StateFlying)
	case PilotAsked:
		f.question = p.Question
		enter(StateAsking)
	case PilotAnswered:
		f.question, f.progress = "", e.Time
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
		enter(StateReview) // a holding clearance was for the old facts
	case Approved:
		f.approvals = append(f.approvals, p.Approval)
	case Cleared:
		f.decision = p
		f.cleared = f.clearanceKey()
		if p.Outcome == policy.Holding {
			enter(StateHolding)
		} else {
			enter(StateReview)
		}
	case MergeFailed:
		enter(StateReview)
	case Takeoff:
		enter(StateTakeoff)
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
