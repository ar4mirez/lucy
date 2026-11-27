package corpus

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/sim"
	"github.com/ar4mirez/lucy/internal/tower"
)

// The airspace and runway every scenario's flights fly in, unless a flight says otherwise.
const (
	Org     = "acme"
	Project = "web"
	Repo    = Org + "/" + Project
)

// DefaultPatience is how long a pull request may make no progress before its flight is grounded,
// in the corpus: shorter than the Tower's day, so a stalled flight grounds in a few hundred virtual
// steps and the restart variant stays fast. A scenario may set its own.
const DefaultPatience = 2 * time.Hour

// Setup is a scenario's world before its first step.
type Setup struct {
	Seed       uint64       // the World's seed; derived from the scenario's id when zero
	Tower      tower.Config // timings and limits; zero values are the Tower's defaults, except Patience (DefaultPatience)
	HideMerger bool         // the forge cannot say who merged a pull request
	Required   []string     // checks Repo's base branch requires
	Flights    []Flight     // dispatched, in order, before the first step
	// Runners, when not zero, flies the Pilots through the runner protocol with that many runners
	// of Org's runs (sim.Options.Runners); zero lets the Tower drive the fake runtime directly.
	Runners int
}

// Flight is a flight dispatched at setup.
type Flight struct {
	Spec    tower.Spec
	Scripts [][]sim.Step // what each Pilot generation does, in order; none: sim.DefaultScript
}

// Fly is a Flight in Org and Project with the given posture, its first Pilot scripted by steps.
func Fly(id string, posture policy.Posture, steps ...sim.Step) Flight {
	f := Flight{Spec: tower.Spec{ID: id, Org: Org, Project: Project, Goal: "corpus flight " + id, Posture: posture}}
	if len(steps) > 0 {
		f.Scripts = [][]sim.Step{steps}
	}
	return f
}

// Then scripts the flight's next Pilot generation.
func (f Flight) Then(steps ...sim.Step) Flight {
	f.Scripts = append(slices.Clone(f.Scripts), steps)
	return f
}

// Touching marks the flight's change as touching fixed-list categories.
func (f Flight) Touching(es ...policy.Exception) Flight {
	f.Spec.Exceptions = es
	return f
}

// Action is one step of a scenario: the owner, the forge, someone else or a fault doing
// something, or a request put to the policy engine.
type Action struct {
	Name string
	Do   func(ctx context.Context, e *Env) error
}

// Assertion is one thing that must hold after a scenario's steps.
type Assertion struct {
	Name  string
	Check func(ctx context.Context, e *Env) error
}

// Variant is how a scenario is played.
type Variant int

// Variants.
const (
	AsWritten        Variant = iota // the steps as written
	RestartEveryStep                // the daemon restarts before every Tower step
)

func (v Variant) String() string {
	if v == RestartEveryStep {
		return "restart-every-step"
	}
	return "as-written"
}

// Env is a scenario's running world.
type Env struct {
	World   *sim.World
	Fixture *Fixture

	variant  Variant
	streams  map[string]string // flight -> its event stream
	hooks    []*hook
	built    map[string]built
	requests map[string]decided
	removed  map[string]error // Remove's name -> what the delete executor returned
	live     []error          // flights seen with two live runs, by liveRuns
}

type built struct {
	req   policy.Request
	facts policy.Facts
}

type decided struct {
	d   policy.Decision
	seq int64
}

type hook struct {
	name  string
	when  func(ctx context.Context, e *Env) (bool, error)
	do    []Action
	fired bool
}

// Result is what a played scenario left behind.
type Result struct {
	Log []sim.Line
}

// Seed returns the seed a scenario's World uses.
func (s Scenario) Seed() uint64 {
	if s.Setup.Seed != 0 {
		return s.Setup.Seed
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(s.ID)) // a hash.Hash never returns an error
	return h.Sum64()
}

// Play runs an executable scenario in dir, a directory the caller owns (such as t.TempDir()),
// and returns its log and every failed step, assertion or invariant.
func Play(ctx context.Context, dir string, s Scenario, v Variant) (Result, error) {
	if !s.Executable() {
		return Result{}, fmt.Errorf("corpus: %s is pending", s.ID)
	}
	worldDir, fsDir := filepath.Join(dir, "world"), filepath.Join(dir, "fs")
	for _, d := range []string{worldDir, fsDir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return Result{}, err
		}
	}
	fx, err := newFixture(fsDir)
	if err != nil {
		return Result{}, fmt.Errorf("corpus: fixture: %w", err)
	}
	cfg := s.Setup.Tower
	if cfg.Patience == 0 {
		cfg.Patience = DefaultPatience
	}
	w, err := sim.Open(ctx, worldDir, s.Seed(), sim.Options{Tower: cfg, Runners: s.Setup.Runners, RunnerOrg: Org})
	if err != nil {
		return Result{}, err
	}
	defer w.Close() //nolint:errcheck // the scenario's errors are what matter
	e := &Env{World: w, Fixture: fx, variant: v, streams: map[string]string{}, built: map[string]built{}, requests: map[string]decided{}, removed: map[string]error{}}
	w.Forge.HideMerger = s.Setup.HideMerger
	if len(s.Setup.Required) > 0 {
		w.Forge.AddRepo(Repo, s.Setup.Required...)
	}
	for _, f := range s.Setup.Flights {
		for _, steps := range f.Scripts {
			w.Runtime.Script(f.Spec.ID, steps...)
		}
		if err := w.Dispatch(ctx, f.Spec); err != nil {
			return Result{}, fmt.Errorf("corpus: dispatch %s: %w", f.Spec.ID, err)
		}
		e.streams[f.Spec.ID] = "task/" + f.Spec.Org + "/" + f.Spec.ID
	}
	for i, a := range s.Steps {
		if err := a.Do(ctx, e); err != nil {
			return Result{}, fmt.Errorf("step %d (%s): %w", i+1, a.Name, err)
		}
	}
	var errs []error
	for _, a := range s.Expect {
		if err := a.Check(ctx, e); err != nil {
			errs = append(errs, fmt.Errorf("expect %s: %w", a.Name, err))
		}
	}
	for _, h := range e.hooks {
		if !h.fired {
			errs = append(errs, fmt.Errorf("the hook %q never fired", h.name))
		}
	}
	errs = append(errs, e.live...)
	errs = append(errs, invariants(ctx, e)...)
	log, err := w.Log(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	// The fixture lives in a fresh temporary directory each run; name it the same in every log,
	// with the digests computed over its paths, so two runs of one scenario compare.
	base := filepath.Dir(fx.Home)
	for i := range log {
		if p := log[i].Payload; strings.Contains(p, base) {
			log[i].Payload = pathDigest.ReplaceAllString(strings.ReplaceAll(p, base, "{fixture}"), `"$1":"{digest}"`)
		}
	}
	return Result{Log: log}, errors.Join(errs...)
}

// pathDigest matches the digests a decision computes over its request and facts.
var pathDigest = regexp.MustCompile(`"(action_sha256|facts_sha256)":"[0-9a-f]{64}"`)

// beforeStep runs before every Tower step: the restart variant's fault, then every armed hook
// whose condition now holds, once.
func (e *Env) beforeStep(ctx context.Context, w *sim.World, _ int) error {
	if e.variant == RestartEveryStep {
		if err := w.Restart(ctx); err != nil {
			return err
		}
	}
	if err := e.liveRuns(ctx); err != nil {
		return err
	}
	for _, h := range e.hooks {
		if h.fired {
			continue
		}
		ok, err := h.when(ctx, e)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		h.fired = true
		for _, a := range h.do {
			if err := a.Do(ctx, e); err != nil {
				return fmt.Errorf("hook %s: %s: %w", h.name, a.Name, err)
			}
		}
	}
	return nil
}

// liveRuns records, with runners, every flight that has more than one run queued or leased: the
// broker fences a flight's live run before it queues the next (invariant 8).
func (e *Env) liveRuns(ctx context.Context) error {
	if e.World.Broker == nil {
		return nil
	}
	runs, err := e.World.Broker.Runs(ctx, 0, 1000)
	if err != nil {
		return err
	}
	live := map[string][]string{}
	for _, r := range runs {
		if !r.State.Final() {
			live[r.Org+"/"+r.Flight] = append(live[r.Org+"/"+r.Flight], r.Run)
		}
	}
	for _, f := range slices.Sorted(maps.Keys(live)) {
		if len(live[f]) > 1 {
			e.live = append(e.live, fmt.Errorf("invariant: flight %s has live runs %v at %s", f, live[f], e.World.Clock.Now().Format(time.RFC3339)))
		}
	}
	return nil
}

// flight returns a flight's status.
func (e *Env) flight(id string) (tower.Status, error) {
	st, ok := e.World.Tower.Flight(id)
	if !ok {
		return tower.Status{}, fmt.Errorf("no flight %q", id)
	}
	return st, nil
}

// events returns the flight's events.
func (e *Env) events(ctx context.Context, id string) ([]sim.Line, error) {
	stream, ok := e.streams[id]
	if !ok {
		return nil, fmt.Errorf("no flight %q", id)
	}
	log, err := e.World.Log(ctx)
	if err != nil {
		return nil, err
	}
	var out []sim.Line
	for _, l := range log {
		if l.Stream == stream {
			out = append(out, l)
		}
	}
	return out, nil
}
