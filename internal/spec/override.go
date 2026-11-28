package spec

import (
	"fmt"
	"strings"
)

// ConditionOverride is the condition an admitted QualityGate carries when a gate of it names who
// may override it: lucy lets only the owner override a gate, whatever the gate names
// (LUCY-SPEC-010, "the owner is the only override role"). The document is accepted as the spec
// writes it, so Core conformance holds; the condition says what lucy does instead.
// TODO(LUCY-SPEC-010): the validation slice (11) takes a gate's override only as the owner's
// recorded word, with the justification the gate requires.
const ConditionOverride = "Override"

// ReasonOverrideIsOwner is the Override condition's reason.
const ReasonOverrideIsOwner = "OverrideIsOwner"

// OwnerRole is the one role lucy lets override a gate.
const OwnerRole = "owner"

// GateOverrides returns, for a QualityGate, each override it grants to anyone but the owner: a
// gate's override.requiredRole, and a visual-regression rule's override.approvers.
func GateOverrides(r Resource) []string {
	var out []string
	gates, _ := r.Spec["gates"].([]any)
	for i, g := range gates {
		gate, _ := g.(map[string]any)
		name, _ := gate["name"].(string)
		if name == "" {
			name = fmt.Sprintf("gates[%d]", i)
		}
		if o, ok := gate["override"].(map[string]any); ok {
			if role, _ := o["requiredRole"].(string); role != "" && role != OwnerRole {
				out = append(out, fmt.Sprintf("gate %s: override.requiredRole %s", name, role))
			}
		}
		rule, _ := gate["rule"].(map[string]any)
		if o, ok := rule["override"].(map[string]any); ok {
			approvers, _ := o["approvers"].([]any)
			for _, a := range approvers {
				if s, _ := a.(string); s != OwnerRole {
					out = append(out, fmt.Sprintf("gate %s: rule.override.approvers %v", name, a))
				}
			}
		}
	}
	return out
}

// markOverrides adds an Override condition to every admitted QualityGate that grants an override
// to anyone but the owner.
func markOverrides(results []Result) {
	for i := range results {
		r := results[i].Resource
		if r == nil || r.APIVersion != CoreAPIVersion || r.Kind != "QualityGate" {
			continue
		}
		named := GateOverrides(*r)
		if len(named) == 0 {
			continue
		}
		results[i].Conditions = append(results[i].Conditions, Condition{Type: ConditionOverride, Status: True, Reason: ReasonOverrideIsOwner,
			Message: clip("lucy lets only the owner override these gates, whatever they name; not acted on: " + strings.Join(named, "; "))})
	}
}
