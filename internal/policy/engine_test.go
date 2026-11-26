package policy

import (
	"bufio"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/pathguard"
)

var update = flag.Bool("update", false, "record this rule set version's hash in testdata/ruleset.sum")

// TestRuleSetVersioned locks the rule set: testdata/ruleset.sum holds one "version sha256" line per
// rule set version ever released. Changing rules.go or trust.go changes the hash, which fails here
// until RuleSetVersion is bumped and the new line recorded with -update. An old version's hash can
// never be replaced.
func TestRuleSetVersioned(t *testing.T) {
	const path = "testdata/ruleset.sum"
	known := map[int]string{}
	f, err := os.Open(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if f != nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			v, sum, ok := strings.Cut(sc.Text(), " ")
			n, err := strconv.Atoi(v)
			if !ok || err != nil {
				t.Fatalf("%s: bad line %q", path, sc.Text())
			}
			known[n] = sum
		}
		f.Close()
	}
	got, ok := known[RuleSetVersion]
	switch {
	case ok && got == ruleSetSHA256:
		return
	case ok:
		t.Fatalf("the rule set changed under version %d (recorded %s, now %s): bump RuleSetVersion, then run go test ./internal/policy -run TestRuleSetVersioned -update",
			RuleSetVersion, got, ruleSetSHA256)
	case !*update:
		t.Fatalf("rule set version %d has no recorded hash: run go test ./internal/policy -run TestRuleSetVersioned -update", RuleSetVersion)
	}
	for v := range known {
		if v > RuleSetVersion {
			t.Fatalf("%s records version %d, newer than this build's %d", path, v, RuleSetVersion)
		}
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := fmt.Fprintf(out, "%d %s\n", RuleSetVersion, ruleSetSHA256); err != nil {
		t.Fatal(err)
	}
}

// TestFailSafe: a rule that panics, or returns an outcome the engine does not know, denies.
func TestFailSafe(t *testing.T) {
	saved := rules
	t.Cleanup(func() { rules = saved })
	r := request(worker, KindRead, "app", "")

	rules = append([]Rule{{"test.panic", "panics", func(*evalCtx) (verdict, bool) { panic("boom") }}}, saved...)
	if d := Evaluate(r, Facts{}); d.Outcome != Deny || d.Rule != "engine.error" || d.RuleSetSHA256 == "" {
		t.Errorf("a panicking rule: %+v", d)
	}
	rules = append([]Rule{{"test.bogus", "bogus", func(*evalCtx) (verdict, bool) { return verdict{outcome: "maybe"}, true }}}, saved...)
	if d := Evaluate(r, Facts{}); d.Outcome != Deny || d.Rule != "engine.error" {
		t.Errorf("a bogus outcome: %+v", d)
	}
	rules = []Rule{{"test.silent", "no opinion", func(*evalCtx) (verdict, bool) { return pass() }}}
	if d := Evaluate(r, Facts{}); d.Outcome != Deny || d.Rule != "engine.error" {
		t.Errorf("no rule decided: %+v", d)
	}
}

// TestEvaluateProperties throws random requests and facts at the engine and checks the promises
// that must hold for every one of them.
func TestEvaluateProperties(t *testing.T) {
	_, proj, scratch, facts := deleteTree(t)
	roles := []Role{RoleCaptain, RoleDaemon, RoleSupervisor, RoleWorker, RoleScout, RoleReviewer, RoleGateway, "admin"}
	kinds := []Kind{KindRead, KindWrite, KindMerge, KindDelete, KindForcePush, KindBranchDelete, KindHistoryRewrite, KindCredentialUse, "exec"}
	sources := []Source{SourceCaptain, SourceConfig, SourceDaemon, SourceTask, SourceAgent, SourceToolOutput,
		SourceRepoContent, SourceIssue, SourcePRBody, SourcePRComment, SourceWeb, SourceEmail, "trusted"}
	orgs := []string{"acme", "globex", ""}
	resources := []string{"", "pr/1", "main", "feature/x", "github", "/", proj + "/build", scratch + "/out", proj + "/h", proj, "rel/path"}
	postures := []Posture{"", PostureCaptain, PostureGreen, PostureClient, "yolo"}
	checks := []CheckState{"", ChecksGreen, ChecksRed, ChecksPending}
	pick := func(rng *rand.Rand, n int) int { return rng.IntN(n) }
	rng := rand.New(rand.NewPCG(7, 11))
	seenAllow := map[Kind]bool{}
	for i := 0; i < 50000; i++ {
		role := roles[pick(rng, len(roles))]
		actor := Actor{ID: "a", Role: role, Org: orgs[pick(rng, len(orgs))]}
		req := Request{Actor: actor, Kind: kinds[pick(rng, len(kinds))],
			Target: Target{Org: orgs[pick(rng, len(orgs))], Project: []string{"", "app"}[pick(rng, 2)], Resource: resources[pick(rng, len(resources))]}}
		for n := pick(rng, 4); n > 0; n-- {
			req.Inputs = append(req.Inputs, Input{Source: sources[pick(rng, len(sources))]})
		}
		f := Facts{Posture: postures[pick(rng, len(postures))], Checks: checks[pick(rng, len(checks))],
			RequiredUnreported: pick(rng, 4) == 0, UnintendedClosing: pick(rng, 6) == 0}
		if pick(rng, 3) == 0 {
			f.Exceptions = []Exception{ExceptionSecurity}
		}
		if pick(rng, 2) == 0 {
			f.DefaultBranch = "main"
		}
		if req.Kind == KindDelete && pick(rng, 4) != 0 {
			df := facts(req.Target.Resource)
			f.Home, f.Roots, f.ResolvedPath = df.Home, df.Roots, df.ResolvedPath
		}
		if pick(rng, 4) != 0 {
			f.HeadSHA = "abc"
		}
		seen := f
		if pick(rng, 5) == 0 {
			seen.HeadSHA, seen.DefaultBranch, seen.ResolvedPath = "old", "old", "/old"
		}
		switch pick(rng, 5) {
		case 0:
			f.Approvals = []Approval{approval(req, seen, ByCaptain)}
		case 1:
			f.Approvals = []Approval{approval(req, seen, ByClientViaCaptain)}
		case 2:
			f.Approvals = []Approval{approval(req, seen, ByCaptain), approval(req, seen, ByClientViaCaptain)}
		case 3:
			f.Approvals = []Approval{{Decision: "d/x", By: ByCaptain, ActionSHA256: strings.Repeat("0", 64)}}
		}
		before := deepCopy(req, f)
		d := Evaluate(req, f)
		if !reflect.DeepEqual(before, deepCopy(req, f)) {
			t.Fatalf("Evaluate changed its input: %+v", req)
		}
		if d2 := Evaluate(req, f); !reflect.DeepEqual(d, d2) {
			t.Fatalf("Evaluate is not deterministic: %+v then %+v", d, d2)
		}
		checkInvariants(t, req, f, d)
		if d.Outcome == Allow {
			seenAllow[req.Kind] = true
		}
	}
	for _, k := range []Kind{KindRead, KindWrite, KindMerge, KindDelete, KindForcePush, KindCredentialUse} {
		if !seenAllow[k] {
			t.Errorf("the generator never produced an allowed %s; it is not exercising the allow paths", k)
		}
	}
}

func checkInvariants(t *testing.T, r Request, f Facts, d Decision) {
	t.Helper()
	fail := func(why string) { t.Fatalf("%s\nrequest %+v\nfacts %+v\ndecision %+v", why, r, f, d) }
	switch d.Outcome {
	case Allow, Deny, RequireCaptain:
	default:
		fail("unknown outcome")
	}
	known := false
	for _, rule := range rules {
		known = known || rule.ID == d.Rule
	}
	if !known || d.Reason == "" {
		fail("the decision names no known rule or no reason")
	}
	if orgBound[r.Actor.Role] && r.Actor.Org != r.Target.Org && d.Outcome != Deny {
		fail("a cross-org request was not denied")
	}
	approvedBy := func(by ApprovalBy) bool {
		for _, a := range f.Approvals {
			if a.By == by && a.Decision != "" && a.ActionSHA256 == ActionDigest(r) && a.FactsSHA256 == FactsDigest(r.Kind, f) {
				return true
			}
		}
		return false
	}
	if d.Approval != "" {
		matched := false
		for _, a := range f.Approvals {
			matched = matched || a.Decision == d.Approval && a.ActionSHA256 == ActionDigest(r) && a.FactsSHA256 == FactsDigest(r.Kind, f)
		}
		if !matched {
			fail("the decision used an approval that does not bind to this action and these facts")
		}
	}
	if d.Outcome != Allow {
		return
	}
	if Privileged(r.Kind) {
		basis := false
		for _, in := range r.Inputs {
			basis = basis || Trusted(in.Source)
		}
		if !basis {
			fail("a privileged action was allowed on untrusted inputs alone")
		}
	}
	switch r.Kind {
	case KindRead:
	case KindWrite:
		if r.Actor.Role != RoleWorker {
			fail("a non-worker was allowed to write")
		}
	case KindCredentialUse:
		if r.Actor.Role != RoleGateway {
			fail("a non-gateway was allowed to use a credential")
		}
	case KindMerge:
		if !mergers[r.Actor.Role] || f.HeadSHA == "" || f.Checks != ChecksGreen || f.RequiredUnreported || f.UnintendedClosing {
			fail("a merge was allowed by the wrong role, with its head unknown, or without green, fully reported checks")
		}
		if len(f.Exceptions) > 0 && !approvedBy(ByCaptain) {
			fail("a fixed-list merge was allowed without the captain's approval")
		}
		if (f.Posture == PostureCaptain || f.Posture == "") && !approvedBy(ByCaptain) || f.Posture == PostureClient && !approvedBy(ByClientViaCaptain) {
			fail("a merge was allowed without the approval its posture needs")
		}
	case KindDelete:
		root, err := pathguardCheck(f, r)
		if err != nil {
			fail("a delete the path guard refuses was allowed: " + err.Error())
		}
		if root != "scratch" && !approvedBy(ByCaptain) {
			fail("a delete outside the scratch dir was allowed without an approval")
		}
	case KindForcePush, KindBranchDelete, KindHistoryRewrite:
		if !approvedBy(ByCaptain) || f.DefaultBranch == "" || strings.EqualFold(shortBranch(r.Target.Resource), shortBranch(f.DefaultBranch)) || neverWrite[r.Actor.Role] {
			fail("a destructive git action was allowed without an approval, or on the default branch")
		}
	default:
		fail("an unknown kind was allowed")
	}
}

func deepCopy(r Request, f Facts) string { return fmt.Sprintf("%#v %#v", r, f) }

func TestDecisionRecordRegisters(t *testing.T) {
	// The record must pass the event store's no-secrets check (LUCY-EVT-005).
	if _, err := eventstore.NewRegistry(DecisionRecord{}); err != nil {
		t.Fatal(err)
	}
	r := request(worker, KindMerge, "app", "pr/3", issueInput, taskInput)
	f := Facts{Checks: ChecksGreen, HeadSHA: "abc", BaseRef: "main", BaseSHA: "def"}
	f.Approvals = []Approval{approval(r, f, ByCaptain)}
	rec := NewRecord(r, f, Evaluate(r, f))
	if rec.Outcome != Deny || rec.Rule != "merge.role" || len(rec.Inputs) != 2 || rec.Inputs[0].Trusted || !rec.Inputs[1].Trusted ||
		len(rec.Facts.Approvals) != 1 || !rec.Tainted || rec.RuleSetSHA256 == "" || rec.FactsSHA256 != FactsDigest(KindMerge, f) ||
		rec.Facts.HeadSHA != "abc" || rec.Facts.BaseRef != "main" || rec.Facts.BaseSHA != "def" {
		t.Errorf("record: %+v", rec)
	}
	// A delete's record shows the home guarded and the roots that admitted the path.
	_, _, scratch, facts := deleteTree(t)
	del := request(worker, KindDelete, "app", scratch+"/out")
	df := facts(scratch + "/out")
	drec := NewRecord(del, df, Evaluate(del, df))
	if drec.Outcome != Allow || drec.Facts.Home != df.Home || !reflect.DeepEqual(drec.Facts.Roots, df.Roots) || len(drec.Facts.Roots) != 2 {
		t.Errorf("delete record: %+v", drec)
	}
}

func pathguardCheck(f Facts, r Request) (string, error) {
	root, err := pathguard.CheckResolved(f.Home, f.Roots, r.Target.Resource, f.ResolvedPath)
	return string(root.Kind), err
}
