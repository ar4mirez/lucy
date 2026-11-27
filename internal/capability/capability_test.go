package capability_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/capability"
	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/runner"
)

// The oracle is LUCY-CRED-003: a token is for one org, one run under one lease, the routes it was
// issued for and a limited time, and the owner can end it at any moment.

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fixture struct {
	t     *testing.T
	clock *clock
	store *eventstore.Store
	b     *runner.Broker
	iss   *capability.Issuer
}

func open(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	reg, err := eventstore.NewRegistry(append(append(runner.Payloads(), capability.Payloads()...), policy.DecisionRecord{})...)
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
	st, err := eventstore.Open(context.Background(), filepath.Join(dir, "lucy", "lucy.db"), eventstore.Options{
		Registry: reg, Projectors: []eventstore.Projector{runner.Projector{}, capability.Projector{}}, Now: c.Now,
		SnapshotDir: filepath.Join(dir, "lucy", "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b := runner.NewBroker(st, c.Now)
	return &fixture{t: t, clock: c, store: st, b: b, iss: &capability.Issuer{Store: st, Leases: b, Now: c.Now}}
}

var ctx = context.Background()

// lease queues and leases a run of org, and issues its token for routes.
func (f *fixture) lease(org, flight string, routes ...string) (runner.Lease, string, capability.Capability) {
	f.t.Helper()
	if err := f.b.Submit(ctx, runner.Task{Run: runner.RunID(flight, 1), Org: org, Project: "web", Flight: flight, Generation: 1, Goal: "g"}); err != nil {
		f.t.Fatal(err)
	}
	l, ok, err := f.b.Lease(ctx, "r1", runner.PlacementContainer, "", time.Minute)
	if err != nil || !ok {
		f.t.Fatalf("lease: %v %v", ok, err)
	}
	tok, c, err := f.iss.Issue(ctx, org, l.Run, l.Fence, routes, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return l, tok, c
}

func TestAValidTokenIsAccepted(t *testing.T) {
	f := open(t)
	_, tok, c := f.lease("acme", "f1", "github", "npm")
	if !strings.HasPrefix(tok, capability.Prefix) || c.ID != capability.ID(tok) || c.Run != "f1#1" || c.Org != "acme" {
		t.Fatalf("token %q, capability %+v", tok, c)
	}
	got, err := f.iss.Verify(ctx, tok, "acme", "github")
	if err != nil || got.ID != c.ID || got.Run != "f1#1" {
		t.Fatalf("verify: %+v, %v", got, err)
	}
	// The log holds the token's digest, never the token.
	evs, err := f.store.ReadAfter(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if bytes.Contains(e.Payload, []byte(tok)) || bytes.Contains(e.Payload, []byte(strings.TrimPrefix(tok, capability.Prefix))) {
			t.Fatalf("event %s holds the token", e.Type)
		}
	}
}

func TestScope(t *testing.T) {
	f := open(t)
	_, tok, _ := f.lease("acme", "f1", "github")
	if _, err := f.iss.Verify(ctx, tok, "acme", "npm"); !errors.Is(err, capability.ErrScope) {
		t.Errorf("another route: %v", err)
	}
	if _, err := f.iss.Verify(ctx, tok, "beta", "github"); !errors.Is(err, capability.ErrScope) {
		t.Errorf("another org: %v", err)
	}
	_, none, _ := f.lease("acme", "f2")
	if _, err := f.iss.Verify(ctx, none, "acme", "github"); !errors.Is(err, capability.ErrScope) {
		t.Errorf("a token for no route: %v", err)
	}
}

func TestForgedTokensAreRefused(t *testing.T) {
	f := open(t)
	_, tok, _ := f.lease("acme", "f1", "github")
	last := "A"
	if strings.HasSuffix(tok, "A") {
		last = "B"
	}
	for _, bad := range []string{"", "github", strings.TrimPrefix(tok, capability.Prefix), tok + "x", tok[:len(tok)-1] + last,
		capability.Prefix + strings.Repeat("A", 43), capability.Prefix + strings.Repeat("A", 200)} {
		if _, err := f.iss.Verify(ctx, bad, "acme", "github"); !errors.Is(err, capability.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestATokenDiesWithItsRun(t *testing.T) {
	t.Run("finished", func(t *testing.T) {
		f := open(t)
		l, tok, _ := f.lease("acme", "f1", "github")
		if err := f.b.Finish(ctx, "r1", l.Run, l.Fence, runner.Result{Outcome: runner.OutcomeFailed, Reason: "x"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.iss.Verify(ctx, tok, "acme", "github"); !errors.Is(err, capability.ErrRunEnded) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("the lease ran out", func(t *testing.T) {
		f := open(t)
		_, tok, _ := f.lease("acme", "f1", "github")
		f.clock.add(2 * time.Minute) // not renewed: past the lease, though not yet recorded as expired
		if _, err := f.iss.Verify(ctx, tok, "acme", "github"); !errors.Is(err, capability.ErrRunEnded) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a renewed lease keeps it", func(t *testing.T) {
		f := open(t)
		l, tok, _ := f.lease("acme", "f1", "github")
		f.clock.add(40 * time.Second)
		if _, err := f.b.Heartbeat(ctx, "r1", l.Run, l.Fence, time.Minute, nil, ""); err != nil {
			t.Fatal(err)
		}
		f.clock.add(40 * time.Second)
		if _, err := f.iss.Verify(ctx, tok, "acme", "github"); err != nil {
			t.Fatalf("%v", err)
		}
	})
	t.Run("its own expiry", func(t *testing.T) {
		f := open(t)
		l, _, _ := f.lease("acme", "f1", "github")
		tok, _, err := f.iss.Issue(ctx, "acme", l.Run, l.Fence, []string{"github"}, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		f.clock.add(31 * time.Second)
		if _, err := f.iss.Verify(ctx, tok, "acme", "github"); !errors.Is(err, capability.ErrExpired) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("no more than MaxTTL", func(t *testing.T) {
		f := open(t)
		l, _, _ := f.lease("acme", "f1", "github")
		_, c, err := f.iss.Issue(ctx, "acme", l.Run, l.Fence, []string{"github"}, 100*time.Hour)
		if err != nil || c.Expires.Sub(c.Issued) != capability.MaxTTL {
			t.Fatalf("%+v %v", c, err)
		}
	})
}

func TestRevoke(t *testing.T) {
	f := open(t)
	_, tok, c := f.lease("acme", "f1", "github")
	got, err := f.iss.Revoke(ctx, c.ID, "owner", "lost laptop")
	if err != nil || !got.Revoked {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := f.iss.Verify(ctx, tok, "acme", "github"); !errors.Is(err, capability.ErrRevoked) {
		t.Fatalf("a revoked token: %v", err)
	}
	if again, err := f.iss.Revoke(ctx, c.ID, "owner", "again"); err != nil || again.RevokedReason != "lost laptop" {
		t.Fatalf("revoking twice: %+v %v", again, err)
	}
	for _, id := range []string{"", "nope", "0123456789abcdef", strings.ToUpper(c.ID)} {
		if _, err := f.iss.Revoke(ctx, id, "owner", ""); !errors.Is(err, capability.ErrUnknown) {
			t.Errorf("revoke %q: %v", id, err)
		}
	}
	list, err := f.iss.List(ctx, true)
	if err != nil || len(list) != 1 || list[0].ID != c.ID || !list[0].Revoked {
		t.Fatalf("list %+v %v", list, err)
	}
	f.clock.add(2 * capability.DefaultTTL)
	if live, _ := f.iss.List(ctx, false); len(live) != 0 {
		t.Errorf("an expired token is listed: %+v", live)
	}
}

// Each heartbeat's renewal keeps a live run's gateway access past DefaultTTL with its newest
// token, and the token before it is refused as rotated.
func TestRenewKeepsALiveRunsAccess(t *testing.T) {
	f := open(t)
	l, tok, first := f.lease("acme", "f1", "github")
	for range 4 {
		f.clock.add(50 * time.Second)
		if _, err := f.b.Heartbeat(ctx, "r1", l.Run, l.Fence, 5*time.Minute, nil, ""); err != nil {
			t.Fatal(err)
		}
		f.clock.add(70 * time.Second)
		if _, err := f.b.Heartbeat(ctx, "r1", l.Run, l.Fence, 5*time.Minute, nil, ""); err != nil {
			t.Fatal(err)
		}
		next, c, err := f.iss.Renew(ctx, l.Run, l.Fence)
		if err != nil || next == tok || c.Org != "acme" || strings.Join(c.Routes, ",") != "github" || !c.Start.Equal(first.Start) {
			t.Fatalf("renew %+v, %v", c, err)
		}
		if _, err := f.iss.Verify(ctx, tok, "acme", "github"); !errors.Is(err, capability.ErrRevoked) {
			t.Fatalf("the rotated token: %v", err)
		}
		tok = next
	}
	if _, err := f.iss.Verify(ctx, tok, "acme", "github"); err != nil {
		t.Fatalf("the newest token, %s after the lease began: %v", 8*time.Minute, err)
	}
	all, err := f.iss.List(ctx, true)
	if err != nil || len(all) != 5 {
		t.Fatalf("list %+v, %v", all, err)
	}
	for _, c := range all[1:] {
		if !c.Revoked || c.RevokedReason != "rotated" {
			t.Fatalf("an older token %+v", c)
		}
	}
}

// No renewal reaches past MaxTTL from the lease's first token, and a revoked token is not renewed.
func TestRenewLimits(t *testing.T) {
	t.Run("MaxTTL from the lease's start", func(t *testing.T) {
		f := open(t)
		l, _, first := f.lease("acme", "f1", "github")
		if _, err := f.b.Heartbeat(ctx, "r1", l.Run, l.Fence, 5*time.Minute, nil, ""); err != nil {
			t.Fatal(err)
		}
		var last capability.Capability
		for {
			f.clock.add(4 * time.Minute)
			if _, err := f.b.Heartbeat(ctx, "r1", l.Run, l.Fence, 5*time.Minute, nil, ""); err != nil {
				t.Fatal(err)
			}
			_, c, err := f.iss.Renew(ctx, l.Run, l.Fence)
			if errors.Is(err, capability.ErrExpired) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			last = c
		}
		if end := first.Start.Add(capability.MaxTTL); last.Expires.After(end) || f.clock.Now().Before(end) {
			t.Fatalf("the last token expires %v, the lease began %v, refused at %v", last.Expires, first.Start, f.clock.Now())
		}
	})
	t.Run("revoked", func(t *testing.T) {
		f := open(t)
		l, tok, _ := f.lease("acme", "f1", "github")
		if _, err := f.iss.Revoke(ctx, capability.ID(tok), "owner", "test"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.iss.Renew(ctx, l.Run, l.Fence); !errors.Is(err, capability.ErrRevoked) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a lease without a token", func(t *testing.T) {
		f := open(t)
		l, _, _ := f.lease("acme", "f1", "github")
		if _, _, err := f.iss.Renew(ctx, l.Run, l.Fence+1); !errors.Is(err, capability.ErrUnknown) {
			t.Fatalf("%v", err)
		}
	})
}

// RevokeRun revokes a run's tokens under its fence and older ones, and no other run's.
func TestRevokeRun(t *testing.T) {
	f := open(t)
	l, tok, _ := f.lease("acme", "f1", "github")
	_, other, _ := f.lease("acme", "f2", "github")
	if err := f.iss.RevokeRun(ctx, l.Run, l.Fence-1, "replaced"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.iss.Verify(ctx, tok, "acme", "github"); err != nil {
		t.Fatalf("a token of a newer fence: %v", err)
	}
	if err := f.iss.RevokeRun(ctx, l.Run, l.Fence, "finished"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.iss.Verify(ctx, tok, "acme", "github"); !errors.Is(err, capability.ErrRevoked) {
		t.Fatalf("%v", err)
	}
	if _, err := f.iss.Verify(ctx, other, "acme", "github"); err != nil {
		t.Fatalf("another run's token: %v", err)
	}
}
