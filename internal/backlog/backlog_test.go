package backlog

import (
	"slices"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/api"
)

func cond(status, reason, msg string) api.ConditionInfo {
	return api.ConditionInfo{Status: status, Reason: reason, Message: msg}
}

// Every Flight is written as a BacklogTask the vendored schema accepts, whatever state it is in,
// and the fields say what the Flight's conditions and the Tower say (LUCY-SPEC-017).
func TestTask(t *testing.T) {
	for _, c := range []struct {
		name   string
		f      api.FlightInfo
		status string
		disp   bool
		why    string // dispatchableReason, when not dispatchable
		block  string // blocked.reason, when blocked
	}{
		{"flying", api.FlightInfo{Org: "acme", Name: "fix-login", Goal: "fix the login page", Shape: "ship", Runway: "web", Autonomy: "owner",
			State: "holding", Ready: cond("True", "Ready", ""), Dispatched: cond("True", "Dispatched", "the Tower flies it")}, "holding", true, "", ""},
		{"ready, dispatch off", api.FlightInfo{Org: "acme", Name: "a", Goal: "g", Shape: "ship",
			Ready: cond("True", "Ready", ""), Dispatched: cond("False", "DispatchDisabled", "the daemon dispatches no Flight unless started with --dispatch")}, StatusReady, true, "", ""},
		{"ready, held", api.FlightInfo{Org: "acme", Name: "b", Goal: "g", Shape: "ship",
			Ready: cond("True", "Ready", ""), Dispatched: cond("False", "PipelineInvalid", "Pipeline acme/ship is invalid")}, StatusReady, true, "", "Pipeline acme/ship is invalid"},
		{"not ready", api.FlightInfo{Org: "acme", Name: "draft", Shape: "ship",
			Ready: cond("False", "NotReady", "not dispatched until it has a goal")}, StatusNotReady, false, "not dispatched until it has a goal", ""},
	} {
		task := Task(c.f)
		if err := Validate(task); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if task["id"] != c.f.Org+"/"+c.f.Name || task["status"] != c.status || task["dispatchable"] != c.disp {
			t.Errorf("%s: %+v", c.name, task)
		}
		if got, _ := task["dispatchableReason"].(string); got != c.why {
			t.Errorf("%s: dispatchableReason %q, want %q", c.name, got, c.why)
		}
		b, _ := task["blocked"].(map[string]any)
		if got, _ := b["reason"].(string); got != c.block {
			t.Errorf("%s: blocked %v, want %q", c.name, b, c.block)
		}
		if c.f.Goal == "" && task["title"] != c.f.Name {
			t.Errorf("%s: a Flight with no goal is titled %v", c.name, task["title"])
		}
		if labels, _ := task["labels"].([]any); !slices.Contains(labels, any("shape:ship")) {
			t.Errorf("%s: labels %v", c.name, labels)
		}
	}
}

// The check is the vendored schema's: a document without what BacklogTask requires is refused.
func TestValidateUsesTheSchema(t *testing.T) {
	for _, doc := range []map[string]any{
		{"id": "acme/x", "status": "ready"},                                            // no title
		{"id": "acme/x", "title": "", "status": "ready"},                               // an empty title
		{"id": "acme/x", "title": "t", "status": "ready", "blocked": map[string]any{}}, // blocked without a reason
	} {
		if err := Validate(doc); err == nil || !strings.Contains(err.Error(), "not a valid BacklogTask") {
			t.Errorf("%v: %v", doc, err)
		}
	}
}
