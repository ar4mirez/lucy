package spec

import (
	"fmt"
	"strings"
)

// ConditionReady is the condition every admitted Flight carries after a load: whether it has what
// dispatch needs (LUCY-SPEC-012). A Flight that is not ready is admitted, so its status says what
// it lacks, but it is never dispatched.
const ConditionReady = "Ready"

// Reasons a Ready condition gives. Unsupported is a Flight that has everything dispatch needs
// but that lucy does not fly yet: a research Flight, until the Tower flies reports. It is not
// Ready, so the condition never claims a Flight will be dispatched when it will not.
const (
	ReasonReady       = "Ready"
	ReasonNotReady    = "NotReady"
	ReasonUnsupported = "Unsupported"
)

// FlightReadiness returns what a Flight lacks before it may be dispatched, empty when it is
// ready: a goal; a delivery path, that is a runway that is an admitted Runway of the same
// namespace (runways holds them, by namespace/name) and a delivery that fits its shape
// (pull-request for ship, report for research); an autonomy posture, stated; and an acceptance
// test.
func FlightReadiness(r Resource, runways map[string]bool) []string {
	str := func(k string) string { s, _ := r.Spec[k].(string); return strings.TrimSpace(s) }
	var missing []string
	if str("goal") == "" {
		missing = append(missing, "a goal")
	}
	switch rw := str("runway"); {
	case rw == "":
		missing = append(missing, "a delivery path: a runway")
	case !runways[r.Metadata.Namespace+"/"+rw]:
		missing = append(missing, fmt.Sprintf("a delivery path: a runway %q admitted in namespace %q", rw, r.Metadata.Namespace))
	}
	shape := str("shape")
	if shape == "" {
		shape = "ship"
	}
	want := map[string]string{"ship": "pull-request", "research": "report"}[shape]
	switch d := str("delivery"); {
	case d == "":
		missing = append(missing, "a delivery path: a delivery ("+want+")")
	case d != want:
		missing = append(missing, fmt.Sprintf("a delivery path that fits a %s Flight (%s, not %s)", shape, want, d))
	}
	if str("autonomy") == "" {
		missing = append(missing, "an autonomy posture (owner, green or client)")
	}
	if str("acceptance") == "" {
		missing = append(missing, "an acceptance test")
	}
	return missing
}

// markReadiness adds a Ready condition to every admitted Flight in results.
func markReadiness(results []Result) {
	runways := map[string]bool{}
	for _, r := range results {
		if r.Resource != nil && r.Resource.APIVersion == LucyAPIVersion && r.Resource.Kind == "Runway" {
			runways[r.Resource.Metadata.Namespace+"/"+r.Resource.Metadata.Name] = true
		}
	}
	for i := range results {
		r := results[i].Resource
		if r == nil || r.APIVersion != LucyAPIVersion || r.Kind != "Flight" {
			continue
		}
		c := Condition{Type: ConditionReady, Status: True, Reason: ReasonReady, Message: "it has a goal, a delivery path, an autonomy posture and an acceptance test"}
		if missing := FlightReadiness(*r, runways); len(missing) > 0 {
			c = Condition{Type: ConditionReady, Status: False, Reason: ReasonNotReady, Message: clip("not dispatched until it has " + strings.Join(missing, ", "))}
		} else if shape, _ := r.Spec["shape"].(string); shape == "research" {
			// TODO(LUCY-SPEC-012): fly research Flights; the Tower's readiness and this one move together.
			c = Condition{Type: ConditionReady, Status: False, Reason: ReasonUnsupported, Message: "it has everything dispatch needs, but lucy does not fly research Flights yet"}
		}
		results[i].Conditions = append(results[i].Conditions, c)
	}
}
