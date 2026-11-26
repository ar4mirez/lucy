package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/pathguard"
)

var (
	captain    = Actor{ID: "captain", Role: RoleCaptain}
	daemon     = Actor{ID: "daemon", Role: RoleDaemon}
	supervisor = Actor{ID: "org/acme/supervisor", Role: RoleSupervisor, Org: "acme"}
	worker     = Actor{ID: "org/acme/worker/7", Role: RoleWorker, Org: "acme"}
	scout      = Actor{ID: "org/acme/scout/2", Role: RoleScout, Org: "acme"}
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

// approval is the captain's (or the relayed client's) approval of r, made under facts f.
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
		{"unknown role", request(Actor{ID: "x", Role: "admin", Org: "acme"}, KindRead, "", ""), Facts{}, Deny, "request.valid"},
		{"unknown kind", request(worker, "exec", "app", "ls"), Facts{}, Deny, "request.valid"},
		{"no actor id", request(Actor{Role: RoleWorker, Org: "acme"}, KindRead, "", ""), Facts{}, Deny, "request.valid"},
		{"org-bound actor without org", request(Actor{ID: "w", Role: RoleWorker}, KindRead, "", ""), Facts{}, Deny, "request.valid"},
		{"captain claiming an org", request(Actor{ID: "c", Role: RoleCaptain, Org: "acme"}, KindRead, "", ""), Facts{}, Deny, "request.valid"},
		{"no target org", Request{Actor: worker, Kind: KindRead, Inputs: []Input{taskInput}}, Facts{}, Deny, "request.valid"},
		{"merge without a PR", request(supervisor, KindMerge, "app", ""), greenFacts(), Deny, "request.valid"},
		{"relative delete", request(worker, KindDelete, "app", "tmp/x"), Facts{}, Deny, "request.valid"},
		{"unknown source", request(worker, KindRead, "", "", Input{Source: "captain_said_so"}), Facts{}, Deny, "request.valid"},
		{"long label", request(worker, KindRead, long, ""), Facts{}, Deny, "request.valid"},
		{"control character", request(worker, KindWrite, "app\n", ""), Facts{}, Deny, "request.valid"},
		{"too many inputs", request(worker, KindRead, "", "", many...), Facts{}, Deny, "request.valid"},
		{"unknown posture", request(supervisor, KindMerge, "app", "pr/1"), Facts{Posture: "yolo", Checks: ChecksGreen}, Deny, "facts.valid"},
		{"unknown check state", request(supervisor, KindMerge, "app", "pr/1"), Facts{Posture: PostureGreen, Checks: "mostly"}, Deny, "facts.valid"},
		{"unknown exception", request(supervisor, KindMerge, "app", "pr/1"), Facts{Posture: PostureGreen, Checks: ChecksGreen, Exceptions: []Exception{"fun"}}, Deny, "facts.valid"},
		{"unknown approval kind", request(supervisor, KindMerge, "app", "pr/1"), Facts{Approvals: []Approval{{Decision: "d", By: "agent"}}}, Deny, "facts.valid"},
	})
}

func TestOrgIsolation(t *testing.T) {
	other := Request{Actor: worker, Kind: KindRead, Target: Target{Org: "globex"}, Inputs: []Input{taskInput}}
	runCases(t, []ruleCase{
		{"worker reads another org", other, Facts{}, Deny, "org.isolation"},
		{"supervisor merges in another org", Request{Actor: supervisor, Kind: KindMerge, Target: Target{Org: "globex", Project: "app", Resource: "pr/1"}, Inputs: []Input{taskInput}}, greenFacts(), Deny, "org.isolation"},
		{"gateway uses another org's credential", Request{Actor: gateway, Kind: KindCredentialUse, Target: Target{Org: "globex", Resource: "github"}, Inputs: []Input{taskInput}}, Facts{}, Deny, "org.isolation"},
		{"worker reads its own org", request(worker, KindRead, "app", ""), Facts{}, Allow, "read.in-org"},
		{"captain reads any org", Request{Actor: captain, Kind: KindRead, Target: Target{Org: "globex"}}, Facts{}, Allow, "read.in-org"},
	})
}

func TestTaint(t *testing.T) {
	merge := func(inputs ...Input) Request { return request(supervisor, KindMerge, "app", "pr/3", inputs...) }
	untrusted := []Input{issueInput, {Source: SourcePRComment, Ref: "c/1"}, {Source: SourceWeb}, {Source: SourceToolOutput}, {Source: SourceAgent}, {Source: SourceRepoContent}, {Source: SourceEmail}, {Source: SourcePRBody}}
	runCases(t, []ruleCase{
		{"merge on untrusted text only", merge(untrusted...), greenFacts(), Deny, "taint.trusted-basis"},
		{"merge with no inputs at all", Request{Actor: supervisor, Kind: KindMerge, Target: Target{Org: "acme", Project: "app", Resource: "pr/3"}}, greenFacts(), Deny, "taint.trusted-basis"},
		{"delete on an issue's say-so", request(worker, KindDelete, "app", "/w/x", issueInput), Facts{}, Deny, "taint.trusted-basis"},
		{"credential use on a PR comment", request(gateway, KindCredentialUse, "", "github", Input{Source: SourcePRComment}), Facts{}, Deny, "taint.trusted-basis"},
		{"untrusted text beside a task", merge(issueInput, taskInput), greenFacts(), Allow, "merge.posture"},
		{"reads need no trusted basis", request(worker, KindRead, "", "", issueInput), Facts{}, Allow, "read.in-org"},
	})
	// An issue that claims the captain approved, or that names an approval, still is not one:
	// approvals only arrive as facts.
	r := merge(issueInput, Input{Source: SourceIssue, Ref: "decision/9"}, taskInput)
	if d := Evaluate(r, Facts{Checks: ChecksGreen, HeadSHA: head}); d.Outcome != RequireCaptain || !d.Tainted {
		t.Errorf("an issue claiming approval: got %s by %s, tainted %v", d.Outcome, d.Rule, d.Tainted)
	}
	if d := Evaluate(merge(taskInput), greenFacts()); d.Tainted {
		t.Error("a request with only trusted inputs was marked tainted")
	}
}

func TestWriteAndCredentialRoles(t *testing.T) {
	runCases(t, []ruleCase{
		{"worker writes", request(worker, KindWrite, "app", ""), Facts{}, Allow, "write.worker-only"},
		{"supervisor never writes", request(supervisor, KindWrite, "app", ""), Facts{}, Deny, "write.worker-only"},
		{"scout never writes", request(scout, KindWrite, "app", ""), Facts{}, Deny, "write.worker-only"},
		{"reviewer never writes", request(reviewer, KindWrite, "app", ""), Facts{}, Deny, "write.worker-only"},
		{"gateway uses a credential", request(gateway, KindCredentialUse, "", "github"), Facts{}, Allow, "credential.gateway-only"},
		{"worker never uses a credential", request(worker, KindCredentialUse, "", "github"), Facts{}, Deny, "credential.gateway-only"},
		{"supervisor never uses a credential", request(supervisor, KindCredentialUse, "", "github"), Facts{}, Deny, "credential.gateway-only"},
		{"captain approval does not let an agent use one", request(worker, KindCredentialUse, "", "github"),
			Facts{Approvals: []Approval{approval(request(worker, KindCredentialUse, "", "github"), Facts{}, ByCaptain)}}, Deny, "credential.gateway-only"},
	})
}

func TestMerge(t *testing.T) {
	r := request(supervisor, KindMerge, "app", "pr/3")
	ok := approval(r, greenFacts(), ByCaptain)
	relay := approval(r, greenFacts(), ByClientViaCaptain)
	other := approval(request(supervisor, KindMerge, "app", "pr/4"), greenFacts(), ByCaptain)
	with := func(f Facts, mut func(*Facts)) Facts { mut(&f); return f }
	runCases(t, []ruleCase{
		{"worker never merges", request(worker, KindMerge, "app", "pr/3"), greenFacts(), Deny, "merge.role"},
		{"scout never merges", request(scout, KindMerge, "app", "pr/3"), greenFacts(), Deny, "merge.role"},
		{"reviewer never merges", request(reviewer, KindMerge, "app", "pr/3"), greenFacts(), Deny, "merge.role"},
		{"red checks", r, with(greenFacts(), func(f *Facts) { f.Checks = ChecksRed }), Deny, "merge.checks-green"},
		{"pending checks", r, with(greenFacts(), func(f *Facts) { f.Checks = ChecksPending }), Deny, "merge.checks-green"},
		{"unknown checks", r, Facts{Posture: PostureGreen, HeadSHA: head}, Deny, "merge.checks-green"},
		{"red even with the captain's approval", r, Facts{Posture: PostureCaptain, Checks: ChecksRed, HeadSHA: head, Approvals: []Approval{ok}}, Deny, "merge.checks-green"},
		{"a required check unreported", r, with(greenFacts(), func(f *Facts) { f.RequiredUnreported = true }), Deny, "merge.checks-green"},
		{"unintended closing refs", r, with(greenFacts(), func(f *Facts) { f.UnintendedClosing = true }), Deny, "merge.closing-refs"},
		{"fixed list under green", r, with(greenFacts(), func(f *Facts) { f.Exceptions = []Exception{ExceptionSecurity} }), RequireCaptain, "merge.fixed-list"},
		{"fixed list approved", r, with(greenFacts(), func(f *Facts) {
			f.Exceptions, f.Approvals = []Exception{ExceptionDataMigration}, []Approval{ok}
		}), Allow, "merge.posture"},
		{"fixed list approved, captain posture", r, Facts{Posture: PostureCaptain, Checks: ChecksGreen, HeadSHA: head,
			Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{ok}}, Allow, "merge.posture"},
		{"fixed list, client posture: the captain's approval alone is not enough", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head,
			Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{ok}}, RequireCaptain, "merge.posture"},
		{"fixed list, client posture: the relay alone is not enough", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head,
			Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{relay}}, RequireCaptain, "merge.fixed-list"},
		{"fixed list, client posture: both", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head,
			Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{ok, relay}}, Allow, "merge.posture"},
		{"head commit unknown", r, Facts{Posture: PostureGreen, Checks: ChecksGreen}, Deny, "merge.head-known"},
		{"a new push invalidates the captain's approval", r, Facts{Posture: PostureCaptain, Checks: ChecksGreen,
			HeadSHA: "2222222222222222222222222222222222222222", Approvals: []Approval{ok}}, Deny, "merge.posture"},
		{"a new push invalidates a fixed-list approval", r, Facts{Posture: PostureGreen, Checks: ChecksGreen,
			HeadSHA: "2222222222222222222222222222222222222222", Exceptions: []Exception{ExceptionSecurity}, Approvals: []Approval{ok}}, Deny, "merge.fixed-list"},
		{"a moved base invalidates the relay", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head, BaseSHA: "3333",
			Approvals: []Approval{relay}}, Deny, "merge.posture"},
		{"fixed list: a client relay is not enough", r, with(greenFacts(), func(f *Facts) {
			f.Exceptions, f.Approvals = []Exception{ExceptionBilling}, []Approval{relay}
		}), RequireCaptain, "merge.fixed-list"},
		{"green posture merges", r, greenFacts(), Allow, "merge.posture"},
		{"default posture is captain", r, Facts{Checks: ChecksGreen, HeadSHA: head}, RequireCaptain, "merge.posture"},
		{"captain posture", r, Facts{Posture: PostureCaptain, Checks: ChecksGreen, HeadSHA: head}, RequireCaptain, "merge.posture"},
		{"captain posture approved", r, Facts{Posture: PostureCaptain, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{ok}}, Allow, "merge.posture"},
		{"approval for another PR", r, Facts{Posture: PostureCaptain, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{other}}, RequireCaptain, "merge.posture"},
		{"approval without a decision id", r, Facts{Posture: PostureCaptain, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{{By: ByCaptain, ActionSHA256: ActionDigest(r)}}}, RequireCaptain, "merge.posture"},
		{"client posture", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head}, RequireCaptain, "merge.posture"},
		{"client posture: captain approval is not the client's", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{ok}}, RequireCaptain, "merge.posture"},
		{"client posture relayed", r, Facts{Posture: PostureClient, Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{relay}}, Allow, "merge.posture"},
		{"the daemon merges under green", request(daemon, KindMerge, "app", "pr/3"), greenFacts(), Allow, "merge.posture"},
	})
	// A stale approval is denied with the reason, not silently escalated.
	moved := Facts{Posture: PostureCaptain, Checks: ChecksGreen, HeadSHA: "2222222222222222222222222222222222222222", Approvals: []Approval{ok}}
	if d := Evaluate(r, moved); !strings.Contains(d.Reason, "the approval was for different facts") || d.Approval != "" {
		t.Errorf("stale approval: %+v", d)
	}
	// The fixed-list approval is recorded when the green posture then allows.
	fixed := with(greenFacts(), func(f *Facts) { f.Exceptions, f.Approvals = []Exception{ExceptionSecurity}, []Approval{ok} })
	if d := Evaluate(r, fixed); d.Approval != ok.Decision {
		t.Errorf("fixed-list approval used = %q, want %q", d.Approval, ok.Decision)
	}
	// The approval used is recorded on the decision.
	if d := Evaluate(r, Facts{Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{other, ok}}); d.Approval != ok.Decision {
		t.Errorf("approval used = %q, want %q", d.Approval, ok.Decision)
	}
	// An approval binds to the actor too: the captain approving the supervisor's merge does not
	// approve the daemon's.
	if d := Evaluate(request(daemon, KindMerge, "app", "pr/3"), Facts{Checks: ChecksGreen, HeadSHA: head, Approvals: []Approval{ok}}); d.Outcome != RequireCaptain {
		t.Errorf("another actor reused an approval: %s by %s", d.Outcome, d.Rule)
	}
}

func TestDestructiveGit(t *testing.T) {
	facts := Facts{DefaultBranch: "main"}
	for _, k := range gitDestructive {
		r := request(worker, k, "app", "feature/x")
		runCases(t, []ruleCase{
			{string(k) + " by a worker", r, facts, RequireCaptain, "git.captain"},
			{string(k) + " approved", r, Facts{DefaultBranch: "main", Approvals: []Approval{approval(r, facts, ByCaptain)}}, Allow, "git.captain"},
			{string(k) + " approved under another default branch", r, Facts{DefaultBranch: "trunk", Approvals: []Approval{approval(r, facts, ByCaptain)}}, Deny, "git.captain"},
			{string(k) + " of the default branch", request(worker, k, "app", "main"), facts, Deny, "git.default-branch"},
			{string(k) + " of refs/heads/main", request(worker, k, "app", "refs/heads/main"), facts, Deny, "git.default-branch"},
			{string(k) + " of the default branch, approved", request(worker, k, "app", "main"),
				Facts{DefaultBranch: "main", Approvals: []Approval{approval(request(worker, k, "app", "main"), facts, ByCaptain)}}, Deny, "git.default-branch"},
			{string(k) + " with the default branch unknown", r, Facts{}, Deny, "git.default-branch"},
			{string(k) + " of Main", request(worker, k, "app", "Main"), facts, Deny, "git.default-branch"},
			{string(k) + " of refs/heads/MAIN", request(worker, k, "app", "refs/heads/MAIN"), facts, Deny, "git.default-branch"},
			{string(k) + " of heads/main", request(worker, k, "app", "heads/main"), facts, Deny, "request.valid"},
			{string(k) + " of refs/heads//main", request(worker, k, "app", "refs/heads//main"), facts, Deny, "request.valid"},
			{string(k) + " of +main", request(worker, k, "app", "+main"), facts, Deny, "request.valid"},
			{string(k) + " of HEAD:main", request(worker, k, "app", "HEAD:main"), facts, Deny, "request.valid"},
			{string(k) + " of x:refs/heads/main", request(worker, k, "app", "x:refs/heads/main"), facts, Deny, "request.valid"},
			{string(k) + " of main with a trailing space", request(worker, k, "app", "main "), facts, Deny, "request.valid"},
			{string(k) + " of an option", request(worker, k, "app", "-f"), facts, Deny, "request.valid"},
			{string(k) + " of refs/tags/v1", request(worker, k, "app", "refs/tags/v1"), facts, Deny, "request.valid"},
			{string(k) + " of a dot-dot name", request(worker, k, "app", "feature/../main"), facts, Deny, "request.valid"},
			{string(k) + " by a supervisor", request(supervisor, k, "app", "feature/x"), facts, Deny, "destructive.role"},
			{string(k) + " by a scout", request(scout, k, "app", "feature/x"), facts, Deny, "destructive.role"},
			{string(k) + " by the gateway", request(gateway, k, "app", "feature/x"), facts, Deny, "destructive.role"},
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
	approved := func(r Request, f Facts) Facts { f.Approvals = []Approval{approval(r, f, ByCaptain)}; return f }
	runCases(t, []ruleCase{
		{"worker deletes in scratch", del(worker, inScratch), facts(inScratch), Allow, "delete.scratch"},
		{"daemon deletes in scratch", del(daemon, inScratch), facts(inScratch), Allow, "delete.scratch"},
		{"worker deletes in the project", del(worker, inProj), facts(inProj), RequireCaptain, "delete.project"},
		{"approved project delete", del(worker, inProj), approved(del(worker, inProj), facts(inProj)), Allow, "delete.project"},
		{"a changed resolved path invalidates the approval", del(worker, inProj), func() Facts {
			f := facts(filepath.Join(proj, "other"))
			f.ResolvedPath = filepath.Join(proj, "other")
			f.Approvals = approved(del(worker, inProj), facts(inProj)).Approvals
			return f
		}(), Deny, "delete.project"},
		{"the root /", del(worker, "/"), Facts{Home: home, ResolvedPath: "/"}, Deny, "delete.path-guard"},
		{"home", del(worker, home), facts(home), Deny, "delete.path-guard"},
		{"a link to home", del(worker, viaLink), facts(viaLink), Deny, "delete.path-guard"},
		{"a link to home, approved", del(worker, viaLink), approved(del(worker, viaLink), facts(viaLink)), Deny, "delete.path-guard"},
		{"a link to home, trailing slash", del(worker, viaLink+"/"), facts(viaLink + "/"), Deny, "delete.path-guard"},
		{"the project root itself", del(worker, proj), facts(proj), Deny, "delete.path-guard"},
		{"the scratch root itself", del(worker, scratch), facts(scratch), Deny, "delete.path-guard"},
		{"dot-dot out of the project", del(worker, proj+"/build/../.."), facts(proj + "/build/../.."), Deny, "delete.path-guard"},
		{"unresolved path", del(worker, inScratch), Facts{Home: home}, Deny, "delete.path-guard"},
		{"no roots declared", del(worker, inScratch), Facts{Home: home, ResolvedPath: inScratch}, Deny, "delete.path-guard"},
		{"no home known", del(worker, inScratch), Facts{Roots: facts(inScratch).Roots, ResolvedPath: inScratch}, Deny, "delete.path-guard"},
		{"the resolved path decides the root", del(worker, inProj), facts(inScratch), Allow, "delete.scratch"},
		{"supervisor deletes", del(supervisor, inScratch), facts(inScratch), Deny, "destructive.role"},
		{"scout deletes", del(scout, inScratch), facts(inScratch), Deny, "destructive.role"},
	})
}

func TestDefaultDeny(t *testing.T) {
	// No rule allows anything for an unknown-but-valid combination; the table ends in a deny.
	last := rules[len(rules)-1]
	if last.ID != "default.deny" {
		t.Fatalf("the last rule is %q, want default.deny", last.ID)
	}
	if v, ok := last.eval(&evalCtx{}); !ok || v.outcome != Deny {
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
