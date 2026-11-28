package autonomy

import (
	"errors"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/spec"
)

// policyDoc is an AutonomyPolicy the spec's schema accepts, with levels 0 (requireApproval all),
// 1 (security-critical-only) and 2 (none); extra adds to level 1 and extraSpec to spec.
func policyDoc(extraLevel, extraGuardrail, extraSpec string) string {
	lv := func(n, ra, more, g string) string {
		return "    - { level: " + n + ", name: L" + n + ", permissions: { read: ['*'], write: [], execute: [] }, guardrails: { requireApproval: " + ra + g + " }, monitoring: continuous" + more + " }\n"
	}
	return "apiVersion: ai-sdlc.io/v1alpha1\nkind: AutonomyPolicy\nmetadata: { name: standard, namespace: acme }\nspec:\n  levels:\n" +
		lv("0", "all", "", "") + lv("1", "security-critical-only", extraLevel, extraGuardrail) + lv("2", "none", "", "") +
		"  promotionCriteria: {}\n  demotionTriggers:\n    - { trigger: critical-security-incident, action: demote-to-0, cooldown: 4w }\n" + extraSpec
}

func load(t *testing.T, doc string) spec.Resource {
	t.Helper()
	res := spec.LoadFile("p.yaml", []byte(doc))
	if len(res) != 1 || !res[0].Valid() {
		t.Fatalf("not admitted: %+v", res)
	}
	return *res[0].Resource
}

func runway(t *testing.T, extra string) spec.Resource {
	t.Helper()
	return load(t, "apiVersion: lucy/v1alpha1\nkind: Runway\nmetadata: { name: web, namespace: acme }\nspec: { "+extra+" }\n")
}

// A level's requireApproval sets the posture: all is owner, security-critical-only green; none is
// refused, as is a level or a policy that does not exist; clientConfirms adds the client's word.
func TestRunwayPosture(t *testing.T) {
	policies := map[string]spec.Resource{"acme/standard": load(t, policyDoc("", "", ""))}
	for name, c := range map[string]struct {
		runway string
		want   policy.Posture
		sets   bool
		err    error
	}{
		"level 0":              {"autonomyPolicy: standard, autonomyLevel: 0", policy.PostureOwner, true, nil},
		"level 1":              {"autonomyPolicy: standard, autonomyLevel: 1", policy.PostureGreen, true, nil},
		"level 1, client":      {"autonomyPolicy: standard, autonomyLevel: 1, clientConfirms: true", policy.PostureClient, true, nil},
		"client alone":         {"clientConfirms: true", policy.PostureClient, true, nil},
		"none":                 {"", "", false, nil},
		"requireApproval none": {"autonomyPolicy: standard, autonomyLevel: 2", "", false, ErrUnsupported},
		"no such level":        {"autonomyPolicy: standard, autonomyLevel: 7", "", false, ErrInvalid},
		"no such policy":       {"autonomyPolicy: other, autonomyLevel: 0", "", false, ErrInvalid},
	} {
		got, sets, err := RunwayPosture(runway(t, c.runway), policies)
		if got != c.want || sets != c.sets || !errors.Is(err, c.err) || (c.err == nil) != (err == nil) {
			t.Errorf("%s: %q, %v, %v; want %q, %v, %v", name, got, sets, err, c.want, c.sets, c.err)
		}
	}
}

// Only what lucy acts on, and what the format requires, is accepted; every other field of a policy
// a Runway names is refused by its path.
func TestCheckPolicy(t *testing.T) {
	if err := CheckPolicy(load(t, policyDoc(", description: 'senior'", "", ""))); err != nil {
		t.Fatalf("a policy lucy accepts: %v", err)
	}
	for name, c := range map[string]struct{ doc, path string }{
		"minimumDuration": {policyDoc(", minimumDuration: 8w", "", ""), "spec.levels[1].minimumDuration"},
		"maxLinesPerPR":   {policyDoc("", ", maxLinesPerPR: 500", ""), "spec.levels[1].guardrails.maxLinesPerPR"},
		"blockedPaths":    {policyDoc("", ", blockedPaths: ['**/auth/**']", ""), "spec.levels[1].guardrails.blockedPaths"},
	} {
		err := CheckPolicy(load(t, c.doc))
		if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), c.path) || !strings.Contains(err.Error(), "acme/standard") {
			t.Errorf("%s: %v, want ErrUnsupported naming acme/standard and %s", name, err, c.path)
		}
	}
}

// A Flight's autonomy may only tighten its Runway's posture, never loosen it; with no Runway
// posture the Flight's own applies.
func TestEffective(t *testing.T) {
	for _, c := range []struct {
		runway policy.Posture
		sets   bool
		flight policy.Posture
		want   policy.Posture
		loosen bool
	}{
		{policy.PostureGreen, true, "", policy.PostureGreen, false},
		{policy.PostureGreen, true, policy.PostureOwner, policy.PostureOwner, false},
		{policy.PostureGreen, true, policy.PostureClient, policy.PostureClient, false},
		{policy.PostureOwner, true, policy.PostureGreen, "", true},
		{policy.PostureClient, true, policy.PostureOwner, "", true},
		{policy.PostureClient, true, policy.PostureGreen, "", true},
		{policy.PostureOwner, true, policy.PostureOwner, policy.PostureOwner, false},
		{"", false, policy.PostureGreen, policy.PostureGreen, false},
	} {
		got, err := Effective(c.runway, c.sets, c.flight)
		if got != c.want || errors.Is(err, ErrLoosens) != c.loosen {
			t.Errorf("runway %q (%v), flight %q: %q, %v; want %q, loosens %v", c.runway, c.sets, c.flight, got, err, c.want, c.loosen)
		}
	}
}
