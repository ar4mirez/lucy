package tower_test

import (
	"context"
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
	ok := tower.Spec{ID: "f1", Org: "acme", Project: "web", Goal: "g"}
	if err := w.Tower.Dispatch(ctx, ok); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]tower.Spec{
		"duplicate":  ok,
		"no goal":    {ID: "f2", Org: "acme", Project: "web"},
		"no project": {ID: "f2", Org: "acme", Goal: "g"},
		"slash id":   {ID: "a/b", Org: "acme", Project: "web", Goal: "g"},
		"hash org":   {ID: "f2", Org: "ac#me", Project: "web", Goal: "g"},
	} {
		if err := w.Tower.Dispatch(ctx, s); err == nil {
			t.Errorf("%s: dispatched", name)
		}
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
	if err := w.Tower.Dispatch(ctx, tower.Spec{ID: "f1", Org: "acme", Project: "web", Goal: "g"}); err != nil {
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
	if err := w.Dispatch(ctx, tower.Spec{ID: "f1", Org: "acme", Project: "web", Goal: "g"}, sim.Ask(q)); err != nil {
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
