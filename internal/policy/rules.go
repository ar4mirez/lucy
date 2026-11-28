package policy

// This file is the rule set: its source is hashed into RuleSetSHA256, so any change here is a new
// rule set version (see engine.go). Rules run in the order of the table; the first rule with an
// opinion decides, and when none has one, default.deny does.

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ar4mirez/lucy/internal/pathguard"
)

// Limits on a request. A request outside them is invalid.
const (
	MaxLabelBytes    = 128  // an actor id, org, project, input ref
	MaxResourceBytes = 4096 // a target resource (a path can be long)
	MaxInputs        = 32
	MaxFactItems     = 64 // roots, exceptions and approvals each
)

// Rule is one entry of the rule set.
type Rule struct {
	ID      string
	Summary string
	eval    func(*evalCtx) (verdict, bool)
}

type verdict struct {
	outcome   Outcome
	reason    string
	approvals []string
}

type evalCtx struct {
	req    Request
	facts  Facts
	action string   // ActionDigest(req)
	bound  string   // FactsDigest(req.Kind, facts)
	used   []string // approvals earlier rules accepted without deciding, in order
}

func allow(reason string) (verdict, bool) { return verdict{outcome: Cleared, reason: reason}, true }
func deny(reason string) (verdict, bool)  { return verdict{outcome: Denied, reason: reason}, true }
func pass() (verdict, bool)               { return verdict{}, false }
func hold(reason string) (verdict, bool) {
	return verdict{outcome: Holding, reason: reason}, true
}

// match returns the decision id of an approval by by for this action under these facts, or
// whether one for this action was made under different facts. Approvals for other actions are
// ignored.
func (c *evalCtx) match(by ApprovalBy) (id string, stale bool) {
	for _, a := range c.facts.Approvals {
		if a.By != by || a.Decision == "" || a.ActionSHA256 != c.action {
			continue
		}
		if a.FactsSHA256 == c.bound {
			return a.Decision, false
		}
		stale = true
	}
	return "", stale
}

// approved allows when a matching approval exists, denies when the only ones for this action were
// made under different facts, else holds for the owner.
func (c *evalCtx) approved(by ApprovalBy, allowWhy, holdWhy string) (verdict, bool) {
	id, stale := c.match(by)
	switch {
	case id != "":
		return verdict{outcome: Cleared, reason: allowWhy, approvals: c.use(id)}, true
	case stale:
		return deny(c.staleReason())
	}
	return hold(holdWhy)
}

// use returns the approvals used so far with id appended, once.
func (c *evalCtx) use(id string) []string {
	for _, u := range c.used {
		if u == id {
			return c.used
		}
	}
	return append(c.used, id)
}

// staleReason says which bound facts an approval for this action no longer matches.
func (c *evalCtx) staleReason() string {
	changed := map[Kind]string{ // #nosec G101 -- reasons keyed by action kind; no credential
		KindMerge:         "the pull request's head or base changed",
		KindDelete:        "the path now resolves elsewhere",
		KindCredentialUse: "the credential's scope changed",
		KindContainer:     "the container's live sessions changed",
	}[c.req.Kind]
	if changed == "" {
		changed = "the project's default branch changed"
	}
	return "the approval was for different facts: " + changed
}

// actorName names a role in a reason: "a worker", or "the {owner}" for the owner, whose display
// title is resolved when the reason is rendered.
func actorName(r Role) string {
	if r == RoleOwner {
		return "the {owner}"
	}
	return "a " + string(r)
}

func (c *evalCtx) kindIs(kinds ...Kind) bool {
	for _, k := range kinds {
		if c.req.Kind == k {
			return true
		}
	}
	return false
}

var gitDestructive = []Kind{KindForcePush, KindBranchDelete, KindHistoryRewrite}

// rules is the rule set, in evaluation order.
var rules = []Rule{
	{"request.valid", "A request must be well formed: known actor role, kind and input sources, bounded labels, and the target fields its kind needs.", func(c *evalCtx) (verdict, bool) {
		if err := validRequest(c.req); err != "" {
			return deny("invalid request: " + err)
		}
		return pass()
	}},
	{"facts.valid", "The daemon's facts must be well formed: known posture, check state, fixed-list categories and approval kinds, bounded lists.", func(c *evalCtx) (verdict, bool) {
		if err := validFacts(c.facts); err != "" {
			return deny("invalid facts: " + err)
		}
		return pass()
	}},
	{"org.isolation", "An actor bound to an org acts only inside that org; cross-org access is denied.", func(c *evalCtx) (verdict, bool) {
		if orgBound[c.req.Actor.Role] && c.req.Actor.Org != c.req.Target.Org {
			return deny(fmt.Sprintf("%s of org %q may not act on org %q", actorName(c.req.Actor.Role), c.req.Actor.Org, c.req.Target.Org))
		}
		return pass()
	}},
	{"taint.trusted-basis", "A privileged request needs at least one input from a trusted source; outside text alone never raises privileges.", func(c *evalCtx) (verdict, bool) {
		if !Privileged(c.req.Kind) {
			return pass()
		}
		for _, in := range c.req.Inputs {
			if Trusted(in.Source) {
				return pass()
			}
		}
		return deny("every input behind this privileged request comes from an untrusted source")
	}},
	{"role.blocked-paths", "A write or delete of a path the actor role's AgentRole blocks (constraints.blockedPaths, globs relative to the checkout) is denied, whatever else allows it.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindWrite, KindDelete) || len(c.facts.BlockedPaths) == 0 {
			return pass()
		}
		if g, ok := pathguard.BlockedBy(c.facts.WorkRoot, c.req.Target.Resource, c.facts.BlockedPaths); ok {
			return deny(fmt.Sprintf("the path matches %q, which the %s role's AgentRole blocks", g, c.req.Actor.Role))
		}
		return pass()
	}},
	{"read.in-org", "Reading inside the actor's own org is allowed.", func(c *evalCtx) (verdict, bool) {
		if c.kindIs(KindRead) {
			return allow("a read inside the actor's org")
		}
		return pass()
	}},
	{"write.worker-only", "Only a ship worker changes a project, inside its own org.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindWrite) {
			return pass()
		}
		if c.req.Actor.Role == RoleWorker {
			return allow("a worker writes in its own org's project")
		}
		return deny(actorName(c.req.Actor.Role) + " never writes to a project")
	}},
	{"credential.gateway-only", "Only the org's credential gateway uses credentials; no agent role ever does.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindCredentialUse) {
			return pass()
		}
		if c.req.Actor.Role == RoleGateway {
			return allow("the org's gateway uses its own credential")
		}
		return deny(actorName(c.req.Actor.Role) + " may not use credentials; only the gateway does")
	}},
	{"merge.role", "Only the {owner}, the daemon and an org supervisor merge.", func(c *evalCtx) (verdict, bool) {
		if c.kindIs(KindMerge) && !mergers[c.req.Actor.Role] {
			return deny(actorName(c.req.Actor.Role) + " never merges")
		}
		return pass()
	}},
	{"merge.head-known", "A merge needs the pull request's head commit, so an approval binds to the code it approves.", func(c *evalCtx) (verdict, bool) {
		if c.kindIs(KindMerge) && c.facts.HeadSHA == "" {
			return deny("the pull request's head commit is unknown")
		}
		return pass()
	}},
	{"merge.checks-green", "Never merge red, pending or unknown checks, or with a required check unreported, in any posture.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindMerge) {
			return pass()
		}
		if c.facts.Checks != ChecksGreen {
			state := string(c.facts.Checks)
			if state == "" {
				state = "unknown"
			}
			return deny("the pull request's checks are " + state)
		}
		if c.facts.RequiredUnreported {
			return deny("a required check has not reported")
		}
		return pass()
	}},
	{"merge.closing-refs", "Never merge a pull request that closes issues its task did not intend.", func(c *evalCtx) (verdict, bool) {
		if c.kindIs(KindMerge) && c.facts.UnintendedClosing {
			return deny("the pull request closes issues its task did not intend")
		}
		return pass()
	}},
	{"merge.fixed-list", "A change on the fixed list (destructive, irreversible, security, production, billing, credentials, data migration, cross-org, judgement) needs the {owner} in every posture, and the posture still decides.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindMerge) || len(c.facts.Exceptions) == 0 {
			return pass()
		}
		id, stale := c.match(ByOwner)
		switch {
		case id != "":
			c.used = c.use(id)
			return pass()
		case stale:
			return deny(c.staleReason())
		}
		return hold(fmt.Sprintf("the change touches the fixed list (%s)", joinExceptions(c.facts.Exceptions)))
	}},
	{"merge.posture", "Merge within the project's posture: the owner posture needs the approval of the {owner}, the green posture merges, the client posture needs the client's confirmation relayed by the {owner}.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindMerge) {
			return pass()
		}
		switch c.facts.Posture {
		case PostureOwner, "":
			return c.approved(ByOwner, "the {owner} approved this merge", "the owner posture merges only on the word of the {owner}")
		case PostureGreen:
			return verdict{outcome: Cleared, reason: "green checks under the green posture", approvals: c.used}, true
		case PostureClient:
			return c.approved(ByClientViaOwner, "the {owner} relayed the client's confirmation",
				"the client posture merges only after the {owner} relays the client's confirmation")
		}
		return deny(fmt.Sprintf("unknown posture %q", c.facts.Posture))
	}},
	{"destructive.role", "Roles that never write (supervisor, researcher, reviewer, gateway) never delete or rewrite anything.", func(c *evalCtx) (verdict, bool) {
		if c.kindIs(append([]Kind{KindDelete}, gitDestructive...)...) && neverWrite[c.req.Actor.Role] {
			return deny(actorName(c.req.Actor.Role) + " never deletes or rewrites")
		}
		return pass()
	}},
	{"delete.path-guard", "A delete must pass the path guard after symlink resolution: never /, $HOME or an ancestor of it, only strictly inside a declared root.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindDelete) {
			return pass()
		}
		if c.facts.ResolvedPath == "" {
			return deny("the delete target's resolved path is unknown")
		}
		if _, err := pathguard.CheckResolved(c.facts.Home, c.facts.Roots, c.req.Target.Resource, c.facts.ResolvedPath); err != nil {
			return deny(err.Error())
		}
		return pass()
	}},
	{"delete.scratch", "A delete strictly inside the task's scratch dir is allowed.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindDelete) {
			return pass()
		}
		root, _ := pathguard.CheckResolved(c.facts.Home, c.facts.Roots, c.req.Target.Resource, c.facts.ResolvedPath)
		if root.Kind == pathguard.Scratch {
			return allow("the path is inside the task's scratch dir")
		}
		return pass()
	}},
	{"delete.project", "A delete inside a project, outside the scratch dir, needs the {owner}.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindDelete) {
			return pass()
		}
		return c.approved(ByOwner, "the {owner} approved this delete", "the path is inside a project, outside the task's scratch dir")
	}},
	{"git.default-branch", "Never force-push, delete or rewrite the default branch, or a branch of a project whose default branch is unknown.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(gitDestructive...) {
			return pass()
		}
		if c.facts.DefaultBranch == "" {
			return deny("the project's default branch is unknown")
		}
		if strings.EqualFold(shortBranch(c.req.Target.Resource), shortBranch(c.facts.DefaultBranch)) {
			return deny("the target is the default branch")
		}
		return pass()
	}},
	{"git.owner", "Force-push, branch delete and history rewrite need the {owner}.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(gitDestructive...) {
			return pass()
		}
		return c.approved(ByOwner, "the {owner} approved this "+string(c.req.Kind), string(c.req.Kind)+" is destructive")
	}},
	{"container.role", "Only the {owner} and the daemon start, restart or stop an org's container; no agent does.", func(c *evalCtx) (verdict, bool) {
		if c.kindIs(KindContainer) && !containerRoles[c.req.Actor.Role] {
			return deny(actorName(c.req.Actor.Role) + " never starts, restarts or stops a container")
		}
		return pass()
	}},
	{"container.live-sessions", "Starting, restarting or stopping an org's container stops the work in it: with no live session it is cleared, with live sessions it needs the {owner}'s confirmation for exactly those sessions.", func(c *evalCtx) (verdict, bool) {
		if !c.kindIs(KindContainer) {
			return pass()
		}
		if !c.facts.SessionsKnown {
			return deny("the org container's live sessions are unknown")
		}
		if len(c.facts.LiveSessions) == 0 {
			return allow("no session is live in the org's container")
		}
		n := len(c.facts.LiveSessions)
		return c.approved(ByOwner, fmt.Sprintf("the {owner} confirmed stopping %s", sessions(n)),
			fmt.Sprintf("%s live in the org's container would stop", sessions(n)))
	}},
	{"default.deny", "Anything no rule allows is denied.", func(*evalCtx) (verdict, bool) {
		return deny("no rule allows this action")
	}},
}

// validRequest returns "" or what is wrong with r.
func validRequest(r Request) string {
	a := r.Actor
	if _, ok := orgBound[a.Role]; !ok {
		return fmt.Sprintf("unknown role %q", a.Role)
	}
	if _, ok := privileged[r.Kind]; !ok {
		return fmt.Sprintf("unknown kind %q", r.Kind)
	}
	for name, v := range map[string]string{"actor id": a.ID, "actor org": a.Org, "org": r.Target.Org, "project": r.Target.Project} {
		if e := label(v, MaxLabelBytes); e != "" {
			return name + " " + e
		}
	}
	if e := label(r.Target.Resource, MaxResourceBytes); e != "" {
		return "resource " + e
	}
	switch {
	case a.ID == "":
		return "the actor has no id"
	case orgBound[a.Role] && a.Org == "":
		return actorName(a.Role) + " must belong to an org"
	case !orgBound[a.Role] && a.Org != "":
		return actorName(a.Role) + " belongs to no org"
	case r.Target.Org == "":
		return "the target names no org"
	}
	needs := map[Kind][]string{
		KindWrite: {"project"}, KindMerge: {"project", "resource"}, KindDelete: {"resource"},
		KindForcePush: {"project", "resource"}, KindBranchDelete: {"project", "resource"},
		KindHistoryRewrite: {"project", "resource"}, KindCredentialUse: {"resource"},
		KindExec: {"project", "resource"}, KindFetch: {"resource"}, KindContainer: {"resource"},
	}
	for _, field := range needs[r.Kind] {
		if field == "project" && r.Target.Project == "" || field == "resource" && r.Target.Resource == "" {
			return fmt.Sprintf("a %s needs a target %s", r.Kind, field)
		}
	}
	if r.Kind == KindContainer && !ContainerOps[r.Target.Resource] {
		return "a container request names up, restart or down"
	}
	if r.Kind == KindDelete && !filepath.IsAbs(r.Target.Resource) {
		return "a delete names an absolute path"
	}
	if (r.Kind == KindForcePush || r.Kind == KindBranchDelete || r.Kind == KindHistoryRewrite) && !plainBranch(r.Target.Resource) {
		return fmt.Sprintf("a %s names a plain branch name", r.Kind)
	}
	if len(r.Inputs) > MaxInputs {
		return fmt.Sprintf("more than %d inputs", MaxInputs)
	}
	for _, in := range r.Inputs {
		if !KnownSource(in.Source) {
			return fmt.Sprintf("unknown input source %q", in.Source)
		}
		if e := label(in.Ref, MaxLabelBytes); e != "" {
			return "input ref " + e
		}
	}
	return ""
}

// validFacts returns "" or what is wrong with f.
func validFacts(f Facts) string {
	switch f.Posture {
	case "", PostureOwner, PostureGreen, PostureClient:
	default:
		return fmt.Sprintf("unknown posture %q", f.Posture)
	}
	switch f.Checks {
	case "", ChecksGreen, ChecksRed, ChecksPending:
	default:
		return fmt.Sprintf("unknown check state %q", f.Checks)
	}
	if len(f.Exceptions) > MaxFactItems || len(f.Roots) > MaxFactItems || len(f.Approvals) > MaxFactItems ||
		len(f.LiveSessions) > MaxFactItems {
		return fmt.Sprintf("more than %d exceptions, roots, approvals or live sessions", MaxFactItems)
	}
	if len(f.BlockedPaths) > MaxFactItems {
		return fmt.Sprintf("more than %d blocked paths", MaxFactItems)
	}
	for _, g := range f.BlockedPaths {
		if !ValidBlockedPath(g) {
			return fmt.Sprintf("blocked path %q is not a glob of at most %d bytes", g, MaxResourceBytes)
		}
	}
	if f.WorkRoot != "" && (!filepath.IsAbs(f.WorkRoot) || len(f.WorkRoot) > MaxResourceBytes) {
		return "the work root is not an absolute path"
	}
	if !f.SessionsKnown && len(f.LiveSessions) > 0 {
		return "live sessions listed but not known"
	}
	for i, s := range f.LiveSessions {
		if s == "" {
			return "an empty live session"
		}
		if e := label(s, MaxLabelBytes); e != "" {
			return "live session " + e
		}
		if i > 0 && f.LiveSessions[i-1] >= s {
			return "live sessions are not sorted and unique"
		}
	}
	for _, e := range f.Exceptions {
		if !exceptions[e] {
			return fmt.Sprintf("unknown fixed-list category %q", e)
		}
	}
	for _, a := range f.Approvals {
		if a.By != ByOwner && a.By != ByClientViaOwner {
			return fmt.Sprintf("unknown approval kind %q", a.By)
		}
		if e := label(a.Decision, MaxLabelBytes); e != "" {
			return "approval decision " + e
		}
	}
	for name, v := range map[string]string{"default branch": f.DefaultBranch, "head sha": f.HeadSHA, "base ref": f.BaseRef,
		"base sha": f.BaseSHA, "credential scope": f.CredentialScope} {
		if e := label(v, MaxLabelBytes); e != "" {
			return name + " " + e
		}
	}
	for _, p := range append([]string{f.Home, f.ResolvedPath}, rootPaths(f.Roots)...) {
		if e := label(p, MaxResourceBytes); e != "" {
			return "path " + e
		}
	}
	return ""
}

// label returns "" or why s is not an acceptable label: valid UTF-8, no control or format
// characters, at most max bytes.
func label(s string, max int) string {
	if len(s) > max {
		return fmt.Sprintf("is longer than %d bytes", max)
	}
	if !utf8.ValidString(s) {
		return "is not valid UTF-8"
	}
	for _, c := range s {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return "contains a control or invisible character"
		}
	}
	return ""
}

// shortBranch is a branch name without its refs/heads/ prefix.
func shortBranch(s string) string { return strings.TrimPrefix(s, "refs/heads/") }

// plainBranch reports whether s, less a refs/heads/ prefix, is a plain branch name by git
// check-ref-format rules, and not a refspec, a forced push, an option or another ref namespace
// git could resolve it into (heads/, refs/, tags/, remotes/, HEAD).
func plainBranch(s string) bool {
	b := shortBranch(s)
	if b == "" || b == "@" || strings.HasPrefix(b, "-") || strings.HasPrefix(b, "/") || strings.HasSuffix(b, "/") ||
		strings.HasSuffix(b, ".") || strings.Contains(b, "..") || strings.Contains(b, "@{") || strings.Contains(b, "//") ||
		strings.ContainsAny(b, " :+~^?*[\\") {
		return false
	}
	parts := strings.Split(b, "/")
	switch strings.ToLower(parts[0]) {
	case "refs", "heads", "tags", "remotes", "head":
		return false
	}
	for _, p := range parts {
		if strings.HasPrefix(p, ".") || strings.HasSuffix(p, ".lock") {
			return false
		}
	}
	return true
}

func rootPaths(roots []pathguard.Root) []string {
	s := make([]string, len(roots))
	for i, r := range roots {
		s[i] = r.Path
	}
	return s
}

func joinExceptions(es []Exception) string {
	s := make([]string, len(es))
	for i, e := range es {
		s[i] = string(e)
	}
	return strings.Join(s, ", ")
}

// sessions says "1 session" or "n sessions".
func sessions(n int) string {
	if n == 1 {
		return "1 session"
	}
	return fmt.Sprintf("%d sessions", n)
}

// ValidBlockedPath reports whether g is a blocked-path glob the engine decides on: at most
// MaxResourceBytes and a glob pathguard.MatchGlob reads.
func ValidBlockedPath(g string) bool {
	return len(g) <= MaxResourceBytes && pathguard.ValidGlob(g)
}
