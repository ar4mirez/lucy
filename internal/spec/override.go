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

// maxOverrideMessage bounds the Override condition's message, well within maxRecordedMessage, so
// the recorded status and the daemon's warning carry the same complete list.
const maxOverrideMessage = 400

// GateOverrides returns, for a QualityGate, one entry per gate that grants an override to anyone
// but the owner: the gate's name with its override.requiredRole, and a visual-regression rule's
// override.approvers.
func GateOverrides(r Resource) []string {
	var out []string
	gates, _ := r.Spec["gates"].([]any)
	for i, g := range gates {
		gate, _ := g.(map[string]any)
		name, _ := gate["name"].(string)
		if name == "" {
			name = fmt.Sprintf("gates[%d]", i)
		}
		var named []string
		if o, ok := gate["override"].(map[string]any); ok {
			if role, _ := o["requiredRole"].(string); role != "" && role != OwnerRole {
				named = append(named, "role "+role)
			}
		}
		rule, _ := gate["rule"].(map[string]any)
		if o, ok := rule["override"].(map[string]any); ok {
			approvers, _ := o["approvers"].([]any)
			var others []string
			for _, a := range approvers {
				if s, _ := a.(string); s != OwnerRole {
					others = append(others, fmt.Sprint(a))
				}
			}
			if len(others) > 0 {
				named = append(named, "approvers "+strings.Join(others, ", "))
			}
		}
		if len(named) > 0 {
			out = append(out, name+" ("+strings.Join(named, "; ")+")")
		}
	}
	return out
}

// overrideMessage lists the entries GateOverrides names, up to maxOverrideMessage, ending with
// "and N more" when the rest do not fit.
func overrideMessage(named []string) string {
	const prefix = "only the owner may override a gate; not acted on: "
	msg := prefix
	for i, e := range named {
		sep := ""
		if i > 0 {
			sep = ", "
		}
		rest := len(named) - i - 1
		reserve := 0
		if rest > 0 {
			reserve = len(fmt.Sprintf(", and %d more", rest))
		}
		if len(msg)+len(sep)+len(e)+reserve > maxOverrideMessage {
			if i > 0 {
				msg += ", "
			}
			return msg + fmt.Sprintf("and %d more", len(named)-i)
		}
		msg += sep + e
	}
	return msg
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
			Message: overrideMessage(named)})
	}
}
