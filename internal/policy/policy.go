// Package policy is lucy's policy engine: "LLM proposes, policy disposes" (adversarial review
// P0-2). Models and agents only request actions; deterministic Go rules decide every privileged
// one (LUCY-POL-001).
//
// The engine is pure and side-effect free: Evaluate reads only its arguments, never the clock,
// the filesystem, the network or any global state, and it returns the same Decision for the same
// input. The daemon gathers the facts a decision needs (the merge posture, check results, captain
// approvals, symlink-resolved paths) from trusted sources and records every decision as an event
// before anything acts on it (LUCY-POL-002, LUCY-POL-003).
//
// The rules (rules.go) are evaluated in a fixed order and the first one with an opinion decides.
// The default is deny. A decision is allow, deny or require-captain, and always names the rule that
// made it, a reason, and the rule set's version and SHA-256, so every decision can be traced to the
// exact rules that produced it (LUCY-POL-006). Any evaluation error, including a panic, is a deny.
//
// Outside text never raises privileges (LUCY-POL-004): every input to a request carries the source
// it came from, the engine (not the requester) decides whether that source is trusted, and a
// privileged request whose inputs are all untrusted is denied. Approvals and confirmations are
// never read from inputs; they arrive only as Facts, from decision records made through the API.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ar4mirez/lucy/internal/pathguard"
)

// Role is what an actor is in lucy's agent model (AGENTIC.md section 2), established by the
// daemon from the caller's authenticated identity, never claimed by the requester.
type Role string

// Roles.
const (
	RoleCaptain    Role = "captain"    // the human who decides; not bound to one org
	RoleDaemon     Role = "daemon"     // lucy's own deterministic code (pool GC, cleanup)
	RoleSupervisor Role = "supervisor" // an org's supervisor session
	RoleWorker     Role = "worker"     // a ship worker in one worktree
	RoleScout      Role = "scout"      // research only; ships nothing
	RoleReviewer   Role = "reviewer"   // an independent reviewer
	RoleGateway    Role = "gateway"    // an org's credential gateway process
)

// Kind is the action requested.
type Kind string

// Action kinds. Every kind except read and write is privileged.
const (
	KindRead           Kind = "read"            // read an org's objects
	KindWrite          Kind = "write"           // change files in a project worktree
	KindMerge          Kind = "merge"           // merge a pull request
	KindDelete         Kind = "delete"          // delete a filesystem path
	KindForcePush      Kind = "force_push"      // push a branch with --force
	KindBranchDelete   Kind = "branch_delete"   // delete a branch
	KindHistoryRewrite Kind = "history_rewrite" // rebase, amend or filter published history
	KindCredentialUse  Kind = "credential_use"  // use a stored credential
)

// Source is where an input to a request came from. It is set by lucy where the text enters the
// system (an adapter, the API), never by the model that read it.
type Source string

// Sources. trust.go says which are trusted.
const (
	SourceCaptain     Source = "captain"      // the captain's own words, through the API
	SourceConfig      Source = "config"       // the captain's spec resources on disk
	SourceDaemon      Source = "daemon"       // lucy's own deterministic code
	SourceTask        Source = "task"         // a task record made through the API
	SourceAgent       Source = "agent"        // a model's own output
	SourceToolOutput  Source = "tool_output"  // output of a tool or command an agent ran
	SourceRepoContent Source = "repo_content" // files in a repository
	SourceIssue       Source = "issue"        // an issue's title or body
	SourcePRBody      Source = "pr_body"      // a pull request's title or body
	SourcePRComment   Source = "pr_comment"   // a pull request or review comment
	SourceWeb         Source = "web"          // a fetched web page
	SourceEmail       Source = "email"        // an email
)

// Actor is who requests an action.
type Actor struct {
	ID   string `json:"id"`            // the actor's name in the event log
	Role Role   `json:"role"`          // set by the daemon from the authenticated caller
	Org  string `json:"org,omitempty"` // the actor's org; empty for the captain and the daemon
}

// Target is what an action is on.
type Target struct {
	Org      string `json:"org"`                // the org the object belongs to
	Project  string `json:"project,omitempty"`  // the project (repository), when there is one
	Resource string `json:"resource,omitempty"` // a PR ("pr/12"), a path, a branch or a credential name
}

// Input is one thing a request is based on: its source and a reference to it (an event, a URL, a
// comment id). It never carries the text itself.
type Input struct {
	Source Source `json:"source"`
	Ref    string `json:"ref,omitempty"`
}

// Request is one action an actor asks for, with the provenance of every input behind it.
type Request struct {
	Actor  Actor   `json:"actor"`
	Kind   Kind    `json:"kind"`
	Target Target  `json:"target"`
	Inputs []Input `json:"inputs,omitempty"`
}

// Posture is a project's merge posture (LUCY-POL-005, AGENTIC.md section 4).
type Posture string

// Postures. The empty posture is the default, captain.
const (
	PostureCaptain Posture = "captain" // merges only on the captain's explicit word
	PostureGreen   Posture = "green"   // green PRs merge, except anything on the fixed list
	PostureClient  Posture = "client"  // merges only after the client confirms and the captain relays it
)

// CheckState summarizes a pull request's required checks, read from the forge's API.
type CheckState string

// Check states. The empty state means unknown, which never merges.
const (
	ChecksGreen   CheckState = "green"
	ChecksRed     CheckState = "red"
	ChecksPending CheckState = "pending"
)

// Exception is a category on the fixed list that always needs the captain (AGENTIC.md section 4).
type Exception string

// Fixed-list categories.
const (
	ExceptionDestructive   Exception = "destructive"
	ExceptionIrreversible  Exception = "irreversible"
	ExceptionSecurity      Exception = "security"
	ExceptionProduction    Exception = "production"
	ExceptionBilling       Exception = "billing"
	ExceptionCredentials   Exception = "credentials"
	ExceptionDataMigration Exception = "data_migration"
	ExceptionCrossOrg      Exception = "cross_org"
	ExceptionJudgement     Exception = "judgement"
)

// ApprovalBy says whose decision an approval records.
type ApprovalBy string

// Approval kinds.
const (
	ByCaptain          ApprovalBy = "captain"            // the captain approved this action
	ByClientViaCaptain ApprovalBy = "client_via_captain" // the captain relayed the client's confirmation
)

// Approval is a decision record made through the API that approves one exact action under the
// facts the approver saw. It binds to the action's digest, so an approval for one action can never
// be used for another, and to the digest of the bound facts, so it lapses when they change (a new
// push to the pull request, a path that now resolves elsewhere).
type Approval struct {
	Decision     string     `json:"decision"`      // the decision record's id
	By           ApprovalBy `json:"by"`            // whose decision it records
	ActionSHA256 string     `json:"action_sha256"` // ActionDigest of the approved request
	FactsSHA256  string     `json:"facts_sha256"`  // FactsDigest of the facts the approver saw
}

// Facts are what the daemon knows about a request, gathered from trusted sources: config, the
// forge's authenticated API, lucy's own records and the filesystem. A requester never supplies
// them. A missing fact is the safe value: unknown checks never merge, no roots delete nothing.
type Facts struct {
	Posture            Posture          `json:"posture,omitempty"`
	Checks             CheckState       `json:"checks,omitempty"`
	RequiredUnreported bool             `json:"required_unreported,omitempty"` // a required check has not reported
	UnintendedClosing  bool             `json:"unintended_closing,omitempty"`  // the PR closes issues the task did not intend
	Exceptions         []Exception      `json:"exceptions,omitempty"`          // fixed-list categories the change touches
	HeadSHA            string           `json:"head_sha,omitempty"`            // the pull request's head commit
	BaseRef            string           `json:"base_ref,omitempty"`            // the pull request's base branch
	BaseSHA            string           `json:"base_sha,omitempty"`            // the base branch's commit
	CredentialScope    string           `json:"credential_scope,omitempty"`    // the scope the credential grants
	DefaultBranch      string           `json:"default_branch,omitempty"`      // the project's default branch
	Home               string           `json:"home,omitempty"`                // the user's resolved home directory
	Roots              []pathguard.Root `json:"roots,omitempty"`               // resolved roots this request may delete in
	ResolvedPath       string           `json:"resolved_path,omitempty"`       // the delete target after symlink resolution
	Approvals          []Approval       `json:"approvals,omitempty"`
}

// Outcome is what a decision says.
type Outcome string

// Outcomes.
const (
	Allow          Outcome = "allow"
	Deny           Outcome = "deny"
	RequireCaptain Outcome = "require-captain" // allowed only once the captain approves this exact action
)

// Decision is the engine's answer.
type Decision struct {
	Outcome        Outcome  `json:"outcome"`
	Rule           string   `json:"rule"`   // the rule that decided
	Reason         string   `json:"reason"` // why, in one line
	RuleSetVersion int      `json:"ruleset_version"`
	RuleSetSHA256  string   `json:"ruleset_sha256"`
	ActionSHA256   string   `json:"action_sha256"`            // ActionDigest(request)
	FactsSHA256    string   `json:"facts_sha256"`             // FactsDigest(request kind, facts)
	Tainted        bool     `json:"tainted"`                  // some input came from an untrusted source
	ApprovalsUsed  []string `json:"approvals_used,omitempty"` // the approval decisions an allow rested on, in the order the rules used them
}

// ActionDigest is the SHA-256 of the action a request asks for: its actor, kind and target, but
// not its inputs. An approval names this digest, so it approves one actor doing one thing to one
// object, however the request is later justified.
func ActionDigest(r Request) string {
	data, err := json.Marshal(struct {
		Actor  Actor  `json:"actor"`
		Kind   Kind   `json:"kind"`
		Target Target `json:"target"`
	}{r.Actor, r.Kind, r.Target})
	if err != nil {
		return "" // cannot happen for these field types; an empty digest matches no approval
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// FactsDigest is the SHA-256 of the facts an approval of a kind of action binds to, the facts the
// approver saw: for a merge the pull request's head SHA, base ref and base SHA; for a delete the
// symlink-resolved path; for a credential use the credential's scope; for a force-push, branch
// delete or history rewrite the project's default branch. Other kinds bind to no facts. It is the
// SHA-256 of the JSON object {"kind": kind, "facts": {...}} with exactly those fields, so it is
// deterministic and an approval made under one set of facts never matches another.
func FactsDigest(k Kind, f Facts) string {
	bound := map[string]string{}
	switch k {
	case KindMerge:
		bound = map[string]string{"head_sha": f.HeadSHA, "base_ref": f.BaseRef, "base_sha": f.BaseSHA}
	case KindDelete:
		bound = map[string]string{"resolved_path": f.ResolvedPath}
	case KindCredentialUse:
		bound = map[string]string{"credential_scope": f.CredentialScope}
	case KindForcePush, KindBranchDelete, KindHistoryRewrite:
		bound = map[string]string{"default_branch": f.DefaultBranch}
	}
	data, err := json.Marshal(struct {
		Kind  Kind              `json:"kind"`
		Facts map[string]string `json:"facts"`
	}{k, bound})
	if err != nil {
		return "" // cannot happen for these field types; an empty digest matches no approval
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
