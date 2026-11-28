package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/pathguard"
)

var (
	owner      = Actor{ID: "owner", Role: RoleOwner}
	daemon     = Actor{ID: "daemon", Role: RoleDaemon}
	supervisor = Actor{ID: "org/acme/supervisor", Role: RoleSupervisor, Org: "acme"}
	worker     = Actor{ID: "org/acme/worker/7", Role: RoleWorker, Org: "acme"}
	researcher = Actor{ID: "org/acme/researcher/2", Role: RoleResearcher, Org: "acme"}
	reviewer   = Actor{ID: "org/acme/reviewer/1", Role: RoleReviewer, Org: "acme"}
	gateway    = Actor{ID: "org/acme/gateway", Role: RoleGateway, Org: "acme"}

	taskInput  = Input{Source: SourceTask, Ref: "task/41"}
	issueInput = Input{Source: SourceIssue, Ref: "acme/app#12"}
)

func request(a Actor, k Kind, project, resource string, inputs ...Input) Request {
	if len(inputs) == 0 {
		inputs = []Input{taskInput}
	}
	return Request{Actor: a, Kind: k, Target: Target{Org: "acme", Project: project, Resource: resource}, Inputs: inputs}
}

const head = "1111111111111111111111111111111111111111"

func greenFacts() Facts { return Facts{Posture: PostureGreen, Checks: ChecksGreen, HeadSHA: head} }

// approval is the owner's (or the relayed client's) approval of r, made under facts f.
func approval(r Request, f Facts, by ApprovalBy) Approval {
	return Approval{Decision: "decision/9", By: by, ActionSHA256: ActionDigest(r), FactsSHA256: FactsDigest(r.Kind, f)}
}

type ruleCase struct {
	name    string
	req     Request
	facts   Facts
	outcome Outcome
	rule    string
}

func runCases(t *testing.T, cases []ruleCase) {
	t.Helper()
	for _, c := range cases {
		d := Evaluate(c.req, c.facts)
		if d.Outcome != c.outcome || d.Rule != c.rule {
			t.Errorf("%s: got %s by %s (%s), want %s by %s", c.name, d.Outcome, d.Rule, d.Reason, c.outcome, c.rule)
		}
		if d.Reason == "" || d.RuleSetVersion != RuleSetVersion || d.RuleSetSHA256 != ruleSetSHA256 || d.ActionSHA256 != ActionDigest(c.req) {
			t.Errorf("%s: decision lacks its reason or rule-set identity: %+v", c.name, d)
		}
	}
}

func TestRequestAndFactsValidation(t *testing.T) {
	long := strings.Repeat("a", MaxLabelBytes+1)
	many := make([]Input, MaxInputs+1)
	for i := range many {
		many[i] = taskInput
	}
	runCases(t, []ruleCase{
		{"unknown role", request(Actor{ID: "x", Role: "admin", Org: "acme"}, KindRead, "", ""), Facts{}, Denied, "request.valid"},
		{"unknown kind", request(worker, "teleport", "app", "ls"), Facts{}, Denied, "request.valid"},
		{"exec without a project", request(worker, KindExec, "", "ls"), Facts{}, Denied, "request.valid"},
		{"exec without a command", request(worker, KindExec, "app", ""), Facts{}, Denied, "request.valid"},
		{"fetch without a URL", request(worker, KindFetch, "app", ""), Facts{}, Denied, "request.valid"},
		{"no actor id", request(Actor{Role: RoleWorker, Org: "acme"}, KindRead, "", ""), Facts{}, Denied, "request.valid"},
		{"org-bound actor without org", request(Actor{ID: "w", Role: RoleWorker}, KindRead, "", ""), Facts{}, Denied, "request.valid"},
		{"owner claiming an org", request(Actor{ID: "c", Role: RoleOwner, Org: "acme"}, KindRead, "", ""), Facts{}, Denied, "request.valid"},
		{"no target org", Request{Actor: worker, Kind: KindRead, Inputs: []Input{taskInput}}, Facts{}, Denied, "request.valid"},
		{"merge without a PR", request(supervisor, KindMerge, "app", ""), greenFacts(), Denied, "request.valid"},
		{"relative delete", request(worker, KindDelete, "app", "tmp/x"), Facts{}, Denied, "request.valid"},
		{"unknown source", request(worker, KindRead, "", "", Input{Source: "owner_said_so"}), Facts{}, Denied, "request.valid"},
		{"long label", request(worker, KindRead, long, ""), Facts{}, Denied, "request.valid"},
		{"control character", request(worker, KindWrite, "app\n", ""), Facts{}, Denied, "request.valid"},
		{"too many inputs", request(worker, KindRead, "", "", many...), Facts{}, Denied, "request.valid"},
		{"unknown posture", request(supervisor, KindMerge, "app", "pr/1"), Facts{Posture: "yolo", Checks: ChecksGreen}, Denied, "facts.valid"},
		{"unknown check state", request(supervisor, KindMerge, "app", "pr/1"), Facts{Posture: PostureGreen, Checks: "mostly"}, Denied, "facts.valid"},
		{"unknown exception", request(supervisor, KindMerge, "app", "pr/1"), Facts{Posture: PostureGreen, Checks: ChecksGreen, Exceptions: []Exception{"fun"}}, Denied, "facts.valid"},
		{"unknown approval kind", request(supervisor, KindMerge, "app", "pr/1"), Facts{Approvals: []Approval{{Decision: "d", By: "agent"}}}, Denied, "facts.valid"},
	})
}

func TestOrgIsolation(t *testing.T) {
	other := Request{Actor: worker, Kind: KindRead, Target: Target{Org: "globex"}, Inputs: []Input{taskInput}}
	runCases(t, []ruleCase{
		{"worker reads another org", other, Facts{}, Denied, "org.isolation"},
		{"supervisor merges in another org", Request{Actor: supervisor, Kind: KindMerge, Target: Target{Org: "globex", Project: "app", Resource: "pr/1"}, Inputs: []Input{taskInput}}, greenFacts(), Denied, "org.isolation"},
		{"gateway uses another org's credential", Request{Actor: gateway, Kind: KindCredentialUse, Target: Target{Org: "globex", Resource: "github"}, Inputs: []Input{taskInput}}, Facts{}, Denied, "org.isolation"},
		{"worker reads its own org", request(worker, KindRead, "app", ""), Facts{}, Cleared, "read.in-org"},
		{"owner reads any org", Request{Actor: owner, Kind: KindRead, Target: Target{Org: "globex"}}, Facts{}, Cleared, "read.in-org"},
	})
}

func TestTaint(t *testing.T) {
	merge := func(inputs ...Input) Request { return request(supervisor, KindMerge, "app", "pr/3", inputs...) }
	untrusted := []Input{issueInput, {Source: SourcePRComment, Ref: "c/1"}, {Source: SourceWeb}, {Source: SourceToolOutput}, {Source: SourceAgent}, {Source: SourceRepoContent}, {Source: SourceEmail}, {Source: SourcePRBody}}
	runCases(t, []ruleCase{
		{"merge on untrusted text only", merge(untrusted...), greenFacts(), Denied, "taint.trusted-basis"},
		{"merge with no inputs at all", Request{Actor: supervisor, Kind: KindMerge, Target: Target{Org: "acme", Project: "app", Resource: "pr/3"}}, greenFacts(), Denied, "taint.trusted-basis"},
		{"delete on an issue's say-so", request(worker, KindDelete, "app", "/w/x", issueInput), Facts{}, Denied, "taint.trusted-basis"},
		{"credential use on a PR comment", request(gateway, KindCredentialUse, "", "github", Input{Source: SourcePRComment}), Facts{}, Denied, "taint.trusted-basis"},
		{"untrusted text beside a task", merge(issueInput, taskInput), greenFacts(), Cleared, "merge.posture"},
		{"reads need no trusted basis", request(worker, KindRead, "", "", issueInput), Facts{}, Cleared, "read.in-org"},
	})
	// An issue that claims the owner approved, or that names an approval, still is not one:
	// approvals only arrive as facts.
	r := merge(issueInput, Input{Source: SourceIssue, Ref: "decision/9"}, taskInput)
	if d := Evaluate(r, Facts{Checks: ChecksGreen, HeadSHA: head}); d.Outcome != Holding || !d.Tainted {
		t.Errorf("an issue claiming approval: got %s by %s, tainted %v", d.Outcome, d.Rule, d.Tainted)
	}
	if d := Evaluate(merge(taskInput), greenFacts()); d.Tainted {
		t.Error("a request with only trusted inputs was marked tainted")
	}
}

func TestWriteAndCredentialRoles(t *testing.T) {
	runCases(t, []ruleCase{
		{"worker writes", request(worker, KindWrite, "app", ""), Facts{}, Cleared, "write.worker-only"},
		{"supervisor never writes", request(supervisor, KindWrite, "app", ""), Facts{}, Denied, "write.worker-only"},
		{"researcher never writes", request(researcher, KindWrite, "app", ""), Facts{}, Denied, "write.worker-only"},
		{"reviewer never writes", request(reviewer, KindWrite, "app", ""), Facts{}, Denied, "write.worker-only"},
		{"gateway uses a credential", request(gateway, KindCredentialUse, "", "github"), Facts{}, Cleared, "credential.gateway-only"},
		{"worker never uses a credential", request(worker, KindCredentialUse, "", "github"), Facts{}, Denied, "credential.gateway-only"},
		{"supervisor never uses a credential", request(supervisor, KindCredentialUse, "", "github"), Facts{}, Denied, "credential.gateway-only"},
		{"owner approval does not let an agent use one", request(worker, KindCredentialUse, "", "github"),
			Facts{Approvals: []Approval{approval(request(worker, KindCredentialUse, "", "github"), Facts{}, ByOwner)}}, Denied, "credential.gateway-only"},
	})
}

func TestMerge(t *testing.T) {
	r := request(supervisor, KindMerge, "app", "pr/3")
	ok := approval(r, greenFacts(), ByOwner)
	relay := approval(r, greenFacts(), ByClientViaOwner)
	other := approval(request(supervisor, KindMerge, "app", "pr/4"), greenFacts(), ByOwner)
	with := func(f Facts, mut func(*Facts)) Facts { mut(&f); return f }
	runCases(t, []ruleCase{
		{"worker never merges", request(worker, KindMerge, "app", "pr/3"), greenFacts(), Denied, "merge.role"},
		{"researcher never merges", request(researcher, KindMerge, "app", "pr/3"), greenFacts(), Denied, "merge.role"},
		{"reviewer never merges", request(reviewer, KindMerge, "app", "pr/3"), greenFacts(), Denied, "merge.role"},
		{"red checks", r, with(greenFacts(), func(f *Facts) { f.Checks = ChecksRed }), Denied, "merge.checks-green"},
		{"pending checks", r, with(greenFacts(), func(f *Facts) { f.Checks = ChecksPending }), Denied, "merge.checks-green"},
		{"unknown checks", r, Facts{Posture: PostureGreen, HeadSHA: head}, Denied, "merge.checks-green"},
		{"red even with the owner's approval", r, Facts{Posture: PostureOwner, Checks: ChecksRed, HeadSHA: head, Approvals: []Approval{ok}}, Denied, "merge.checks-green"},
		{"a required check unreported", r, with(greenFacts(), func(f *Facts) { f.RequiredUnreported = true }), Denied, "merge.checks-green"},
		{"unintended closing refs", r, with(greenFacts(), func(f *Facts) { f.UnintendedClosing = true }), Denied, "merge.closing-refs"},
		{"fixed list under green", r, with(greenFacts(), func(f *Facts) { f.Exceptions = []Exception{ExceptionSecurity} }), Holding, "merge.fixed-list"},
		{"fixed list approved", r, with(greenFacts(), func(f *Facts) {
			f.Exceptions, f.Approvals = []Exception{ExceptionDataMigration}, []Approval{ok}
		}), Cleared, "merge.posture"},
		{"fixed list approved, owner posture", r, Facts{Posture: PostureOwner, Checks: ChecksGreen, HeadSHA: head,
			Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{ok}}, Cleared, "merge.posture"},
		{"fixed list, client posture: the owner's approval alone is not enough", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head,
			Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{ok}}, Holding, "merge.posture"},
		{"fixed list, client posture: the relay alone is not enough", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head,
			Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{relay}}, Holding, "merge.fixed-list"},
		{"fixed list, client posture: both", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head,
			Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{ok, relay}}, Cleared, "merge.posture"},
		{"head commit unknown", r, Facts{Posture: PostureGreen, Checks: ChecksGreen}, Denied, "merge.head-known"},
		{"a new push invalidates the owner's approval", r, Facts{Posture: PostureOwner, Checks: ChecksGreen,
			HeadSHA: "2222222222222222222222222222222222222222", Approvals: []Approval{ok}}, Denied, "merge.posture"},
		{"a new push invalidates a fixed-list approval", r, Facts{Posture: PostureGreen, Checks: ChecksGreen,
			HeadSHA: "2222222222222222222222222222222222222222", Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{ok}}, Denied, "merge.fixed-list"},
		{"a moved base invalidates the relay", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head, BaseSHA: "3333",
			Approvals: []Approval{relay}}, Denied, "merge.posture"},
		{"fixed list: a client relay is not enough", r, with(greenFacts(), func(f *Facts) {
			f.Exceptions, f.Approvals = []Exception{ExceptionBilling}, []Approval{relay}
		}), Holding, "merge.fixed-list"},
		{"green posture merges", r, greenFacts(), Cleared, "merge.posture"},
		{"default posture is owner", r, Facts{Checks: ChecksGreen, HeadSHA: head}, Holding, "merge.posture"},
		{"owner posture", r, Facts{Posture: PostureOwner, Checks: ChecksGreen, HeadSHA: head}, Holding, "merge.posture"},
		{"owner posture approved", r, Facts{Posture: PostureOwner, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{ok}}, Cleared, "merge.posture"},
		{"approval for another PR", r, Facts{Posture: PostureOwner, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{other}}, Holding, "merge.posture"},
		{"approval without a decision id", r, Facts{Posture: PostureOwner, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{{By: ByOwner, ActionSHA256: ActionDigest(r)}}}, Holding, "merge.posture"},
		{"client posture", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head}, Holding, "merge.posture"},
		{"client posture: owner approval is not the client's", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{ok}}, Holding, "merge.posture"},
		{"client posture relayed", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{relay}}, Cleared, "merge.posture"},
		{"the daemon merges under green", request(daemon, KindMerge, "app", "pr/3"), greenFacts(), Cleared, "merge.posture"},
	})
	// A stale approval is denied with the reason, not silently held.
	moved := Facts{Posture: PostureOwner, Checks: ChecksGreen, HeadSHA: "2222222222222222222222222222222222222222", Approvals: []Approval{ok}}
	if d := Evaluate(r, moved); !strings.Contains(d.Reason, "the approval was for different facts") || len(d.ApprovalsUsed) != 0 {
		t.Errorf("stale approval: %+v", d)
	}
	// The fixed-list approval is recorded when the green posture then allows.
	fixed := with(greenFacts(), func(f *Facts) { f.Exceptions, f.Approvals = []Exception{ExceptionSecurity}, []Approval{ok} })
	if d := Evaluate(r, fixed); !reflect.DeepEqual(d.ApprovalsUsed, []string{ok.Decision}) {
		t.Errorf("fixed-list approvals used = %q, want [%q]", d.ApprovalsUsed, ok.Decision)
	}
	// A client-posture fixed-list merge records both approvals, the owner's first.
	capt, rel := ok, relay
	capt.Decision, rel.Decision = "d/1", "d/2"
	both := Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head, Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{rel, capt}}
	if d := Evaluate(r, both); d.Outcome != Cleared || !reflect.DeepEqual(d.ApprovalsUsed, []string{"d/1", "d/2"}) {
		t.Errorf("client fixed-list merge: %s, approvals used %q", d.Outcome, d.ApprovalsUsed)
	}
	// The owner posture's one approval, used by both rules, is recorded once.
	captFixed := Facts{Posture: PostureOwner, Checks: ChecksGreen, HeadSHA: head, Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{capt}}
	if d := Evaluate(r, captFixed); d.Outcome != Cleared || !reflect.DeepEqual(d.ApprovalsUsed, []string{"d/1"}) {
		t.Errorf("owner fixed-list merge: %s, approvals used %q", d.Outcome, d.ApprovalsUsed)
	}
	// A hold records none, even when an earlier rule accepted an approval.
	if d := Evaluate(r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head, Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{capt}}); d.Outcome != Holding || len(d.ApprovalsUsed) != 0 {
		t.Errorf("hold: %s, approvals used %q", d.Outcome, d.ApprovalsUsed)
	}
	// The approval used is recorded on the decision.
	if d := Evaluate(r, Facts{Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{other, ok}}); !reflect.DeepEqual(d.ApprovalsUsed, []string{ok.Decision}) {
		t.Errorf("approvals used = %q, want [%q]", d.ApprovalsUsed, ok.Decision)
	}
	// An approval binds to the actor too: the owner approving the supervisor's merge does not
	// approve the daemon's.
	if d := Evaluate(request(daemon, KindMerge, "app", "pr/3"), Facts{Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{ok}}); d.Outcome != Holding {
		t.Errorf("another actor reused an approval: %s by %s", d.Outcome, d.Rule)
	}
}

func TestDestructiveGit(t *testing.T) {
	facts := Facts{DefaultBranch: "main"}
	for _, k := range gitDestructive {
		r := request(worker, k, "app", "feature/x")
		runCases(t, []ruleCase{
			{string(k) + " by a worker", r, facts, Holding, "git.owner"},
			{string(k) + " approved", r, Facts{DefaultBranch: "main", Approvals: []Approval{approval(r, facts, ByOwner)}}, Cleared, "git.owner"},
			{string(k) + " approved under another default branch", r, Facts{DefaultBranch: "trunk", Approvals: []Approval{approval(r, facts, ByOwner)}}, Denied, "git.owner"},
			{string(k) + " of the default branch", request(worker, k, "app", "main"), facts, Denied, "git.default-branch"},
			{string(k) + " of refs/heads/main", request(worker, k, "app", "refs/heads/main"), facts, Denied, "git.default-branch"},
			{string(k) + " of the default branch, approved", request(worker, k, "app", "main"),
				Facts{DefaultBranch: "main", Approvals: []Approval{approval(request(worker, k, "app", "main"), facts, ByOwner)}}, Denied, "git.default-branch"},
			{string(k) + " with the default branch unknown", r, Facts{}, Denied, "git.default-branch"},
			{string(k) + " of Main", request(worker, k, "app", "Main"), facts, Denied, "git.default-branch"},
			{string(k) + " of refs/heads/MAIN", request(worker, k, "app", "refs/heads/MAIN"), facts, Denied, "git.default-branch"},
			{string(k) + " of heads/main", request(worker, k, "app", "heads/main"), facts, Denied, "request.valid"},
			{string(k) + " of refs/heads//main", request(worker, k, "app", "refs/heads//main"), facts, Denied, "request.valid"},
			{string(k) + " of +main", request(worker, k, "app", "+main"), facts, Denied, "request.valid"},
			{string(k) + " of HEAD:main", request(worker, k, "app", "HEAD:main"), facts, Denied, "request.valid"},
			{string(k) + " of x:refs/heads/main", request(worker, k, "app", "x:refs/heads/main"), facts, Denied, "request.valid"},
			{string(k) + " of main with a trailing space", request(worker, k, "app", "main "), facts, Denied, "request.valid"},
			{string(k) + " of an option", request(worker, k, "app", "-f"), facts, Denied, "request.valid"},
			{string(k) + " of refs/tags/v1", request(worker, k, "app", "refs/tags/v1"), facts, Denied, "request.valid"},
			{string(k) + " of a dot-dot name", request(worker, k, "app", "feature/../main"), facts, Denied, "request.valid"},
			{string(k) + " by a supervisor", request(supervisor, k, "app", "feature/x"), facts, Denied, "destructive.role"},
			{string(k) + " by a researcher", request(researcher, k, "app", "feature/x"), facts, Denied, "destructive.role"},
			{string(k) + " by the gateway", request(gateway, k, "app", "feature/x"), facts, Denied, "destructive.role"},
		})
	}
}

// deleteTree is a real directory tree for delete tests: a home with a project inside it and a
// scratch dir outside it, plus a symlink from the project to home.
func deleteTree(t *testing.T) (home, proj, scratch string, facts func(path string) Facts) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, proj, scratch = filepath.Join(base, "home"), filepath.Join(base, "home", "proj"), filepath.Join(base, "tmp")
	for _, d := range []string{filepath.Join(proj, "build"), filepath.Join(scratch, "out")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(home, filepath.Join(proj, "h")); err != nil {
		t.Fatal(err)
	}
	g, err := pathguard.New(home, pathguard.Root{Kind: pathguard.Project, Path: proj}, pathguard.Root{Kind: pathguard.Scratch, Path: scratch})
	if err != nil {
		t.Fatal(err)
	}
	facts = func(path string) Facts {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			resolved = ""
		}
		return Facts{Home: g.Home(), Roots: g.Roots(), ResolvedPath: resolved}
	}
	return home, proj, scratch, facts
}

func TestDelete(t *testing.T) {
	home, proj, scratch, facts := deleteTree(t)
	inScratch := filepath.Join(scratch, "out")
	inProj := filepath.Join(proj, "build")
	viaLink := filepath.Join(proj, "h")
	del := func(a Actor, p string) Request { return request(a, KindDelete, "app", p) }
	approved := func(r Request, f Facts) Facts { f.Approvals = []Approval{approval(r, f, ByOwner)}; return f }
	runCases(t, []ruleCase{
		{"worker deletes in scratch", del(worker, inScratch), facts(inScratch), Cleared, "delete.scratch"},
		{"daemon deletes in scratch", del(daemon, inScratch), facts(inScratch), Cleared, "delete.scratch"},
		{"worker deletes in the project", del(worker, inProj), facts(inProj), Holding, "delete.project"},
		{"approved project delete", del(worker, inProj), approved(del(worker, inProj), facts(inProj)), Cleared, "delete.project"},
		{"a changed resolved path invalidates the approval", del(worker, inProj), func() Facts {
			f := facts(filepath.Join(proj, "other"))
			f.ResolvedPath = filepath.Join(proj, "other")
			f.Approvals = approved(del(worker, inProj), facts(inProj)).Approvals
			return f
		}(), Denied, "delete.project"},
		{"the root /", del(worker, "/"), Facts{Home: home, ResolvedPath: "/"}, Denied, "delete.path-guard"},
		{"home", del(worker, home), facts(home), Denied, "delete.path-guard"},
		{"a link to home", del(worker, viaLink), facts(viaLink), Denied, "delete.path-guard"},
		{"a link to home, approved", del(worker, viaLink), approved(del(worker, viaLink), facts(viaLink)), Denied, "delete.path-guard"},
		{"a link to home, trailing slash", del(worker, viaLink+"/"), facts(viaLink + "/"), Denied, "delete.path-guard"},
		{"the project root itself", del(worker, proj), facts(proj), Denied, "delete.path-guard"},
		{"the scratch root itself", del(worker, scratch), facts(scratch), Denied, "delete.path-guard"},
		{"dot-dot out of the project", del(worker, proj+"/build/../.."), facts(proj + "/build/../.."), Denied, "delete.path-guard"},
		{"unresolved path", del(worker, inScratch), Facts{Home: home}, Denied, "delete.path-guard"},
		{"no roots declared", del(worker, inScratch), Facts{Home: home, ResolvedPath: inScratch}, Denied, "delete.path-guard"},
		{"no home known", del(worker, inScratch), Facts{Roots: facts(inScratch).Roots, ResolvedPath: inScratch}, Denied, "delete.path-guard"},
		{"the resolved path decides the root", del(worker, inProj), facts(inScratch), Cleared, "delete.scratch"},
		{"supervisor deletes", del(supervisor, inScratch), facts(inScratch), Denied, "destructive.role"},
		{"researcher deletes", del(researcher, inScratch), facts(inScratch), Denied, "destructive.role"},
	})
}

func TestDefaultDeny(t *testing.T) {
	// No rule allows anything for an unknown-but-valid combination; the table ends in a deny.
	last := rules[len(rules)-1]
	if last.ID != "default.deny" {
		t.Fatalf("the last rule is %q, want default.deny", last.ID)
	}
	if v, ok := last.eval(&evalCtx{}); !ok || v.outcome != Denied {
		t.Fatal("default.deny does not deny")
	}
}

func TestRuleIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range CurrentRuleSet().Rules {
		if seen[r.ID] || r.ID == "" || r.Summary == "" {
			t.Errorf("rule %q is duplicated or undocumented", r.ID)
		}
		seen[r.ID] = true
	}
}

// namesOwnerByID reports whether s calls the human "owner" in prose rather than {owner}; "the owner
// posture" names the posture, not the person.
func namesOwnerByID(s string) bool {
	s = strings.NewReplacer("{owner}", "", "owner posture", "").Replace(s)
	return strings.Contains(s, "owner")
}

// Reasons and summaries that name the human say {owner}, so a surface can render the configured
// display title in its place.
func TestReasonsNameTheOwnerByPlaceholder(t *testing.T) {
	for _, r := range CurrentRuleSet().Rules {
		if namesOwnerByID(r.Summary) {
			t.Errorf("rule %s summary names the owner by role id: %q", r.ID, r.Summary)
		}
	}
	f := Facts{Posture: PostureOwner, Checks: ChecksGreen, HeadSHA: head, DefaultBranch: "main"}
	merge := request(daemon, KindMerge, "app", "pr/12")
	client := Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head}
	cases := map[string]struct {
		req   Request
		facts Facts
		want  string
	}{
		"owner posture holds":   {merge, f, "the owner posture merges only on the word of the {owner}"},
		"owner posture cleared": {merge, Facts{Posture: PostureOwner, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{approval(merge, f, ByOwner)}}, "the {owner} approved this merge"},
		"client posture holds":  {merge, client, "the client posture merges only after the {owner} relays the client's confirmation"},
		"the owner writes":      {request(owner, KindWrite, "app", ""), Facts{}, "the {owner} never writes to a project"},
		"the owner uses a key":  {request(owner, KindCredentialUse, "", "deploy-key"), Facts{}, "the {owner} may not use credentials; only the gateway does"},
		"an owner in an org":    {request(Actor{ID: "owner", Role: RoleOwner, Org: "acme"}, KindRead, "", ""), Facts{}, "invalid request: the {owner} belongs to no org"},
		"a worker merges":       {request(worker, KindMerge, "app", "pr/12"), f, "a worker never merges"},
	}
	for name, c := range cases {
		d := Evaluate(c.req, c.facts)
		if d.Reason != c.want {
			t.Errorf("%s: reason %q, want %q", name, d.Reason, c.want)
		}
		if namesOwnerByID(d.Reason) {
			t.Errorf("%s: reason names the owner by role id: %q", name, d.Reason)
		}
	}
}

// TestExecAndFetchDenied: a shell command or a network fetch is decided, and no rule allows either
// yet, for any role: the default deny refuses them, and outside text alone is refused first.
func TestExecAndFetchDenied(t *testing.T) {
	agentOnly := Input{Source: SourceAgent, Ref: "tool/Bash"}
	runCases(t, []ruleCase{
		{"worker exec", request(worker, KindExec, "app", "go test ./..."), Facts{}, Denied, "default.deny"},
		{"worker fetch", request(worker, KindFetch, "app", "https://example.com"), Facts{}, Denied, "default.deny"},
		{"owner exec", request(Actor{ID: "o", Role: RoleOwner}, KindExec, "app", "ls"), Facts{}, Denied, "default.deny"},
		{"exec on agent text alone", request(worker, KindExec, "app", "ls", agentOnly), Facts{}, Denied, "taint.trusted-basis"},
	})
}

// TestContainer covers LUCY-SBX-003: starting, restarting or stopping an org's container stops
// the work in it, so with live sessions it needs the owner's confirmation for exactly those
// sessions, and no agent may ask at all.
func TestContainer(t *testing.T) {
	daemonInput := Input{Source: SourceDaemon, Ref: "sandbox/acme"}
	restart := request(daemon, KindContainer, "", "restart", daemonInput)
	none := Facts{SessionsKnown: true}
	live := Facts{SessionsKnown: true, LiveSessions: []string{"f1#1", "f2#1"}}
	confirmed := live
	confirmed.Approvals = []Approval{approval(restart, live, ByOwner)}
	// The owner confirmed while one session was live; a second one has started since.
	staleFacts := Facts{SessionsKnown: true, LiveSessions: []string{"f1#1"}}
	stale := live
	stale.Approvals = []Approval{approval(restart, staleFacts, ByOwner)}
	// A confirmation of a restart does not confirm a stop.
	down := request(daemon, KindContainer, "", "down", daemonInput)
	otherOp := live
	otherOp.Approvals = []Approval{approval(down, live, ByOwner)}
	clientOnly := live
	clientOnly.Approvals = []Approval{approval(restart, live, ByClientViaOwner)}
	runCases(t, []ruleCase{
		{"no live session", restart, none, Cleared, "container.live-sessions"},
		{"up with no live session", request(daemon, KindContainer, "", "up", daemonInput), none, Cleared, "container.live-sessions"},
		{"owner asks, no live session", request(owner, KindContainer, "", "down", Input{Source: SourceOwner}), none, Cleared, "container.live-sessions"},
		{"live sessions hold for the owner", restart, live, Holding, "container.live-sessions"},
		{"the owner confirmed these sessions", restart, confirmed, Cleared, "container.live-sessions"},
		{"a confirmation for other sessions", restart, stale, Denied, "container.live-sessions"},
		{"a confirmation of another operation", restart, otherOp, Holding, "container.live-sessions"},
		{"a relayed client confirmation is not the owner's", restart, clientOnly, Holding, "container.live-sessions"},
		{"sessions unknown", restart, Facts{}, Denied, "container.live-sessions"},
		{"sessions listed but not known", restart, Facts{LiveSessions: []string{"f1#1"}}, Denied, "facts.valid"},
		{"unsorted sessions", restart, Facts{SessionsKnown: true, LiveSessions: []string{"f2#1", "f1#1"}}, Denied, "facts.valid"},
		{"duplicate sessions", restart, Facts{SessionsKnown: true, LiveSessions: []string{"f1#1", "f1#1"}}, Denied, "facts.valid"},
		{"an empty session", restart, Facts{SessionsKnown: true, LiveSessions: []string{""}}, Denied, "facts.valid"},
		{"unknown operation", request(daemon, KindContainer, "", "destroy", daemonInput), none, Denied, "request.valid"},
		{"no operation", request(daemon, KindContainer, "", "", daemonInput), none, Denied, "request.valid"},
		{"a worker never restarts its container", request(worker, KindContainer, "", "restart"), none, Denied, "container.role"},
		{"nor does the supervisor", request(supervisor, KindContainer, "", "down"), none, Denied, "container.role"},
		{"nor does the gateway", request(gateway, KindContainer, "", "up"), none, Denied, "container.role"},
		{"outside text alone", request(daemon, KindContainer, "", "restart", issueInput), none, Denied, "taint.trusted-basis"},
	})
	many := Facts{SessionsKnown: true}
	for i := 0; i <= MaxFactItems; i++ {
		many.LiveSessions = append(many.LiveSessions, fmt.Sprintf("f%03d#1", i))
	}
	runCases(t, []ruleCase{{"too many sessions", restart, many, Denied, "facts.valid"}})
}

// TestContainerFactsDigest checks that a container confirmation binds to the live sessions and to
// nothing else the daemon happens to know.
func TestContainerFactsDigest(t *testing.T) {
	a := Facts{SessionsKnown: true, LiveSessions: []string{"f1#1"}}
	b := Facts{SessionsKnown: true, LiveSessions: []string{"f1#1", "f2#1"}}
	if FactsDigest(KindContainer, a) == FactsDigest(KindContainer, b) {
		t.Error("a new live session did not change the bound facts")
	}
	c := a
	c.HeadSHA, c.Posture = head, PostureGreen
	if FactsDigest(KindContainer, a) != FactsDigest(KindContainer, c) {
		t.Error("facts a container operation does not bind to changed its digest")
	}
	// {"kind":"container","facts":{"live_sessions":"f1#1"}}, as the digest's documented shape.
	sum := sha256.Sum256([]byte(`{"kind":"container","facts":{"live_sessions":"f1#1"}}`))
	if got, want := FactsDigest(KindContainer, a), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("FactsDigest = %s, want %s", got, want)
	}
}

// The actor role's AgentRole blockedPaths deny a write or a delete of a matching path, relative to
// the checkout, whatever rule would otherwise allow it; reads and paths that match nothing are
// unaffected (LUCY-SPEC-015).
func TestBlockedPaths(t *testing.T) {
	root := "/work/acme/web/slot-1"
	f := Facts{BlockedPaths: []string{".github/workflows/**", "**/secrets/**", "*.pem"}, WorkRoot: root}
	for _, c := range []struct {
		name    string
		kind    Kind
		path    string
		outcome Outcome
		rule    string
	}{
		{"a workflow", KindWrite, root + "/.github/workflows/ci.yml", Denied, "role.blocked-paths"},
		{"a nested secret", KindWrite, root + "/app/config/secrets/key.txt", Denied, "role.blocked-paths"},
		{"a top-level pem", KindWrite, root + "/server.pem", Denied, "role.blocked-paths"},
		{"a pem deeper down", KindWrite, root + "/certs/server.pem", Cleared, "write.worker-only"},
		{"an ordinary file", KindWrite, root + "/main.go", Cleared, "write.worker-only"},
		{"a read of a blocked path", KindRead, root + "/.github/workflows/ci.yml", Cleared, "read.in-org"},
		{"a blocked path outside the checkout", KindWrite, "/elsewhere/app/secrets/x", Denied, "role.blocked-paths"},
	} {
		d := Evaluate(request(worker, c.kind, "web", c.path), f)
		if d.Outcome != c.outcome || d.Rule != c.rule {
			t.Errorf("%s: %s by %s (%s), want %s by %s", c.name, d.Outcome, d.Rule, d.Reason, c.outcome, c.rule)
		}
	}
	// A delete inside a scratch root that the path guard would allow is still denied when blocked.
	scratch := t.TempDir()
	df := Facts{BlockedPaths: []string{"**/secrets/**"}, Home: "/home/nobody", Roots: []pathguard.Root{{Kind: pathguard.Scratch, Path: scratch}},
		ResolvedPath: scratch + "/secrets/key", WorkRoot: scratch}
	if d := Evaluate(request(worker, KindDelete, "web", scratch+"/secrets/key"), df); d.Outcome != Denied || d.Rule != "role.blocked-paths" {
		t.Errorf("a blocked delete: %s by %s (%s)", d.Outcome, d.Rule, d.Reason)
	}
	df.BlockedPaths = nil
	if d := Evaluate(request(worker, KindDelete, "web", scratch+"/secrets/key"), df); d.Outcome != Cleared {
		t.Errorf("the same delete, nothing blocked: %s by %s (%s)", d.Outcome, d.Rule, d.Reason)
	}
	for _, bad := range []Facts{{BlockedPaths: []string{""}}, {BlockedPaths: []string{"a//b"}}, {BlockedPaths: []string{"[x"}}, {WorkRoot: "relative"}} {
		if d := Evaluate(request(worker, KindWrite, "web", root+"/x"), bad); d.Outcome != Denied || d.Rule != "facts.valid" {
			t.Errorf("facts %+v: %s by %s", bad, d.Outcome, d.Rule)
		}
	}
}

func TestMatchGlob(t *testing.T) {
	for _, c := range []struct {
		g, p string
		want bool
	}{
		{"**", "a/b/c", true},
		{"**/x", "x", true},
		{"**/x", "a/b/x", true},
		{"a/**", "a", true},
		{"a/**/z", "a/z", true},
		{"a/**/z", "a/b/c/z", true},
		{"a/*/z", "a/b/c/z", false},
		{"*.go", "dir/main.go", false},
		{".env*", ".env.local", true},
	} {
		if got := matchGlob(c.g, c.p); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.g, c.p, got, c.want)
		}
	}
}
