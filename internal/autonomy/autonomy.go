// Package autonomy loads a project's merge posture from the spec's AutonomyPolicy (LUCY-POL-005,
// LUCY-SPEC-010). A Runway names an AutonomyPolicy in its namespace and a level of it; lucy reads
// only that level's guardrails.requireApproval: all is the owner posture, security-critical-only the
// green posture (the fixed-list exceptions still need the owner). none and architecture-changes-only
// are refused: lucy never merges without a human on the fixed list. A Runway's clientConfirms adds
// the client's confirmation, relayed by the owner, on top: the client posture. A Flight's own
// autonomy may only tighten the Runway's posture, never loosen it.
//
// What lucy accepts in an AutonomyPolicy a Runway names, as with Pipelines: the fields the format
// requires (spec.levels, promotionCriteria and demotionTriggers; per level level, name,
// permissions, guardrails and monitoring), which it accepts and does not act on yet, a level's
// description, a label, and guardrails.requireApproval, which it acts on. Every other field is
// refused by name, as not supported yet (TODO(LUCY-SPEC-018): demotion triggers and the other
// guardrails, m2). Promotion and demotion are the owner's, by hand: lucy changes no level.
package autonomy

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/spec"
)

// ErrUnsupported refuses an AutonomyPolicy field or value lucy does not act on yet.
var ErrUnsupported = errors.New("autonomy: not supported yet")

// ErrInvalid refuses a Runway's autonomy that names no admitted policy or level.
var ErrInvalid = errors.New("autonomy: invalid")

// ErrLoosens refuses a Flight whose autonomy is looser than its Runway's posture.
var ErrLoosens = errors.New("autonomy: a Flight's autonomy may only tighten its Runway's posture")

// strictness orders the postures: a stricter one needs more human word before a merge.
var strictness = map[policy.Posture]int{policy.PostureGreen: 0, policy.PostureOwner: 1, policy.PostureClient: 2}

var (
	specFields      = []string{"levels", "promotionCriteria", "demotionTriggers"}
	levelFields     = []string{"level", "name", "description", "permissions", "guardrails", "monitoring"}
	guardrailFields = []string{"requireApproval"}
)

// CheckPolicy refuses an AutonomyPolicy with a field lucy does not accept, naming its path.
func CheckPolicy(r spec.Resource) error {
	where := r.Metadata.Namespace + "/" + r.Metadata.Name
	refuse := func(path string) error {
		return fmt.Errorf("%w: AutonomyPolicy %s: %s: lucy does not act on it; TODO(LUCY-SPEC-018)", ErrUnsupported, where, path)
	}
	if k := extra(r.Spec, specFields); k != "" {
		return refuse("spec." + k)
	}
	levels, _ := r.Spec["levels"].([]any)
	for i, l := range levels {
		lv, _ := l.(map[string]any)
		if k := extra(lv, levelFields); k != "" {
			return refuse(fmt.Sprintf("spec.levels[%d].%s", i, k))
		}
		g, _ := lv["guardrails"].(map[string]any)
		if k := extra(g, guardrailFields); k != "" {
			return refuse(fmt.Sprintf("spec.levels[%d].guardrails.%s", i, k))
		}
	}
	return nil
}

// extra returns the first key of m, sorted, that is not in allowed.
func extra(m map[string]any, allowed []string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ok := false
		for _, a := range allowed {
			ok = ok || a == k
		}
		if !ok {
			return k
		}
	}
	return ""
}

// RunwayPosture returns the posture a Runway's autonomy sets, and false when it names no
// AutonomyPolicy. policies holds the admitted AutonomyPolicies by namespace/name.
func RunwayPosture(runway spec.Resource, policies map[string]spec.Resource) (policy.Posture, bool, error) {
	name, _ := runway.Spec["autonomyPolicy"].(string)
	clientConfirms, _ := runway.Spec["clientConfirms"].(bool)
	where := runway.Metadata.Namespace + "/" + runway.Metadata.Name
	if name == "" {
		if clientConfirms {
			return policy.PostureClient, true, nil
		}
		return "", false, nil
	}
	p, ok := policies[runway.Metadata.Namespace+"/"+name]
	if !ok {
		return "", false, fmt.Errorf("%w: Runway %s names AutonomyPolicy %q, which is not admitted in namespace %q", ErrInvalid, where, name, runway.Metadata.Namespace)
	}
	if err := CheckPolicy(p); err != nil {
		return "", false, err
	}
	level := runway.Spec["autonomyLevel"]
	levels, _ := p.Spec["levels"].([]any)
	for i, l := range levels {
		lv, _ := l.(map[string]any)
		if fmt.Sprint(lv["level"]) != fmt.Sprint(level) {
			continue
		}
		g, _ := lv["guardrails"].(map[string]any)
		ra, _ := g["requireApproval"].(string)
		var posture policy.Posture
		switch ra {
		case "all":
			posture = policy.PostureOwner
		case "security-critical-only":
			posture = policy.PostureGreen
		default:
			return "", false, fmt.Errorf("%w: AutonomyPolicy %s/%s: spec.levels[%d].guardrails.requireApproval %q: lucy never merges without a human on the fixed list; only all and security-critical-only are flown",
				ErrUnsupported, p.Metadata.Namespace, name, i, ra)
		}
		if clientConfirms {
			posture = policy.PostureClient
		}
		return posture, true, nil
	}
	return "", false, fmt.Errorf("%w: Runway %s names level %v of AutonomyPolicy %q, which has no such level", ErrInvalid, where, level, name)
}

// Effective returns the posture a Flight is flown under: its Runway's (when the Runway sets one),
// tightened by the Flight's own autonomy, which may never loosen it; or, when the Runway sets none,
// the Flight's own.
func Effective(runwayPosture policy.Posture, runwaySets bool, flight policy.Posture) (policy.Posture, error) {
	if !runwaySets {
		return flight, nil
	}
	if flight == "" {
		return runwayPosture, nil
	}
	fs, ok := strictness[flight]
	if !ok {
		return "", fmt.Errorf("%w: unknown autonomy %q", ErrInvalid, flight)
	}
	if fs < strictness[runwayPosture] {
		return "", fmt.Errorf("%w: the Flight's %s is looser than its Runway's %s", ErrLoosens, flight, runwayPosture)
	}
	return flight, nil
}
