package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/ar4mirez/lucy/internal/api"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/autonomy"
	"github.com/ar4mirez/lucy/internal/converge"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/pipeline"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/spec"
	"github.com/ar4mirez/lucy/internal/tower"
	"github.com/ar4mirez/lucy/internal/worktree"
)

// The daemon hosts the Tower (m1 slice 7a): it flies the flights in its log and, only when started
// with --dispatch, dispatches the Ready Flights loaded from the config directory, starting their
// Pilots through the runner protocol; its Pilots' questions reach the owner through lucy questions
// and lucy answer. Until the validation slice connects a forge,
// its forge is a stub that reads no pull request and merges nothing, so no flight lands anything.

// errNoForge is the stub forge's answer to every call.
var errNoForge = errors.New("daemon: no forge is connected yet: lucy reads no pull request and merges nothing until the validation slice")

// stubIdentity is the forge identity the hosted Tower is configured with: no merge is ever lucy's.
const stubIdentity = "lucy-no-forge"

// stubForge is the hosted Tower's forge: it refuses every call.
type stubForge struct{}

func (stubForge) PullRequest(context.Context, string, int) (tower.PullRequest, error) {
	return tower.PullRequest{}, errNoForge
}

func (stubForge) Merge(context.Context, string, int, string) (string, error) { return "", errNoForge }

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

// hostedTower is the Tower the daemon runs. A Tower is not safe for concurrent use: every call
// holds mu, which the API's questions view holds too.
type hostedTower struct {
	tw        *tower.Tower
	mu        sync.Mutex
	log       *slog.Logger
	pipelines *pipeline.Catalog // each org's ship and research Pipelines (LUCY-SPEC-014)
}

// ErrTakeoverGate refuses to dispatch a Flight whose Runway names a real repository: until runs
// have an OS boundary of their own (issue #36) and the owner's go/no-go, a Pilot works only on a
// local fixture, whose remote is a file:// URL or absolute path under lucy's data directory or
// the system temp directory.
var ErrTakeoverGate = errors.New("daemon: the takeover gate is closed: no Flight is dispatched onto a real repository until runs have an OS boundary of their own (issue #36) and the owner's go/no-go")

// ConditionDispatched is the condition the daemon adds to every admitted Ready Flight it loads:
// whether the hosted Tower flies it, and if not, why.
const ConditionDispatched = "Dispatched"

// Reasons a Dispatched condition gives.
const (
	ReasonDispatched       = "Dispatched"
	ReasonInFlight         = "InFlight"
	ReasonDispatchDisabled = "DispatchDisabled"
	ReasonTakeoverGate     = "TakeoverGate"
	ReasonNameInUse        = "NameInUse"
	ReasonFinished         = "Finished"
	ReasonDispatchFailed   = "DispatchFailed"
	ReasonPipelineInvalid  = "PipelineInvalid" // the org's Pipeline of the Flight's shape is invalid; the built-in one never stands in
	// ReasonAutonomyInvalid: the Flight's posture cannot be set: its Runway names an AutonomyPolicy or
	// level lucy cannot fly, or the Flight's autonomy would loosen its Runway's (LUCY-POL-005).
	ReasonAutonomyInvalid = "AutonomyInvalid"
	// ReasonWorktreePoolInvalid: the org's WorktreePool cannot be accepted (LUCY-SPEC-017); lucy's
	// own pool defaults never stand in for it.
	ReasonWorktreePoolInvalid = "WorktreePoolInvalid"
)

// takeoverGate refuses remote unless it is a file:// URL or an absolute path that, with symlinks
// resolved, is under lucy's data directory or the system temp directory.
func takeoverGate(remote string) error {
	p := remote
	if u, err := url.Parse(remote); err == nil && u.Scheme == "file" && (u.Host == "" || u.Host == "localhost") {
		p = u.Path
	} else if !filepath.IsAbs(remote) {
		return fmt.Errorf("%w: the remote %q is not a local fixture", ErrTakeoverGate, remote)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil || !filepath.IsAbs(real) {
		return fmt.Errorf("%w: the remote %q is not a local fixture", ErrTakeoverGate, remote)
	}
	roots := []string{os.TempDir()}
	if data, err := eventstore.DataDir(); err == nil {
		roots = append(roots, data)
	}
	for _, root := range roots {
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(r, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("%w: the remote %q is not under lucy's data directory or the system temp directory", ErrTakeoverGate, remote)
}

// startTower opens the Tower on the daemon's store and broker, rebuilding every flight from the
// log. When dispatch is on, it dispatches every Ready Flight the set holds that it does not fly
// yet and that gate lets through; either way it adds each Ready Flight's Dispatched condition to
// set, which the daemon then records.
func startTower(ctx context.Context, store *eventstore.Store, broker *runner.Broker, set *spec.Set, pipelines *pipeline.Catalog, poll time.Duration, log *slog.Logger,
	dispatch bool, gate func(org, project string) error) (*hostedTower, error) {
	tw, err := tower.New(ctx, tower.Config{Store: store, Runtime: runner.Runtime{Broker: broker}, Forge: stubForge{},
		Clock: wallClock{}, ForgeIdentity: stubIdentity, PollInterval: poll,
		Refusal: func(err error) bool { return errors.Is(err, errNoForge) }})
	if err != nil {
		return nil, err
	}
	h := &hostedTower{tw: tw, log: log, pipelines: pipelines}
	h.dispatch(ctx, set, dispatch, gate)
	h.tighten(ctx, set)
	return h, nil
}

// dispatch decides, for every admitted Flight whose Ready condition is True, whether the Tower
// flies it, and records why as its Dispatched condition, whether or not dispatch is on. A Flight
// that is not Ready is never dispatched (LUCY-SPEC-012). Without dispatch on, none is. A Flight the Tower flies already, by
// its name, in its namespace and runway, was dispatched by an earlier start, unless it is
// finished; one whose name a flight of another org or runway holds is not dispatched.
func (h *hostedTower) dispatch(ctx context.Context, set *spec.Set, enabled bool, gate func(org, project string) error) {
	pools := worktree.PoolErrors(set.Results)
	bases := map[string]string{}
	for _, r := range set.Admitted() {
		if r.APIVersion == spec.LucyAPIVersion && r.Kind == "Runway" {
			b, _ := r.Spec["defaultBranch"].(string)
			bases[r.Metadata.Namespace+"/"+r.Metadata.Name] = b
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, res := range set.Results {
		r := res.Resource
		if r == nil || r.APIVersion != spec.LucyAPIVersion || r.Kind != "Flight" || !ready(res) {
			continue
		}
		mark := func(status spec.ConditionStatus, reason, message string) {
			set.Results[i].Conditions = append(set.Results[i].Conditions,
				spec.Condition{Type: ConditionDispatched, Status: status, Reason: reason, Message: message})
		}
		str := func(k string) string { s, _ := r.Spec[k].(string); return s }
		org, project := r.Metadata.Namespace, str("runway")
		if f, ok := h.tw.Flight(r.Metadata.Name); ok {
			if f.Org != org || f.Project != project {
				h.log.Warn("flight not dispatched: its name is in use", "flight", r.ID(), "in_use_by", f.Org+"/"+f.ID, "runway", f.Project, "state", f.State)
				mark(spec.False, ReasonNameInUse, fmt.Sprintf("the name is in use by flight %s of org %s, runway %s (%s); give the Flight a new name", f.ID, f.Org, f.Project, f.State))
				continue
			}
			if f.State.Final() {
				h.log.Warn("flight not dispatched: a finished flight has its name", "flight", r.ID(), "state", f.State)
				mark(spec.False, ReasonFinished, fmt.Sprintf("flight %s is %s; a new Flight needs a new name", f.ID, f.State))
				continue
			}
			if err := gate(org, project); err != nil {
				h.log.Warn("flight in the log refused at the takeover gate: its leases fail", "flight", r.ID(), "err", err)
				mark(spec.False, ReasonTakeoverGate, err.Error())
				continue
			}
			mark(spec.True, ReasonInFlight, "the Tower is flying it from its log")
			continue
		}
		if !enabled {
			mark(spec.False, ReasonDispatchDisabled, "the daemon dispatches no Flight unless it is started with --dispatch")
			continue
		}
		if err := gate(org, project); err != nil {
			h.log.Warn("flight not dispatched", "flight", r.ID(), "err", err)
			mark(spec.False, ReasonTakeoverGate, err.Error())
			continue
		}
		s := tower.Spec{ID: r.Metadata.Name, Org: org, Project: project, Goal: str("goal"),
			Base: bases[org+"/"+project], Delivery: str("delivery"),
			Posture: policy.Posture(str("autonomy")), Acceptance: str("acceptance")}
		shape := str("shape")
		if shape == "" {
			shape = pipeline.Ship
		}
		posture, err := flightPosture(set, *r)
		if err != nil {
			h.log.Warn("flight not dispatched: its posture cannot be set", "flight", r.ID(), "err", err)
			mark(spec.False, ReasonAutonomyInvalid, err.Error())
			continue
		}
		s.Posture = posture
		if err := pools[org]; err != nil {
			h.log.Warn("flight not dispatched: its org's WorktreePool is invalid", "flight", r.ID(), "err", err)
			mark(spec.False, ReasonWorktreePoolInvalid, err.Error())
			continue
		}
		p, err := h.pipelines.For(org, shape)
		if errors.Is(err, pipeline.ErrNoPipeline) {
			mark(spec.False, ReasonDispatchFailed, fmt.Sprintf("no %s Pipeline", shape))
			continue
		}
		if err != nil {
			h.log.Warn("flight not dispatched: its Pipeline is invalid", "flight", r.ID(), "err", err)
			mark(spec.False, ReasonPipelineInvalid, err.Error())
			continue
		}
		s.Pipeline = towerPipeline(p)
		if err := h.tw.Dispatch(ctx, s); err != nil {
			h.log.Warn("flight not dispatched", "flight", r.ID(), "err", err)
			mark(spec.False, ReasonDispatchFailed, err.Error())
			continue
		}
		h.log.Info("flight dispatched", "flight", r.ID())
		mark(spec.True, ReasonDispatched, "the Tower flies it")
	}
}

func ready(res spec.Result) bool {
	for _, c := range res.Conditions {
		if c.Type == spec.ConditionReady {
			return c.Status == spec.True
		}
	}
	return false
}

// run runs the Tower's converge loop until ctx ends (LUCY-SPEC-008): it steps every flight when the
// log changes (a Pilot's heartbeat, an answer, an approval), at least every poll interval while a
// flight is active (to watch what the log does not carry: the forge, a Pilot's silence). A flight
// whose step fails backs off on its own (tower.FlightError), so it never delays the others; only a
// failure of the loop itself backs off the whole Tower.
func (h *hostedTower) run(ctx context.Context, store *eventstore.Store, poll time.Duration) {
	converge.Run(ctx, towerLoop{h}, store, converge.Options{Resync: max(poll, converge.DefaultResync),
		MinInterval: min(poll, converge.DefaultMinInterval), Log: h.log})
}

// towerLoop is the Tower's converge loop body. A Tower step is level-triggered: each flight acts on
// the state it rebuilt from the log and what the runtime and forge report now, and records only
// what changed.
type towerLoop struct{ h *hostedTower }

func (towerLoop) Name() string { return "tower" }

func (l towerLoop) Reconcile(ctx context.Context) (time.Time, error) {
	err := l.h.step(ctx)
	l.h.mu.Lock()
	next, active := l.h.tw.NextWake()
	l.h.mu.Unlock()
	if !active {
		next = time.Time{}
	}
	return next, err
}

// step steps the Tower once. It logs each flight's failed step, which backs off that flight alone,
// and returns only errors that are not one flight's. The stub forge's refusal, which every flight
// in review meets on every step and which no backoff would change, is no failure (Config.Refusal).
func (h *hostedTower) step(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	err := h.tw.Step(ctx)
	if err == nil || ctx.Err() != nil {
		return nil
	}
	errs := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	}
	var kept []error
	for _, e := range errs {
		var fe *tower.FlightError
		if errors.As(e, &fe) {
			h.log.Warn("flight step failed; retrying it after a backoff", "flight", fe.Flight, "err", fe.Err, "failures", fe.Failures, "backoff", fe.Backoff)
			continue
		}
		kept = append(kept, e)
	}
	return errors.Join(kept...)
}

// towerPipeline is what the Tower takes from a Pipeline: how many lost Pilots it replaces, whether
// every merge needs the owner, and what an approval timeout does. None approves a merge.
func towerPipeline(p pipeline.Policy) *tower.Pipeline {
	out := &tower.Pipeline{Name: p.Namespace + "/" + p.Name, MaxRestarts: p.Pilot.MaxRetries,
		ApprovalRequired: p.Merge.Required, ApprovalTimeout: p.Merge.Timeout}
	switch p.Merge.OnTimeout {
	case pipeline.OnTimeoutAbort:
		out.OnTimeout = tower.OnTimeoutAbort
	}
	return out
}

// flights lists the admitted Flights of set, sorted by org and name, with their Ready and
// Dispatched conditions and the Tower's state of each flight it flies under the Flight's name and
// org (LUCY-SPEC-017).
func (h *hostedTower) flights(set *spec.Set) []api.FlightInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	cond := func(res spec.Result, typ string) api.ConditionInfo {
		c, ok := res.Condition(typ)
		if !ok {
			return api.ConditionInfo{}
		}
		return api.ConditionInfo{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message, LastTransitionTime: c.LastTransitionTime}
	}
	var out []api.FlightInfo
	for _, res := range set.Results {
		r := res.Resource
		if r == nil || r.APIVersion != spec.LucyAPIVersion || r.Kind != "Flight" {
			continue
		}
		str := func(k string) string { s, _ := r.Spec[k].(string); return s }
		f := api.FlightInfo{Org: r.Metadata.Namespace, Name: r.Metadata.Name, Goal: str("goal"), Shape: str("shape"),
			Runway: str("runway"), Ready: cond(res, spec.ConditionReady), Dispatched: cond(res, ConditionDispatched)}
		if f.Shape == "" {
			f.Shape = pipeline.Ship
		}
		if st, ok := h.tw.Flight(r.Metadata.Name); ok && st.Org == f.Org && st.Project == f.Runway {
			f.Autonomy, f.State, f.Reason, f.StepError, f.RetryAt = string(st.Posture), string(st.State), st.Reason, st.StepError, st.RetryAt.UTC()
		} else if p, err := flightPosture(set, *r); err == nil {
			f.Autonomy = string(p)
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b api.FlightInfo) int { return cmp.Or(cmp.Compare(a.Org, b.Org), cmp.Compare(a.Name, b.Name)) })
	return out
}

// flightPosture is the posture Flight f is flown under: its Runway's, from the AutonomyPolicy level
// the Runway names (or clientConfirms), tightened by the Flight's own autonomy, which may never
// loosen it; or the Flight's own when its Runway sets none (LUCY-POL-005).
func flightPosture(set *spec.Set, f spec.Resource) (policy.Posture, error) {
	runwayName, _ := f.Spec["runway"].(string)
	own, _ := f.Spec["autonomy"].(string)
	policies := map[string]spec.Resource{}
	var runway *spec.Resource
	for _, r := range set.Admitted() {
		switch {
		case r.APIVersion == spec.CoreAPIVersion && r.Kind == "AutonomyPolicy":
			policies[r.Metadata.Namespace+"/"+r.Metadata.Name] = r
		case r.APIVersion == spec.LucyAPIVersion && r.Kind == "Runway" && r.Metadata.Namespace == f.Metadata.Namespace && r.Metadata.Name == runwayName:
			runway = &r
		}
	}
	if runway == nil {
		return policy.Posture(own), nil // readiness refuses a Flight without its Runway
	}
	rp, sets, err := autonomy.RunwayPosture(*runway, policies)
	if err != nil {
		return "", err
	}
	return autonomy.Effective(rp, sets, policy.Posture(own))
}

// tighten moves every flight in flight to a stricter posture when the config now asks for one
// (LUCY-POL-005): its Runway's AutonomyPolicy level was demoted, or its Flight's own autonomy
// tightened. A flight whose posture can no longer be resolved from the config moves to owner: its
// Runway is gone or refused, its Runway's AutonomyPolicy can no longer be read, or neither its
// Runway nor its Flight states a posture any more. A flight is never loosened in flight: a promotion applies only to flights dispatched after it.
func (h *hostedTower) tighten(ctx context.Context, set *spec.Set) {
	h.mu.Lock()
	defer h.mu.Unlock()
	policies := map[string]spec.Resource{}
	runways := map[string]spec.Resource{}
	flights := map[string]spec.Resource{}
	for _, r := range set.Admitted() {
		id := r.Metadata.Namespace + "/" + r.Metadata.Name
		switch {
		case r.APIVersion == spec.CoreAPIVersion && r.Kind == "AutonomyPolicy":
			policies[id] = r
		case r.APIVersion == spec.LucyAPIVersion && r.Kind == "Runway":
			runways[id] = r
		case r.APIVersion == spec.LucyAPIVersion && r.Kind == "Flight":
			flights[id] = r
		}
	}
	for _, st := range h.tw.Flights() {
		if st.State.Final() {
			continue
		}
		var own policy.Posture
		if f, ok := flights[st.Org+"/"+st.ID]; ok {
			s, _ := f.Spec["autonomy"].(string)
			own = policy.Posture(s)
		}
		to, why := policy.Posture(""), ""
		if rw, ok := runways[st.Org+"/"+st.Project]; !ok {
			to, why = policy.PostureOwner, fmt.Sprintf("its Runway %s/%s can no longer be read", st.Org, st.Project)
		} else {
			rp, sets, err := autonomy.RunwayPosture(rw, policies)
			switch {
			case err != nil:
				to, why = policy.PostureOwner, "its Runway's autonomy can no longer be read: "+err.Error()
			case sets:
				to, why = rp, fmt.Sprintf("its Runway %s/%s now sets %s", st.Org, st.Project, rp)
			case own == "":
				to, why = policy.PostureOwner, fmt.Sprintf("nothing states its posture any more: its Runway %s/%s sets none and its Flight none", st.Org, st.Project)
			}
		}
		if tower.Stricter(own, to) {
			to, why = own, "its Flight's autonomy is now "+string(own)
		}
		if to == "" {
			continue
		}
		done, err := h.tw.Tighten(ctx, st.ID, to, why)
		switch {
		case err != nil:
			h.log.Warn("could not tighten a flight's posture", "flight", st.Org+"/"+st.ID, "to", to, "err", err)
		case done:
			h.log.Info("flight's posture tightened", "flight", st.Org+"/"+st.ID, "to", to, "why", why)
		}
	}
}
