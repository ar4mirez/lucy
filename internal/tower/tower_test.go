package tower_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/sim"
	"github.com/ar4mirez/lucy/internal/tower"
)

// The Tower's flights are exercised end to end in internal/sim; these are its refusals.

func world(t *testing.T) *sim.World {
	t.Helper()
	w, err := sim.Open(context.Background(), t.TempDir(), 1, sim.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() }) //nolint:errcheck // best effort in cleanup
	return w
}

func TestNewNeedsEverySeam(t *testing.T) {
	w := world(t)
	for _, cfg := range []tower.Config{
		{Runtime: w.Runtime, Forge: w.Forge, Clock: w.Clock, ForgeIdentity: sim.DefaultIdentity},
		{Store: w.Store, Forge: w.Forge, Clock: w.Clock, ForgeIdentity: sim.DefaultIdentity},
		{Store: w.Store, Runtime: w.Runtime, Clock: w.Clock, ForgeIdentity: sim.DefaultIdentity},
		{Store: w.Store, Runtime: w.Runtime, Forge: w.Forge, ForgeIdentity: sim.DefaultIdentity},
		{Store: w.Store, Runtime: w.Runtime, Forge: w.Forge, Clock: w.Clock},
	} {
		if _, err := tower.New(context.Background(), cfg); err == nil {
			t.Errorf("New(%+v) succeeded", cfg)
		}
	}
}

func TestDispatchRefuses(t *testing.T) {
	ctx := context.Background()
	w := world(t)
	ok := tower.Spec{ID: "f1", Org: "acme", Project: "web", Goal: "g", Posture: policy.PostureOwner, Delivery: tower.DeliveryPullRequest, Acceptance: "the checks pass"}
	if err := w.Tower.Dispatch(ctx, ok); err != nil {
		t.Fatal(err)
	}
	ready := func(f func(*tower.Spec)) tower.Spec { s := ok; s.ID = "f2"; f(&s); return s }
	for name, s := range map[string]tower.Spec{
		"duplicate": ok,
		"slash id":  ready(func(s *tower.Spec) { s.ID = "a/b" }),
		"hash org":  ready(func(s *tower.Spec) { s.Org = "ac#me" }),
	} {
		if err := w.Tower.Dispatch(ctx, s); err == nil {
			t.Errorf("%s: dispatched", name)
		}
	}
	// Readiness (LUCY-SPEC-012): each missing part is refused and named, and nothing is recorded.
	for name, c := range map[string]struct {
		s    tower.Spec
		want string
	}{
		"no goal":             {ready(func(s *tower.Spec) { s.Goal = " " }), "a goal"},
		"no runway":           {ready(func(s *tower.Spec) { s.Project = "" }), "a delivery path (its runway)"},
		"no delivery":         {ready(func(s *tower.Spec) { s.Delivery = "" }), "a delivery path (pull-request)"},
		"a research delivery": {ready(func(s *tower.Spec) { s.Delivery = "report" }), "a delivery path the Tower flies"},
		"no autonomy":         {ready(func(s *tower.Spec) { s.Posture = "" }), "an autonomy posture"},
		"an unknown autonomy": {ready(func(s *tower.Spec) { s.Posture = "yolo" }), "a known autonomy posture"},
		"no acceptance test":  {ready(func(s *tower.Spec) { s.Acceptance = "" }), "an acceptance test"},
	} {
		err := w.Tower.Dispatch(ctx, c.s)
		if !errors.Is(err, tower.ErrNotReady) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a refusal naming %q", name, err, c.want)
		}
		if _, ok := w.Tower.Flight("f2"); ok {
			t.Fatalf("%s: an unready flight was recorded", name)
		}
	}
	all := tower.Readiness(tower.Spec{ID: "f3", Org: "acme"})
	if len(all) != 4 {
		t.Errorf("an empty flight lacks %v; want the goal, delivery path, autonomy and acceptance test", all)
	}
	if st, _ := w.Tower.Flight("f1"); st.State != tower.StateDispatched {
		t.Fatalf("status %+v", st)
	}
	if _, ok := w.Tower.Flight("nope"); ok {
		t.Fatal("found an unknown flight")
	}
}

func TestOwnerCallsNeedTheRightState(t *testing.T) {
	ctx := context.Background()
	w := world(t)
	if err := w.Tower.Dispatch(ctx, tower.Spec{ID: "f1", Org: "acme", Project: "web", Goal: "g", Posture: policy.PostureOwner, Delivery: tower.DeliveryPullRequest, Acceptance: "the checks pass"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Tower.Approve(ctx, "f1", policy.ByOwner); err == nil || !strings.Contains(err.Error(), "no pull request") {
		t.Errorf("approve before a pull request: %v", err)
	}
	if err := w.Tower.Answer(ctx, "f1", "x"); err == nil || !strings.Contains(err.Error(), "not asking") {
		t.Errorf("answer without a question: %v", err)
	}
	if err := w.Tower.Approve(ctx, "nope", policy.ByOwner); err == nil {
		t.Error("approved an unknown flight")
	}
	if err := w.Tower.Answer(ctx, "nope", "x"); err == nil {
		t.Error("answered an unknown flight")
	}
}

// A long question is capped in the log without splitting a UTF-8 sequence.
func TestQuestionCapped(t *testing.T) {
	ctx := context.Background()
	w := world(t)
	q := strings.Repeat("é", 2000)
	if err := w.Dispatch(ctx, tower.Spec{ID: "f1", Org: "acme", Project: "web", Goal: "g", Posture: policy.PostureOwner, Delivery: tower.DeliveryPullRequest, Acceptance: "the checks pass"}, sim.Ask(q)); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(ctx, sim.RunOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := w.Tower.Flight("f1")
	if st.State != tower.StateAsking || len(st.Question) > 1024 || !strings.HasPrefix(q, st.Question) || len(st.Question)%2 != 0 {
		t.Fatalf("question of %d bytes in state %s", len(st.Question), st.State)
	}
}

// failingPolls is a Runtime whose Poll fails for one flight's sessions, and counts those polls.
type failingPolls struct {
	tower.Runtime
	flight   string
	sessions map[string]bool
	polls    []time.Time
	clock    tower.Clock
}

func (r *failingPolls) Start(ctx context.Context, req tower.PilotRequest) (string, error) {
	s, err := r.Runtime.Start(ctx, req)
	if err == nil && req.Flight == r.flight {
		r.sessions[s] = true
	}
	return s, err
}

func (r *failingPolls) Poll(ctx context.Context, session string) (tower.PilotReport, error) {
	if r.sessions[session] {
		r.polls = append(r.polls, r.clock.Now())
		return tower.PilotReport{}, errors.New("the runtime cannot read this session")
	}
	return r.Runtime.Poll(ctx, session)
}

// A flight whose step keeps failing backs off on its own (LUCY-SPEC-008): it is retried after 1 s,
// 2 s, 4 s and so on, not sooner whatever wakes the Tower, and its status shows why; the other
// flight's question reaches the owner on the usual schedule, one poll after its Pilot starts.
func TestAFailingFlightBacksOffAlone(t *testing.T) {
	ctx := context.Background()
	w := world(t)
	rt := &failingPolls{Runtime: w.Runtime, flight: "bad", sessions: map[string]bool{}, clock: w.Clock}
	tw, err := tower.New(ctx, tower.Config{Store: w.Store, Runtime: rt, Forge: w.Forge, Clock: w.Clock, ForgeIdentity: sim.DefaultIdentity})
	if err != nil {
		t.Fatal(err)
	}
	spec := func(id string) tower.Spec {
		return tower.Spec{ID: id, Org: "acme", Project: "web", Goal: "g", Posture: policy.PostureOwner, Delivery: tower.DeliveryPullRequest, Acceptance: "the checks pass"}
	}
	w.Runtime.Script("good", sim.Ask("which account?"))
	for _, id := range []string{"bad", "good"} {
		if err := tw.Dispatch(ctx, spec(id)); err != nil {
			t.Fatal(err)
		}
	}
	start := w.Clock.Now()
	if err := tw.Step(ctx); err != nil { // both Pilots start
		t.Fatal(err)
	}
	var asked time.Time
	for i := 0; i < 7; i++ {
		next, active := tw.NextWake()
		if !active {
			t.Fatal("no flight is active")
		}
		w.Clock.Advance(next.Sub(w.Clock.Now()))
		err := tw.Step(ctx)
		var fe *tower.FlightError
		if !errors.As(err, &fe) || fe.Flight != "bad" {
			t.Fatalf("step %d: %v, want the bad flight's error alone", i, err)
		}
		if st, _ := tw.Flight("good"); st.State == tower.StateAsking && asked.IsZero() {
			asked = w.Clock.Now()
		}
		if err := tw.Step(ctx); err != nil { // woken again at once: the bad flight's backoff holds
			t.Fatalf("step %d, woken again: %v", i, err)
		}
	}
	if asked.IsZero() || asked.Sub(start) > tower.DefaultPollInterval {
		t.Fatalf("the good flight asked at %v after its start, want within one poll interval (%v)", asked.Sub(start), tower.DefaultPollInterval)
	}
	if len(rt.polls) != 7 {
		t.Fatalf("the bad flight was polled %d times in 7 wakes, want 7", len(rt.polls))
	}
	for i := 1; i < len(rt.polls); i++ {
		want := time.Second << (i - 1)
		if got := rt.polls[i].Sub(rt.polls[i-1]); got != want {
			t.Errorf("retry %d of the bad flight came %v after the last, want %v", i, got, want)
		}
	}
	st, _ := tw.Flight("bad")
	if !strings.Contains(st.StepError, "cannot read this session") || st.Failures != 7 || !st.RetryAt.After(w.Clock.Now()) {
		t.Fatalf("the bad flight's status: %+v, want its error, 7 failures and a retry time ahead", st)
	}
	if st, _ := tw.Flight("good"); st.StepError != "" || st.Failures != 0 || !st.RetryAt.IsZero() {
		t.Fatalf("the good flight's status: %+v, want no step error", st)
	}
}
