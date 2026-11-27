package tower_test

import (
	"context"
	"errors"
	"strings"
	"testing"

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
