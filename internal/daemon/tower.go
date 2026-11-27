package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/spec"
	"github.com/ar4mirez/lucy/internal/tower"
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
	tw  *tower.Tower
	mu  sync.Mutex
	log *slog.Logger
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
	ReasonDispatchDisabled = "DispatchDisabled"
	ReasonTakeoverGate     = "TakeoverGate"
	ReasonNameInUse        = "NameInUse"
	ReasonFinished         = "Finished"
	ReasonDispatchFailed   = "DispatchFailed"
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
func startTower(ctx context.Context, store *eventstore.Store, broker *runner.Broker, set *spec.Set, poll time.Duration, log *slog.Logger,
	dispatch bool, gate func(org, project string) error) (*hostedTower, error) {
	tw, err := tower.New(ctx, tower.Config{Store: store, Runtime: runner.Runtime{Broker: broker}, Forge: stubForge{},
		Clock: wallClock{}, ForgeIdentity: stubIdentity, PollInterval: poll})
	if err != nil {
		return nil, err
	}
	h := &hostedTower{tw: tw, log: log}
	h.dispatch(ctx, set, dispatch, gate)
	return h, nil
}

// dispatch decides, for every admitted Flight whose Ready condition is True, whether the Tower
// flies it, and records why as its Dispatched condition. A Flight that is not Ready is never
// dispatched (LUCY-SPEC-012). Without dispatch on, none is. A Flight the Tower flies already, by
// its name, in its namespace and runway, was dispatched by an earlier start, unless it is
// finished; one whose name a flight of another org or runway holds is not dispatched.
func (h *hostedTower) dispatch(ctx context.Context, set *spec.Set, enabled bool, gate func(org, project string) error) {
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
		if !enabled {
			mark(spec.False, ReasonDispatchDisabled, "the daemon dispatches no Flight unless it is started with --dispatch")
			continue
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
			mark(spec.True, ReasonDispatched, "the Tower flies it")
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

// run steps the Tower every poll interval until ctx ends; a step's error is logged, not fatal.
func (h *hostedTower) run(ctx context.Context, poll time.Duration) {
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		h.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// step steps the Tower once and logs each flight's step error, except the stub forge's refusal,
// which every flight in review meets on every step.
func (h *hostedTower) step(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	err := h.tw.Step(ctx)
	if err == nil || ctx.Err() != nil {
		return
	}
	errs := []error{err}
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		errs = j.Unwrap()
	}
	for _, e := range errs {
		if !errors.Is(e, errNoForge) {
			h.log.Warn("tower step failed", "err", e)
		}
	}
}
