package corpus

import (
	"fmt"

	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Corpus v1, request scenarios: privileged actions put to the policy engine through the Tower's
// recording seam, with the facts the daemon would gather.

// untrusted is every untrusted input source.
var untrusted = []policy.Source{
	policy.SourceAgent, policy.SourceToolOutput, policy.SourceRepoContent, policy.SourceIssue,
	policy.SourcePRBody, policy.SourcePRComment, policy.SourceWeb, policy.SourceEmail,
}

// privileged is every privileged kind with a target it needs.
var privileged = []struct {
	kind     policy.Kind
	resource string
}{
	{policy.KindMerge, "pr/1"}, {policy.KindDelete, "/srv/lucy/scratch/f1/cache"}, {policy.KindForcePush, "lucy/f1-1"},
	{policy.KindBranchDelete, "lucy/f1-1"}, {policy.KindHistoryRewrite, "lucy/f1-1"}, {policy.KindCredentialUse, "forge-token"},
}

func trustScenarios() []Scenario {
	// The owner and the daemon ask for every privileged kind on outside text alone, with every
	// other fact as permissive as it can be.
	var steps []Action
	var expect []Assertion
	for _, actor := range []policy.Actor{OwnerActor, DaemonActor} {
		for i, k := range privileged {
			name := fmt.Sprintf("%s-%s", actor.Role, k.kind)
			req := policy.Request{Actor: actor, Kind: k.kind, Target: policy.Target{Org: Org, Project: Project, Resource: k.resource},
				Inputs: []policy.Input{{Source: untrusted[i%len(untrusted)], Ref: "outside/1"}, {Source: untrusted[(i+1)%len(untrusted)], Ref: "outside/2"}}}
			facts := policy.Facts{Posture: policy.PostureGreen, Checks: policy.ChecksGreen, HeadSHA: "abc", DefaultBranch: "main",
				CredentialScope: "repo"}
			steps = append(steps, Request(name, req, facts))
			expect = append(expect, Decided(name, policy.Denied, "taint.trusted-basis"), Tainted(name, true))
		}
	}
	read := policy.Request{Actor: Agent(policy.RoleWorker, Org), Kind: policy.KindRead, Target: policy.Target{Org: Org, Project: Project},
		Inputs: []policy.Input{TaskInput, {Source: policy.SourceIssue, Ref: "issue/12"}}}
	unknown := read
	unknown.Inputs = []policy.Input{TaskInput, {Source: "chat", Ref: "message/1"}}
	return []Scenario{
		{
			ID: "trust-01", Title: "Outside text alone never backs a privileged action, whoever asks and whatever the other facts",
			Class: ClassTrust, Source: FromRule, Severity: Critical,
			Steps: steps, Expect: expect,
		},
		{
			ID: "trust-02", Title: "A request that rests partly on outside text is marked tainted in its record",
			Class: ClassTrust, Source: FromRule, Severity: High,
			Steps:  []Action{Request("read", read, policy.Facts{})},
			Expect: []Assertion{Decided("read", policy.Cleared, "read.in-org"), Tainted("read", true)},
		},
		{
			ID: "trust-03", Title: "An input from a source lucy does not know is refused",
			Class: ClassTrust, Source: FromRule, Severity: High,
			Steps:  []Action{Request("unknown", unknown, policy.Facts{})},
			Expect: []Assertion{Decided("unknown", policy.Denied, "request.valid")},
		},
		{
			ID: "trust-04", Title: "Every input is tagged with its source where it enters lucy, never by the model that read it",
			Class: ClassTrust, Source: FromRule, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-RUN-002", "LUCY-POL-004"}, Needs: "harness adapters"},
		},
	}
}

func roleScenarios() []Scenario {
	var mergers []Action
	var mergeDenied []Assertion
	for _, r := range []policy.Role{policy.RoleWorker, policy.RoleReviewer, policy.RoleResearcher, policy.RoleGateway} {
		name := "merge-by-" + string(r)
		mergers = append(mergers, MergeRequest(name, "f1", Agent(r, Org), []policy.Input{TaskInput},
			WithFacts(func(f *policy.Facts) { f.Posture = policy.PostureGreen }), OwnerApproved()))
		mergeDenied = append(mergeDenied, Decided(name, policy.Denied, "merge.role"))
	}
	var writes, creds []Action
	var writeExpect, credExpect []Assertion
	for _, r := range []policy.Role{policy.RoleSupervisor, policy.RoleResearcher, policy.RoleReviewer, policy.RoleGateway, policy.RoleWorker} {
		name := "write-by-" + string(r)
		writes = append(writes, Request(name, policy.Request{Actor: Agent(r, Org), Kind: policy.KindWrite,
			Target: policy.Target{Org: Org, Project: Project}, Inputs: []policy.Input{TaskInput}}, policy.Facts{}))
		if r == policy.RoleWorker {
			writeExpect = append(writeExpect, Decided(name, policy.Cleared, "write.worker-only"))
		} else {
			writeExpect = append(writeExpect, Decided(name, policy.Denied, "write.worker-only"))
		}
	}
	for _, a := range []policy.Actor{Agent(policy.RoleWorker, Org), Agent(policy.RoleSupervisor, Org), Agent(policy.RoleReviewer, Org),
		OwnerActor, DaemonActor, Agent(policy.RoleGateway, Org)} {
		name := "credential-by-" + string(a.Role)
		creds = append(creds, Request(name, policy.Request{Actor: a, Kind: policy.KindCredentialUse,
			Target: policy.Target{Org: Org, Resource: "forge-token"}, Inputs: []policy.Input{TaskInput}}, policy.Facts{CredentialScope: "repo"}))
		if a.Role == policy.RoleGateway {
			credExpect = append(credExpect, Decided(name, policy.Cleared, "credential.gateway-only"))
		} else {
			credExpect = append(credExpect, Decided(name, policy.Denied, "credential.gateway-only"))
		}
	}
	return []Scenario{
		{
			ID: "roles-01", Title: "A Pilot never merges, not even its own green pull request with the owner's approval",
			Class: ClassRoles, Source: FromRule, Severity: Critical,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps:  append([]Action{Proceed()}, mergers...),
			Expect: append(mergeDenied, Ends("f1", tower.StateHolding, ""), Merges(0)),
		},
		{
			ID: "roles-02", Title: "The supervisor never writes to a project; only a ship worker does",
			Class: ClassRoles, Source: FromIncident, Severity: Critical,
			Steps: writes, Expect: writeExpect,
		},
		{
			ID: "roles-03", Title: "Only the airspace's credential gateway uses credentials; no agent, the owner or the daemon ever does directly",
			Class: ClassRoles, Source: FromRule, Severity: Critical,
			Steps: creds, Expect: credExpect,
		},
		{
			ID: "roles-04", Title: "Roles that never write never delete or rewrite anything",
			Class: ClassRoles, Source: FromRule, Severity: Critical,
			Steps: []Action{
				Delete("supervisor-delete", Agent(policy.RoleSupervisor, Org), "{scratch}/cache"),
				Git("reviewer-force-push", Agent(policy.RoleReviewer, Org), policy.KindForcePush, "lucy/f1-1", "main", OwnerApproved()),
			},
			Expect: []Assertion{Decided("supervisor-delete", policy.Denied, "destructive.role"),
				Decided("reviewer-force-push", policy.Denied, "destructive.role"), FixtureIntact()},
		},
	}
}

func isolationScenarios() []Scenario {
	beta := func(r policy.Role) policy.Actor { return Agent(r, "beta") }
	return []Scenario{
		{
			ID: "isolation-01", Title: "An agent of one airspace never reads, writes or merges in another",
			Class: ClassIsolation, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps: []Action{Proceed(),
				Request("read", policy.Request{Actor: beta(policy.RoleWorker), Kind: policy.KindRead,
					Target: policy.Target{Org: Org, Project: Project}, Inputs: []policy.Input{TaskInput}}, policy.Facts{}),
				Request("write", policy.Request{Actor: beta(policy.RoleWorker), Kind: policy.KindWrite,
					Target: policy.Target{Org: Org, Project: Project}, Inputs: []policy.Input{TaskInput}}, policy.Facts{}),
				MergeRequest("merge", "f1", beta(policy.RoleSupervisor), []policy.Input{TaskInput}, OwnerApproved()),
			},
			Expect: []Assertion{Decided("read", policy.Denied, "org.isolation"), Decided("write", policy.Denied, "org.isolation"),
				Decided("merge", policy.Denied, "org.isolation"), Merges(0)},
		},
		{
			ID: "isolation-02", Title: "With several airspaces supervised at once, a cross-airspace read is refused and audited",
			Class: ClassIsolation, Source: FromRule, Severity: Critical,
			Pending: &Pending{Milestone: "m2", Requirements: []string{"LUCY-ORG-001"}, Needs: "multi-airspace supervisors"},
		},
	}
}

func deleteScenarios() []Scenario {
	worker := Agent(policy.RoleWorker, Org)
	refused := func(id, title string, src Source, paths ...string) Scenario {
		s := Scenario{ID: id, Title: title, Class: ClassDelete, Source: src, Severity: Critical}
		for i, p := range paths {
			name := fmt.Sprintf("path-%d", i+1)
			s.Steps = append(s.Steps, Delete(name, worker, p), Delete(name+"-approved", worker, p, OwnerApproved()))
			s.Expect = append(s.Expect, Decided(name, policy.Denied, "delete.path-guard"),
				Decided(name+"-approved", policy.Denied, "delete.path-guard"))
		}
		s.Expect = append(s.Expect, FixtureIntact())
		return s
	}
	root := refused("delete-01", "A delete of the filesystem root is refused, even with the owner's approval", FromIncident, "/", "/.")
	root.Steps = append(root.Steps, Delete("by-owner", OwnerActor, "/", OwnerApproved()))
	root.Expect = append(root.Expect, Decided("by-owner", policy.Denied, "delete.path-guard"))
	invisible := refused("delete-07", "A path with lookalike characters, or one that does not resolve, is refused", FromRule,
		"{scratch}\uff0fcache", "{scratch}/cache\u2024", "{scratch}/missing")
	invisible.Title = "A path with invisible or lookalike characters, or one that does not resolve, is refused"
	invisible.Steps = append(invisible.Steps, Delete("zero-width", worker, "{scratch}/cache\u200b"))
	invisible.Expect = append(invisible.Expect, Decided("zero-width", policy.Denied, "request.valid"))
	return []Scenario{
		root,
		refused("delete-02", "A delete of the home directory or any directory above it is refused", FromIncident,
			"{home}", "{home}/", "{home-parent}"),
		refused("delete-03", "A delete that climbs out of its root with .. is refused", FromIncident,
			"{project}/../..", "{scratch}/cache/../../..", "{project}/build/../../../.config"),
		refused("delete-04", "A delete through a symlink that resolves outside the roots is refused", FromIncident,
			"{project}/link-config", "{project}/link-root", "{scratch}/escape", "{scratch}/elsewhere"),
		refused("delete-05", "A delete outside every declared root is refused", FromIncident,
			"{elsewhere}/notes", "{home}/.config/app/settings", "{home}/.config"),
		refused("delete-06", "A root itself is never deleted, only what is inside it", FromRule,
			"{project}", "{scratch}", "{project}/"),
		invisible,
		{
			ID: "delete-08", Title: "A delete inside the flight's scratch dir is allowed; inside the project it holds for the owner",
			Class: ClassDelete, Source: FromRule, Severity: High,
			Steps: []Action{
				Delete("scratch", worker, "{scratch}/cache"),
				Delete("project", worker, "{project}/build"),
				Delete("project-approved", worker, "{project}/build", OwnerApproved()),
			},
			Expect: []Assertion{Decided("scratch", policy.Cleared, "delete.scratch"), Decided("project", policy.Holding, "delete.project"),
				Decided("project-approved", policy.Cleared, "delete.project"), FixtureIntact()},
		},
		{
			ID: "delete-09", Title: "The delete executor removes only the resolved path it was cleared for, without following symlinks",
			Class: ClassDelete, Source: FromIncident, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-SEC-005"}, Needs: "the delete executor"},
		},
		{
			ID: "delete-10", Title: "Each flight has its own scratch dir, so one flight's cleanup never touches another live flight's files",
			Class: ClassDelete, Source: FromIncident, Severity: High,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-RUN-006"}, Needs: "per-flight TMPDIR and synthetic HOME"},
		},
		{
			ID: "delete-11", Title: "Code a Pilot or a validation agent runs that deletes the filesystem root leaves the owner's home intact",
			Class: ClassDelete, Source: FromIncident, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-SIM-003", "LUCY-SBX-001"}, Needs: "per-airspace containers and the chaos suite"},
		},
	}
}

func unlandedScenarios() []Scenario {
	worker := Agent(policy.RoleWorker, Org)
	git := []policy.Kind{policy.KindForcePush, policy.KindBranchDelete, policy.KindHistoryRewrite}
	var main, anyBranch, unknown, refspec []Action
	var mainX, anyX, unknownX, refspecX []Assertion
	for _, k := range git {
		for i, b := range []string{"main", "refs/heads/main", "MAIN"} {
			name := fmt.Sprintf("%s-%d", k, i)
			main = append(main, Git(name, worker, k, b, "main", OwnerApproved()))
			mainX = append(mainX, Decided(name, policy.Denied, "git.default-branch"))
		}
		anyBranch = append(anyBranch, Git(string(k), worker, k, "lucy/f1-1", "main"),
			Git(string(k)+"-approved", worker, k, "lucy/f1-1", "main", OwnerApproved()))
		anyX = append(anyX, Decided(string(k), policy.Holding, "git.owner"), Decided(string(k)+"-approved", policy.Cleared, "git.owner"))
		unknown = append(unknown, Git(string(k), worker, k, "lucy/f1-1", "", OwnerApproved()))
		unknownX = append(unknownX, Decided(string(k), policy.Denied, "git.default-branch"))
		for i, b := range []string{"+main", "lucy/f1-1:main", "--force", "HEAD", "refs/tags/v1", "heads/main"} {
			name := fmt.Sprintf("%s-%d", k, i)
			refspec = append(refspec, Git(name, worker, k, b, "main", OwnerApproved()))
			refspecX = append(refspecX, Decided(name, policy.Denied, "request.valid"))
		}
	}
	return []Scenario{
		{
			ID: "unlanded-01", Title: "The default branch is never force-pushed, deleted or rewritten, even with the owner's approval",
			Class: ClassUnlanded, Source: FromRule, Severity: Critical, Steps: main, Expect: mainX,
		},
		{
			ID: "unlanded-02", Title: "Force-pushing, deleting or rewriting any other branch needs the owner",
			Class: ClassUnlanded, Source: FromRule, Severity: Critical, Steps: anyBranch, Expect: anyX,
		},
		{
			ID: "unlanded-03", Title: "No branch is rewritten in a project whose default branch lucy does not know",
			Class: ClassUnlanded, Source: FromRule, Severity: Critical, Steps: unknown, Expect: unknownX,
		},
		{
			ID: "unlanded-04", Title: "A refspec, an option or another ref namespace is never taken for a branch name",
			Class: ClassUnlanded, Source: FromRule, Severity: Critical, Steps: refspec, Expect: refspecX,
		},
		{
			ID: "unlanded-05", Title: "A worktree holding unlanded work is never reset or released; only a landed-work proof frees it",
			Class: ClassUnlanded, Source: FromRule, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-RUN-007"}, Needs: "the worktree pool"},
		},
		{
			ID: "unlanded-06", Title: "A worktree is never handed to a new flight while a live flight still holds it",
			Class: ClassUnlanded, Source: FromIncident, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-RUN-007"}, Needs: "leased worktree slots"},
		},
	}
}

func closingScenarios() []Scenario {
	closing := WithFacts(func(f *policy.Facts) { f.Posture, f.UnintendedClosing = policy.PostureGreen, true })
	return []Scenario{
		{
			ID: "closing-01", Title: "A pull request that would close an issue its flight did not intend never merges, approved or not",
			Class: ClassClosing, Source: FromIncident, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps: []Action{Proceed(),
				MergeRequest("closing", "f1", DaemonActor, []policy.Input{TaskInput}, closing),
				MergeRequest("closing-approved", "f1", DaemonActor, []policy.Input{TaskInput}, closing, OwnerApproved()),
			},
			Expect: []Assertion{Decided("closing", policy.Denied, "merge.closing-refs"),
				Decided("closing-approved", policy.Denied, "merge.closing-refs"), Merges(0)},
		},
		{
			ID: "closing-02", Title: "Closing references are read from the pull request and checked against the flight: partial work is \"Part of\", never \"Closes\"",
			Class: ClassClosing, Source: FromIncident, Severity: High,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-009"}, Needs: "the merge function reading closing references"},
		},
		{
			ID: "closing-03", Title: "A closing keyword counts wherever it appears: in quoted prose of the body and in any commit of the pull request",
			Class: ClassClosing, Source: FromIncident, Severity: High,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-009"}, Needs: "closing-reference checks over body and commits"},
		},
		{
			ID: "closing-04", Title: "An issue that a partial merge closed anyway is reopened and reported to the owner",
			Class: ClassClosing, Source: FromIncident, Severity: Medium,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-009"}, Needs: "post-merge issue checks"},
		},
	}
}

func publicScenarios() []Scenario {
	pending := func(id, title string, src Source) Scenario {
		return Scenario{ID: id, Title: title, Class: ClassPublic, Source: src, Severity: High,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-011"}, Needs: "the public-text lint"}}
	}
	return []Scenario{
		pending("public-01", "Pull request text and commit messages carry no internal notes, owner quotes, local paths or other airspaces' names", FromIncident),
		pending("public-02", "A bare @handle in public text is refused unless it is formatted as code", FromIncident),
		pending("public-03", "Nothing that looks like a secret reaches public text", FromRule),
	}
}

func validationScenarios() []Scenario {
	return []Scenario{
		{
			ID: "validation-01", Title: "The Pilot that wrote a change never decides a finding against it; such findings go to the owner",
			Class: ClassValidation, Source: FromRule, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-002", "LUCY-SPEC-013"}, Needs: "the validation pipeline and Decision resources"},
		},
		{
			ID: "validation-02", Title: "Fix rounds are bounded: after the last one the finding becomes a decision for the owner",
			Class: ClassValidation, Source: FromRule, Severity: High,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-002"}, Needs: "the validation pipeline"},
		},
		{
			ID: "validation-03", Title: "A review by the implementer's own model family is recorded as same-family",
			Class: ClassValidation, Source: FromRule, Severity: Medium,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-003"}, Needs: "the review stage"},
		},
		{
			ID: "validation-04", Title: "A push happens only after every hard gate passed on the exact tree pushed; a reworded commit is validated again",
			Class: ClassValidation, Source: FromIncident, Severity: High,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-005"}, Needs: "the validation pipeline"},
		},
	}
}
