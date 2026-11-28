// Package backlog writes lucy's Flights as the ai-sdlc spec's BacklogTask documents (the front
// matter of a backlog task, vendored as spec.BacklogTaskSchema), so a tool that reads the spec's
// backlog reads lucy's queue (LUCY-SPEC-017). lucy writes them only: reading backlog markdown as
// a source of Flights is not part of m1.
package backlog

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ar4mirez/lucy/internal/api"
	"github.com/ar4mirez/lucy/internal/spec"
)

// Statuses a BacklogTask gives a Flight the Tower does not fly; one it flies has the Tower's state.
const (
	StatusReady    = "ready"     // it has what dispatch needs, and waits for it
	StatusNotReady = "not ready" // it lacks something dispatch needs
)

// Task is a Flight as a BacklogTask:
//
//   - id: the Flight's org and name, org/name;
//   - title: its goal, or its name when it states none;
//   - status: the Tower's state when the Tower flies it, else ready or not ready;
//   - dispatchable: whether its Ready condition is True; when false, dispatchableReason says what
//     it lacks;
//   - blocked: present, with the Dispatched condition's message, when the daemon refused to
//     dispatch a Ready Flight for a reason other than dispatch being off (the takeover gate, an
//     invalid Pipeline or WorktreePool, a name in use);
//   - labels: shape:<shape>, runway:<runway> and autonomy:<posture>.
func Task(f api.FlightInfo) map[string]any {
	title := strings.TrimSpace(f.Goal)
	if title == "" {
		title = f.Name
	}
	ready := f.Ready.Status == string(spec.True)
	status := StatusNotReady
	switch {
	case f.State != "":
		status = f.State
	case ready:
		status = StatusReady
	}
	t := map[string]any{"id": f.Org + "/" + f.Name, "title": title, "status": status, "dispatchable": ready}
	if !ready && f.Ready.Message != "" {
		t["dispatchableReason"] = f.Ready.Message
	}
	if d := f.Dispatched; ready && f.State == "" && d.Status == string(spec.False) && d.Reason != "DispatchDisabled" && d.Message != "" {
		t["blocked"] = map[string]any{"reason": d.Message}
	}
	labels := []any{"shape:" + f.Shape}
	if f.Runway != "" {
		labels = append(labels, "runway:"+f.Runway)
	}
	if f.Autonomy != "" {
		labels = append(labels, "autonomy:"+f.Autonomy)
	}
	t["labels"] = labels
	return t
}

// Validate checks a BacklogTask document against the vendored schema.
func Validate(t map[string]any) error {
	// The schema validator reads JSON values: round-trip so the document holds only them.
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	errs, err := spec.Validate(spec.BacklogTaskSchema, doc)
	if err != nil {
		return err
	}
	if len(errs) > 0 {
		return fmt.Errorf("backlog: %s is not a valid BacklogTask: %v", t["id"], errs)
	}
	return nil
}
