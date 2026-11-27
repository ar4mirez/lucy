package daemon

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/spec"
	"github.com/ar4mirez/lucy/internal/tower"
)

// The daemon hosts the Tower (m1 slice 7a): it flies the Flights loaded from the config
// directory, starting their Pilots through the runner protocol, and its Pilots' questions reach
// the owner through lucy questions and lucy answer. Until the validation slice connects a forge,
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

// startTower opens the Tower on the daemon's store and broker, rebuilding every flight from the
// log, and dispatches every Ready Flight the set holds that it does not fly yet.
func startTower(ctx context.Context, store *eventstore.Store, broker *runner.Broker, set spec.Set, poll time.Duration, log *slog.Logger) (*hostedTower, error) {
	tw, err := tower.New(ctx, tower.Config{Store: store, Runtime: runner.Runtime{Broker: broker}, Forge: stubForge{},
		Clock: wallClock{}, ForgeIdentity: stubIdentity, PollInterval: poll})
	if err != nil {
		return nil, err
	}
	h := &hostedTower{tw: tw, log: log}
	h.dispatch(ctx, set)
	return h, nil
}

// dispatch dispatches every admitted Flight whose Ready condition is True and that the Tower does
// not fly yet, by its name. A Flight that is not Ready is never dispatched (LUCY-SPEC-012).
func (h *hostedTower) dispatch(ctx context.Context, set spec.Set) {
	bases := map[string]string{}
	for _, r := range set.Admitted() {
		if r.APIVersion == spec.LucyAPIVersion && r.Kind == "Runway" {
			b, _ := r.Spec["defaultBranch"].(string)
			bases[r.Metadata.Namespace+"/"+r.Metadata.Name] = b
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, res := range set.Results {
		r := res.Resource
		if r == nil || r.APIVersion != spec.LucyAPIVersion || r.Kind != "Flight" || !ready(res) {
			continue
		}
		if _, ok := h.tw.Flight(r.Metadata.Name); ok {
			continue // flown already: dispatched by an earlier start
		}
		str := func(k string) string { s, _ := r.Spec[k].(string); return s }
		s := tower.Spec{ID: r.Metadata.Name, Org: r.Metadata.Namespace, Project: str("runway"), Goal: str("goal"),
			Base: bases[r.Metadata.Namespace+"/"+str("runway")], Delivery: str("delivery"),
			Posture: policy.Posture(str("autonomy")), Acceptance: str("acceptance")}
		if err := h.tw.Dispatch(ctx, s); err != nil {
			h.log.Warn("flight not dispatched", "flight", r.ID(), "err", err)
			continue
		}
		h.log.Info("flight dispatched", "flight", r.ID())
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

func (h *hostedTower) step(ctx context.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.tw.Step(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, errNoForge) {
		h.log.Warn("tower step failed", "err", err)
	}
}
