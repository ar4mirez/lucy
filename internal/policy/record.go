package policy

import "github.com/ar4mirez/lucy/internal/pathguard"

// Stream is the event stream decision records are appended to.
const Stream = "policy"

// DecisionRecord is the policy.decision event: one decision, with the request it answered, the
// facts it rested on and the rule set that made it (LUCY-POL-003). The daemon appends it before it
// returns the decision; only a recorded decision authorizes anything (LUCY-POL-002). It holds names
// and references, never secrets or the text of an input.
type DecisionRecord struct {
	Outcome        Outcome       `json:"outcome"`
	Rule           string        `json:"rule"`
	Reason         string        `json:"reason"`
	RuleSetVersion int           `json:"ruleset_version"`
	RuleSetSHA256  string        `json:"ruleset_sha256"`
	ActionSHA256   string        `json:"action_sha256"`
	FactsSHA256    string        `json:"facts_sha256"`
	Tainted        bool          `json:"tainted"`
	Approval       string        `json:"approval,omitempty"`
	Actor          Actor         `json:"actor"`
	Kind           Kind          `json:"kind"`
	Target         Target        `json:"target"`
	Inputs         []InputRecord `json:"inputs"`
	Facts          FactsRecord   `json:"facts"`
}

// InputRecord is an input as recorded, with the trust the engine gave it.
type InputRecord struct {
	Source  Source `json:"source"`
	Ref     string `json:"ref,omitempty"`
	Trusted bool   `json:"trusted"`
}

// FactsRecord is the part of the facts a reader needs to understand a decision.
type FactsRecord struct {
	Posture            Posture          `json:"posture,omitempty"`
	Checks             CheckState       `json:"checks,omitempty"`
	RequiredUnreported bool             `json:"required_unreported,omitempty"`
	UnintendedClosing  bool             `json:"unintended_closing,omitempty"`
	Exceptions         []Exception      `json:"exceptions,omitempty"`
	HeadSHA            string           `json:"head_sha,omitempty"`
	BaseRef            string           `json:"base_ref,omitempty"`
	BaseSHA            string           `json:"base_sha,omitempty"`
	CredentialScope    string           `json:"credential_scope,omitempty"`
	DefaultBranch      string           `json:"default_branch,omitempty"`
	Home               string           `json:"home,omitempty"`
	Roots              []pathguard.Root `json:"roots,omitempty"`
	ResolvedPath       string           `json:"resolved_path,omitempty"`
	Approvals          []string         `json:"approvals,omitempty"` // decision ids of the approvals offered
}

// EventType implements eventstore.Payload.
func (DecisionRecord) EventType() string { return "policy.decision" }

// EventVersion implements eventstore.Payload.
func (DecisionRecord) EventVersion() int { return 1 }

// NewRecord builds the record of decision d for req under facts.
func NewRecord(req Request, facts Facts, d Decision) DecisionRecord {
	rec := DecisionRecord{
		Outcome: d.Outcome, Rule: d.Rule, Reason: d.Reason, RuleSetVersion: d.RuleSetVersion,
		RuleSetSHA256: d.RuleSetSHA256, ActionSHA256: d.ActionSHA256, FactsSHA256: d.FactsSHA256, Tainted: d.Tainted,
		Approval: d.Approval, Actor: req.Actor, Kind: req.Kind, Target: req.Target,
		Inputs: []InputRecord{},
		Facts: FactsRecord{
			Posture: facts.Posture, Checks: facts.Checks, RequiredUnreported: facts.RequiredUnreported,
			UnintendedClosing: facts.UnintendedClosing, Exceptions: facts.Exceptions,
			HeadSHA: facts.HeadSHA, BaseRef: facts.BaseRef, BaseSHA: facts.BaseSHA,
			CredentialScope: facts.CredentialScope, DefaultBranch: facts.DefaultBranch,
			Home: facts.Home, Roots: facts.Roots, ResolvedPath: facts.ResolvedPath,
		},
	}
	for _, in := range req.Inputs {
		rec.Inputs = append(rec.Inputs, InputRecord{Source: in.Source, Ref: in.Ref, Trusted: Trusted(in.Source)})
	}
	for _, a := range facts.Approvals {
		rec.Facts.Approvals = append(rec.Facts.Approvals, a.Decision)
	}
	return rec
}
