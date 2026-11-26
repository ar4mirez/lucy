package spec

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
)

// Status lives in the event store, never in config files (LUCY-SPEC-003): loading config records
// one resource.status event per resource whose observed state changed, a resource.removed event
// for each resource no longer in config, and a config.loaded summary. The resource_status
// projection holds the current Conditions of every resource.

// StatusStream is the event stream config status events are appended to.
const StatusStream = "config"

// Bounds on what one load records.
const (
	MaxRecordedProblems = 32
	maxRecordedMessage  = 512
	maxRecordedPaths    = 8
)

// StatusEvent is the resource.status event: a resource's observed state after a load. Its
// conditions carry no times: the projection stamps lastTransitionTime from the event's own time
// when a condition's status changes, so replaying the log gives the same state.
type StatusEvent struct {
	APIVersion string      `json:"api_version"`
	Kind       string      `json:"kind"`
	Namespace  string      `json:"namespace"`
	Name       string      `json:"name"`
	Source     string      `json:"source"`
	SpecSHA256 string      `json:"spec_sha256,omitempty"` // set when the resource was admitted
	Conditions []Condition `json:"conditions"`
}

// EventType implements eventstore.Payload.
func (StatusEvent) EventType() string { return "resource.status" }

// EventVersion implements eventstore.Payload.
func (StatusEvent) EventVersion() int { return 1 }

// RemovedEvent is the resource.removed event: a resource that is no longer in config.
type RemovedEvent struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// EventType implements eventstore.Payload.
func (RemovedEvent) EventType() string { return "resource.removed" }

// EventVersion implements eventstore.Payload.
func (RemovedEvent) EventVersion() int { return 1 }

// LoadedEvent is the config.loaded event: one load of the config directory.
type LoadedEvent struct {
	Dir       string    `json:"dir"`
	Documents int       `json:"documents"` // results, including whole-file problems
	Admitted  int       `json:"admitted"`
	Refused   int       `json:"refused"`
	Problems  []Problem `json:"problems,omitempty"` // refusals with no unique identity, at most MaxRecordedProblems
	Truncated int       `json:"truncated,omitempty"`
}

// EventType implements eventstore.Payload.
func (LoadedEvent) EventType() string { return "config.loaded" }

// EventVersion implements eventstore.Payload.
func (LoadedEvent) EventVersion() int { return 1 }

// Problem is a refused document or file that has no identity of its own to carry status: a file
// that does not parse, a document without a valid name, or a duplicate.
type Problem struct {
	Source     string      `json:"source"`
	Conditions []Condition `json:"conditions"`
}

// Payloads returns the event payload types this file defines, for the daemon's registry.
func Payloads() []eventstore.Payload {
	return []eventstore.Payload{StatusEvent{}, RemovedEvent{}, LoadedEvent{}}
}

// ResourceStatus is a resource's current observed state, as the projection holds it.
type ResourceStatus struct {
	APIVersion string      `json:"api_version"`
	Kind       string      `json:"kind"`
	Namespace  string      `json:"namespace"`
	Name       string      `json:"name"`
	Source     string      `json:"source"`
	SpecSHA256 string      `json:"spec_sha256,omitempty"`
	Conditions []Condition `json:"conditions"`
	Seq        int64       `json:"seq"` // the event that last changed it
}

func (s ResourceStatus) id() string { return s.Namespace + "/" + s.Kind + "/" + s.Name }

var dnsLabel = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// identified reports whether a result names a resource well enough to carry status: a kind lucy
// knows and a name and namespace in the spec's format.
func identified(r Result) bool {
	if _, ok := LookupKind(r.APIVersion, r.Kind); !ok {
		return false
	}
	return len(r.Name) <= 253 && len(r.Namespace) <= 253 && dnsLabel.MatchString(r.Name) && dnsLabel.MatchString(r.Namespace)
}

// recorded returns the conditions as the log keeps them: messages bounded, and a schema
// violation reduced to the paths it was found at. Violation messages can quote values from the
// file, and a misplaced value could be a secret, so those stay in the CLI's output only.
func recorded(r Result) []Condition {
	out := make([]Condition, 0, len(r.Conditions))
	for _, c := range r.Conditions {
		c.LastTransitionTime = ""
		if c.Reason == ReasonSchemaViolation && len(r.Errors) > 0 {
			var paths []string
			for i, e := range r.Errors {
				if i == maxRecordedPaths {
					paths = append(paths, fmt.Sprintf("and %d more", len(r.Errors)-i))
					break
				}
				p := e.Path
				if p == "" {
					p = "/"
				}
				paths = append(paths, p)
			}
			c.Message = fmt.Sprintf("%d schema violations at %s; run `lucy config validate` for details", len(r.Errors), strings.Join(paths, ", "))
		}
		if len(c.Message) > maxRecordedMessage {
			c.Message = c.Message[:maxRecordedMessage] + "..."
		}
		out = append(out, c)
	}
	return out
}

// plan works out what a load records: a status per identified resource (the admitted one when
// several share an identity, else the first), everything else as a problem.
func plan(set Set) (map[string]StatusEvent, []Problem, error) {
	statuses := map[string]StatusEvent{}
	var problems []Problem
	for _, r := range set.Results {
		if !identified(r) {
			problems = append(problems, Problem{Source: r.Source.String(), Conditions: recorded(r)})
			continue
		}
		ev := StatusEvent{
			APIVersion: r.APIVersion, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name,
			Source: r.Source.String(), Conditions: recorded(r),
		}
		if r.Resource != nil {
			sum, err := r.Resource.SpecSHA256()
			if err != nil {
				return nil, nil, err
			}
			ev.SpecSHA256 = sum
		}
		id := r.Namespace + "/" + r.Kind + "/" + r.Name
		prev, seen := statuses[id]
		switch {
		case !seen:
			statuses[id] = ev
		case prev.SpecSHA256 == "" && ev.SpecSHA256 != "":
			statuses[id] = ev
			problems = append(problems, Problem{Source: prev.Source, Conditions: prev.Conditions})
		default:
			problems = append(problems, Problem{Source: ev.Source, Conditions: ev.Conditions})
		}
	}
	return statuses, problems, nil
}

// RecordSummary says what Record appended.
type RecordSummary struct {
	Changed int // resource.status events
	Removed int // resource.removed events
}

// Record appends a load's outcome to the event store, in one transaction: a resource.status event
// for each resource whose observed state differs from the projection's, a resource.removed event
// for each resource the projection has and config no longer does, and a config.loaded summary.
func Record(ctx context.Context, store *eventstore.Store, actor string, set Set) (RecordSummary, error) {
	var sum RecordSummary
	desired, problems, err := plan(set)
	if err != nil {
		return sum, err
	}
	current, err := Statuses(ctx, store)
	if err != nil {
		return sum, err
	}
	have := map[string]ResourceStatus{}
	for _, c := range current {
		have[c.id()] = c
	}
	var payloads []eventstore.Payload
	ids := make([]string, 0, len(desired))
	for id := range desired {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		ev := desired[id]
		if c, ok := have[id]; ok && sameStatus(c, ev) {
			continue
		}
		payloads = append(payloads, ev)
		sum.Changed++
	}
	for _, c := range current {
		if _, ok := desired[c.id()]; !ok {
			payloads = append(payloads, RemovedEvent{Kind: c.Kind, Namespace: c.Namespace, Name: c.Name})
			sum.Removed++
		}
	}
	loaded := LoadedEvent{Dir: set.Dir, Documents: len(set.Results), Refused: set.Invalid()}
	loaded.Admitted = loaded.Documents - loaded.Refused
	if len(problems) > MaxRecordedProblems {
		loaded.Truncated = len(problems) - MaxRecordedProblems
		problems = problems[:MaxRecordedProblems]
	}
	loaded.Problems = problems
	payloads = append(payloads, loaded)
	err = store.Update(ctx, func(tx *eventstore.Tx) error {
		for chunk := range slices.Chunk(payloads, eventstore.MaxEventsPerAppend) {
			if _, err := tx.Append(ctx, StatusStream, eventstore.AnyVersion, actor, chunk...); err != nil {
				return err
			}
		}
		return nil
	})
	return sum, err
}

func sameStatus(c ResourceStatus, ev StatusEvent) bool {
	if c.APIVersion != ev.APIVersion || c.Source != ev.Source || c.SpecSHA256 != ev.SpecSHA256 || len(c.Conditions) != len(ev.Conditions) {
		return false
	}
	for i := range c.Conditions {
		a, b := c.Conditions[i], ev.Conditions[i]
		if a.Type != b.Type || a.Status != b.Status || a.Reason != b.Reason || a.Message != b.Message {
			return false
		}
	}
	return true
}

// Statuses returns every resource's current status from the projection, ordered by namespace,
// kind and name.
func Statuses(ctx context.Context, store *eventstore.Store) ([]ResourceStatus, error) {
	rows, err := store.QueryContext(ctx, `SELECT api_version, kind, namespace, name, source, spec_sha256, conditions, seq
		FROM resource_status ORDER BY namespace, kind, name`)
	if err != nil {
		return nil, fmt.Errorf("spec: read resource status: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []ResourceStatus
	for rows.Next() {
		var s ResourceStatus
		var conds string
		if err := rows.Scan(&s.APIVersion, &s.Kind, &s.Namespace, &s.Name, &s.Source, &s.SpecSHA256, &conds, &s.Seq); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(conds), &s.Conditions); err != nil {
			return nil, fmt.Errorf("spec: resource status %s: %w", s.id(), err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// StatusProjector maintains the resource_status table from resource.status and resource.removed
// events.
type StatusProjector struct{}

// Name implements eventstore.Projector.
func (StatusProjector) Name() string { return "resource_status" }

// Version implements eventstore.Projector.
func (StatusProjector) Version() int { return 1 }

// Reset implements eventstore.Projector.
func (StatusProjector) Reset(ctx context.Context, tx eventstore.Execer) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS resource_status`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE TABLE resource_status (
			namespace   TEXT NOT NULL,
			kind        TEXT NOT NULL,
			name        TEXT NOT NULL,
			api_version TEXT NOT NULL,
			source      TEXT NOT NULL,
			spec_sha256 TEXT NOT NULL,
			conditions  TEXT NOT NULL,
			seq         INTEGER NOT NULL,
			PRIMARY KEY (namespace, kind, name)
		) STRICT`)
	return err
}

// Apply implements eventstore.Projector.
func (StatusProjector) Apply(ctx context.Context, tx eventstore.Execer, e eventstore.Event) error {
	switch e.Type {
	case "resource.status":
		var ev StatusEvent
		if err := strictJSON(e.Payload, &ev); err != nil {
			return err
		}
		var prevJSON string
		err := tx.QueryRowContext(ctx, `SELECT conditions FROM resource_status WHERE namespace = ? AND kind = ? AND name = ?`,
			ev.Namespace, ev.Kind, ev.Name).Scan(&prevJSON)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var prev []Condition
		if prevJSON != "" {
			if err := json.Unmarshal([]byte(prevJSON), &prev); err != nil {
				return err
			}
		}
		stamp := e.Time.UTC().Format(time.RFC3339)
		conds := make([]Condition, len(ev.Conditions))
		for i, c := range ev.Conditions {
			c.LastTransitionTime = stamp
			for _, p := range prev {
				if p.Type == c.Type && p.Status == c.Status && p.LastTransitionTime != "" {
					c.LastTransitionTime = p.LastTransitionTime
				}
			}
			conds[i] = c
		}
		data, err := json.Marshal(conds)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO resource_status (namespace, kind, name, api_version, source, spec_sha256, conditions, seq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (namespace, kind, name) DO UPDATE SET api_version = excluded.api_version, source = excluded.source,
				spec_sha256 = excluded.spec_sha256, conditions = excluded.conditions, seq = excluded.seq`,
			ev.Namespace, ev.Kind, ev.Name, ev.APIVersion, ev.Source, ev.SpecSHA256, string(data), e.Seq)
		return err
	case "resource.removed":
		var ev RemovedEvent
		if err := strictJSON(e.Payload, &ev); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM resource_status WHERE namespace = ? AND kind = ? AND name = ?`, ev.Namespace, ev.Kind, ev.Name)
		return err
	}
	return nil
}

func strictJSON(data []byte, v any) error {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
