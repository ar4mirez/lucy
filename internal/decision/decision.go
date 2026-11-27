// Package decision is lucy's decision catalog (LUCY-SPEC-013): every decision that waits on the
// owner, projected from the event log into the spec's Decision resource (RFC-0035,
// decision.v1.schema.json). A decision opens when the log records something only the owner may
// settle, and it closes only on the owner's own recorded word: an answer, an approval or a
// confirmation. Nothing expires it into an answer, and no agent can close it; a newer request or
// the flight's end supersedes or archives it.
//
// The kinds:
//
//   - question: a Pilot asked the owner (task.pilot_asked); the owner answers it with lucy answer
//     (task.pilot_answered).
//   - merge: a merge holds for the owner's approval (task.clearance with outcome holding); the owner
//     approves it with lucy approve (task.approved).
//   - container: an operation on an org's container holds until the owner confirms the sessions it
//     stops (sandbox.requested, needing the owner); lucy sandbox confirm (sandbox.confirmed).
package decision

import (
	"fmt"
	"time"
)

// Kinds of decision.
const (
	KindQuestion  = "question"
	KindMerge     = "merge"
	KindContainer = "container"
)

// Lifecycles, the spec's (RFC-0035 4.2): lucy uses open, answered, superseded and archived.
const (
	Open       = "open"
	Answered   = "answered"
	Superseded = "superseded"
	Archived   = "archived" // the flight it was about ended before the owner decided
)

// Row is a decision as the catalog's projection holds it.
type Row struct {
	Seq          int64 // the event that opened it; its id is DEC-<Seq>
	Kind         string
	Org          string
	Subject      string // the flight, or the container operation
	Ref          int64  // the policy decision a merge or container decision is about
	Summary      string
	Body         string // untrusted text where the opener carried some (a Pilot's question)
	Lifecycle    string
	AnsweredBy   string
	AnsweredAt   time.Time
	SupersededBy int64
	Opened       time.Time
	Updated      time.Time
}

// ID is the decision's id in the spec's form, DEC-NNNN.
func (r Row) ID() string { return fmt.Sprintf("DEC-%04d", r.Seq) }

// Scope is the spec's free-form scope: flight:<org>/<flight> or container:<org>.
func (r Row) Scope() string {
	if r.Kind == KindContainer {
		return "container:" + r.Org
	}
	return "flight:" + r.Org + "/" + r.Subject
}

// Option is one way the owner may settle a decision.
type Option struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// Options are the ways to settle a decision of kind k about subject: each names the owner's
// command that does it.
func Options(k, org, subject string) []Option {
	switch k {
	case KindQuestion:
		return []Option{{ID: "answer", Description: "answer the Pilot: lucy answer " + subject + " <answer>"}}
	case KindMerge:
		return []Option{{ID: "approve", Description: "approve the merge as the owner: lucy approve " + subject}}
	default:
		return []Option{{ID: "confirm", Description: "confirm stopping the sessions it names: lucy sandbox confirm " + org + " " + subject + " <decision>"}}
	}
}

// answeredOption is the option an answered decision of kind k was settled with.
func answeredOption(k string) string {
	return map[string]string{KindQuestion: "answer", KindMerge: "approve", KindContainer: "confirm"}[k]
}

// Resource is a decision in the spec's Decision shape (decision.v1.schema.json).
type Resource struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
	Status     Status   `json:"status"`
}

// Metadata is a Decision's metadata.
type Metadata struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Scope   string `json:"scope"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// Spec is a Decision's spec. lucy never auto-applies a decision, so every one is irreversible in
// the spec's sense: no override window lets it settle itself, and it has no fallback option.
type Spec struct {
	Summary    string   `json:"summary"`
	Body       string   `json:"body,omitempty"`
	Reversible bool     `json:"reversible"`
	Options    []Option `json:"options"`
	ContextRef string   `json:"contextRef,omitempty"`
}

// Status is a Decision's status.
type Status struct {
	Lifecycle        string  `json:"lifecycle"`
	AnsweredOptionID *string `json:"answeredOptionId"`
	AnsweredBy       *string `json:"answeredBy"`
	AnsweredAt       *string `json:"answeredAt"`
	SupersededBy     *string `json:"supersededBy"`
	Routing          Routing `json:"routing"`
}

// Routing says who may settle a decision: the owner, and never a model.
type Routing struct {
	AssignedActor  string `json:"assignedActor"`
	ActorRationale string `json:"actorRationale"`
	LLMEligible    bool   `json:"llmEligible"`
}

// Resource returns the decision in the spec's Decision shape.
func (r Row) Resource() Resource {
	src := "emergent-finding"
	if r.Kind == KindQuestion {
		src = "subagent-escalation"
	}
	ts := func(t time.Time) string { return t.UTC().Format(time.RFC3339) }
	res := Resource{
		APIVersion: "ai-sdlc.io/v1alpha1",
		Kind:       "Decision",
		Metadata:   Metadata{ID: r.ID(), Source: src, Scope: r.Scope(), Created: ts(r.Opened), Updated: ts(r.Updated)},
		Spec:       Spec{Summary: r.Summary, Body: r.Body, Options: Options(r.Kind, r.Org, r.Subject)},
		Status: Status{Lifecycle: r.Lifecycle, Routing: Routing{AssignedActor: "owner", LLMEligible: false,
			ActorRationale: "only the owner's own recorded word closes a decision"}},
	}
	if r.Ref > 0 {
		res.Spec.ContextRef = fmt.Sprintf("policy.decision:%d", r.Ref)
	}
	if r.Lifecycle == Answered {
		opt, by, at := answeredOption(r.Kind), r.AnsweredBy, ts(r.AnsweredAt)
		res.Status.AnsweredOptionID, res.Status.AnsweredBy, res.Status.AnsweredAt = &opt, &by, &at
	}
	if r.Lifecycle == Superseded && r.SupersededBy > 0 {
		id := fmt.Sprintf("DEC-%04d", r.SupersededBy)
		res.Status.SupersededBy = &id
	}
	return res
}
