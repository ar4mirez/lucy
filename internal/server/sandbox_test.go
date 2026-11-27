package server

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/runner"
	"github.com/ar4mirez/lucy/internal/sandbox"
	"github.com/ar4mirez/lucy/internal/sandbox/berth"
	"github.com/ar4mirez/lucy/internal/sandbox/berth/berthtest"
)

// Without a Sandbox binding nothing can wait on the owner: the list is empty, and a confirmation
// is refused as unavailable rather than recorded anywhere.
func TestSandboxWithoutABinding(t *testing.T) {
	f := start(t, os.Geteuid(), 0, nil)
	list, err := f.client.SandboxHeld(ctx)
	if err != nil || list.Held == nil || len(list.Held) != 0 {
		t.Fatalf("held %+v, %v", list, err)
	}
	_, err = f.client.SandboxConfirm(ctx, api.ConfirmParams{Org: "acme", Op: "restart", Decision: 1})
	wantCode(t, err, api.CodeUnavailable)
}

// The owner lists a container operation that waits on them and confirms the decision they were
// shown; a confirmation of a replaced decision, of nothing, or of a malformed operation is refused.
// The oracle is LUCY-SBX-003: only the owner's confirmation of exactly these sessions clears it.
func TestSandboxHeldAndConfirmed(t *testing.T) {
	b := berthtest.Start(t, "acme")
	b.SetRunning("acme", true)
	var sb *sandbox.Sandbox
	var broker *runner.Broker
	f := start(t, os.Geteuid(), 0, func(o *Options) {
		broker = runner.NewBroker(o.Store, nil)
		broker.VerifyPlacementWith(testPlacement{})
		c, err := berth.New(berth.Options{Socket: b.Socket})
		if err != nil {
			t.Fatal(err)
		}
		if sb, err = sandbox.New(o.Store, c, broker); err != nil {
			t.Fatal(err)
		}
		o.Runs, o.Containers = broker, sb
	})
	if err := broker.Submit(ctx, runner.Task{Run: runner.RunID("f1", 1), Org: "acme", Project: "web", Flight: "f1", Generation: 1, Goal: "fix"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := broker.Lease(ctx, "r1", runner.PlacementContainer, "acme", "test-proof", time.Minute); err != nil || !ok {
		t.Fatalf("lease: %v %v", ok, err)
	}
	first, err := sb.Request(ctx, "acme", sandbox.OpRestart, sandbox.Daemon("test"))
	if err != nil || !first.NeedsOwner {
		t.Fatalf("restart: %+v, %v", first, err)
	}
	second, err := sb.Request(ctx, "acme", sandbox.OpRestart, sandbox.Daemon("test"))
	if err != nil {
		t.Fatal(err)
	}

	list, err := f.client.SandboxHeld(ctx)
	if err != nil || len(list.Held) != 1 {
		t.Fatalf("held %+v, %v", list, err)
	}
	if h := list.Held[0]; h.Org != "acme" || h.Op != "restart" || h.Decision != second.DecisionSeq || h.Outcome != "holding" ||
		!slices.Equal(h.LiveSessions, []string{"f1#1"}) || h.Since.IsZero() {
		t.Fatalf("held op %+v", h)
	}
	for _, c := range []struct {
		p     api.ConfirmParams
		field string
	}{
		{api.ConfirmParams{Org: "acme", Op: "restart", Decision: first.DecisionSeq}, "decision"},
		{api.ConfirmParams{Org: "acme", Op: "down", Decision: second.DecisionSeq}, "op"},
		{api.ConfirmParams{Org: "acme", Op: "destroy", Decision: second.DecisionSeq}, "op"},
		{api.ConfirmParams{Org: "Acme", Op: "restart", Decision: second.DecisionSeq}, "org"},
	} {
		_, err := f.client.SandboxConfirm(ctx, c.p)
		if ae := wantCode(t, err, api.CodeInvalidParams); ae.Data.Field != c.field {
			t.Errorf("confirm %+v: field %q, want %q", c.p, ae.Data.Field, c.field)
		}
	}
	res, err := f.client.SandboxConfirm(ctx, api.ConfirmParams{Org: "acme", Op: "restart", Decision: second.DecisionSeq})
	if err != nil || res.Org != "acme" || res.Op != "restart" || res.Approval == "" {
		t.Fatalf("confirm: %+v, %v", res, err)
	}
	if list, _ := f.client.SandboxHeld(ctx); len(list.Held) != 0 {
		t.Fatalf("still held after the confirmation: %+v", list)
	}
	if len(b.Changes()) != 0 {
		t.Fatal("a confirmation alone reached berth")
	}
	// The daemon's next request is cleared on the owner's confirmation, and names it.
	done, err := sb.Request(ctx, "acme", sandbox.OpRestart, sandbox.Daemon("test"))
	if err != nil || !done.Acted || len(b.Changes()) != 1 {
		t.Fatalf("restart after the owner confirmed: %+v, %v", done, err)
	}
}
