package config

import (
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/policy"
)

// TestHoldingRendersPolicyReasons evaluates merges that hold and renders each decision as a
// holding message for an owner titled "Commander": the reason names the Commander, never the
// role id.
func TestHoldingRendersPolicyReasons(t *testing.T) {
	y := mustParse(t, "[you]\ntitle = \"Commander\"\n").You
	req := policy.Request{
		Actor:  policy.Actor{ID: "lucy", Role: policy.RoleDaemon},
		Kind:   policy.KindMerge,
		Target: policy.Target{Org: "acme", Project: "web", Resource: "acme/web#7"},
		Inputs: []policy.Input{{Source: policy.SourceDaemon, Ref: "task-1"}},
	}
	cases := map[policy.Posture]string{
		policy.PostureOwner:  "holding: waiting on the Commander: the owner posture merges only on the word of the Commander",
		policy.PostureClient: "holding: waiting on the Commander: the client posture merges only after the Commander relays the client's confirmation",
	}
	for posture, want := range cases {
		d := policy.Evaluate(req, policy.Facts{Posture: posture, Checks: policy.ChecksGreen, HeadSHA: "abc123"})
		if d.Outcome != policy.Holding {
			t.Fatalf("%s posture: got %s by %s (%s), want holding", posture, d.Outcome, d.Rule, d.Reason)
		}
		got := y.Holding(d.Reason)
		if got != want {
			t.Errorf("%s posture: Holding = %q, want %q", posture, got, want)
		}
		if strings.Contains(got, "{owner}") || strings.Contains(strings.ReplaceAll(got, "owner posture", ""), "owner") {
			t.Errorf("%s posture: %q names the owner by role id", posture, got)
		}
	}
}
