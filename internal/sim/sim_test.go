package sim

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ar4mirez/lucy/internal/audit"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/tower"
)

func newWorld(t *testing.T, seed uint64) *World {
	t.Helper()
	w, err := Open(context.Background(), t.TempDir(), seed, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() }) //nolint:errcheck // best effort in cleanup
	return w
}

func spec(id string, posture policy.Posture) tower.Spec {
	return tower.Spec{ID: id, Org: "acme", Project: "web", Goal: "fix the login page", Posture: posture}
}

func run(t *testing.T, w *World, opts RunOptions) {
	t.Helper()
	if err := w.Run(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
}

func dispatch(t *testing.T, w *World, s tower.Spec, steps ...Step) {
	t.Helper()
	if err := w.Dispatch(context.Background(), s, steps...); err != nil {
		t.Fatal(err)
	}
}

func state(t *testing.T, w *World, id string) tower.Status {
	t.Helper()
	st, ok := w.Tower.Flight(id)
	if !ok {
		t.Fatalf("no flight %s", id)
	}
	return st
}

func wantState(t *testing.T, w *World, id string, want tower.State, reason string) {
	t.Helper()
	if st := state(t, w, id); st.State != want || st.Reason != reason {
		t.Fatalf("flight %s is %s (%q), want %s (%q)", id, st.State, st.Reason, want, reason)
	}
}

// outcomes lists each recorded decision as outcome:rule.
func outcomes(t *testing.T, w *World) []string {
	t.Helper()
	ds, err := w.Decisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range ds {
		out = append(out, string(d.Record.Outcome)+":"+d.Record.Rule)
	}
	return out
}

func wantOutcomes(t *testing.T, w *World, want ...string) {
	t.Helper()
	if got := outcomes(t, w); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("decisions %v, want %v", got, want)
	}
}

// types lists the event types of the flight's stream.
func types(t *testing.T, w *World, id string) []string {
	t.Helper()
	log, err := w.Log(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range log {
		if l.Stream == "task/acme/"+id {
			out = append(out, strings.TrimPrefix(l.Type, "task."))
		}
	}
	return out
}

// checkInvariants holds for every World: the audit chain verifies, and every merge the forge
// performed rests on a cleared decision recorded before it, for the same pull request and head
// (LUCY-SIM-002).
func checkInvariants(t *testing.T, w *World) {
	t.Helper()
	ctx := context.Background()
	if _, err := audit.VerifyStore(ctx, w.Store); err != nil {
		t.Fatalf("audit chain: %v", err)
	}
	ds, err := w.Decisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range w.Forge.Merges() {
		found := false
		for _, d := range ds {
			r := d.Record
			if r.Outcome == policy.Cleared && r.Kind == policy.KindMerge && r.Facts.HeadSHA == m.HeadSHA &&
				r.Target.Org+"/"+r.Target.Project == m.Repo && r.Target.Resource == "pr/"+strconv.Itoa(m.PR) {
				found = true
			}
		}
		if !found {
			t.Fatalf("merge %+v has no cleared decision", m)
		}
	}
	for _, d := range ds {
		if d.Record.Outcome == policy.Cleared && !allowingRules[d.Record.Rule] {
			t.Fatalf("decision %d cleared under %s, which is not an allowing rule", d.Seq, d.Record.Rule)
		}
	}
}

// allowingRules are the rules that may clear a privileged action in the simulation suite.
var allowingRules = map[string]bool{"merge.posture": true, "credential.gateway-only": true}

func TestTakeoffGreenPosture(t *testing.T) {
	w := newWorld(t, 1)
	dispatch(t, w, spec("f1", policy.PostureGreen))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	wantOutcomes(t, w, "cleared:merge.posture")
	if got := strings.Join(types(t, w, "f1"), " "); got != "dispatched pilot_started pilot_exited pr_observed pr_observed clearance takeoff" {
		t.Fatalf("events %s", got)
	}
	if m := w.Forge.Merges(); len(m) != 1 || m[0].HeadSHA != state(t, w, "f1").Observed.HeadSHA {
		t.Fatalf("merges %+v", m)
	}
	checkInvariants(t, w)
}

func TestOwnerPostureHoldsUntilApproved(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 2)
	dispatch(t, w, spec("f1", ""))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateHolding, "")
	if len(w.Forge.Merges()) != 0 {
		t.Fatal("merged while holding")
	}
	if err := w.Tower.Approve(ctx, "f1", policy.ByOwner); err != nil {
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	wantOutcomes(t, w, "holding:merge.posture", "cleared:merge.posture")
	checkInvariants(t, w)
}

func TestFixedListHoldsUnderGreenPosture(t *testing.T) {
	w := newWorld(t, 3)
	s := spec("f1", policy.PostureGreen)
	s.Exceptions = []policy.Exception{policy.ExceptionDataMigration}
	dispatch(t, w, s)
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateHolding, "")
	if err := w.Tower.Approve(context.Background(), "f1", policy.ByOwner); err != nil {
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	wantOutcomes(t, w, "holding:merge.fixed-list", "cleared:merge.posture")
	checkInvariants(t, w)
}

func TestRedChecksDenied(t *testing.T) {
	w := newWorld(t, 4)
	dispatch(t, w, spec("f1", policy.PostureGreen), Work(time.Hour), OpenPR(Passing("lint", time.Minute), Failing("test", 10*time.Minute)))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateGrounded, tower.GroundStalled)
	wantOutcomes(t, w, "denied:merge.checks-green")
	if len(w.Forge.Merges()) != 0 {
		t.Fatal("merged red")
	}
	checkInvariants(t, w)
}

func TestRequiredCheckUnreported(t *testing.T) {
	w := newWorld(t, 5)
	w.Forge.AddRepo("acme/web", "ci", "security-scan")
	dispatch(t, w, spec("f1", policy.PostureGreen))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateGrounded, tower.GroundStalled)
	if st := state(t, w, "f1"); !st.Observed.RequiredUnreported || len(w.Forge.Merges()) != 0 {
		t.Fatalf("observed %+v, merges %v", st.Observed, w.Forge.Merges())
	}
	checkInvariants(t, w)
}

func TestFlakyCheckRecovers(t *testing.T) {
	w := newWorld(t, 6)
	dispatch(t, w, spec("f1", policy.PostureGreen), Work(20*time.Minute), OpenPR(Flaky("e2e", 15*time.Minute)))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	wantOutcomes(t, w, "denied:merge.checks-green", "cleared:merge.posture")
	checkInvariants(t, w)
}

func TestApprovalInvalidatedByPush(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 7)
	dispatch(t, w, spec("f1", policy.PostureOwner))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateHolding, "")
	if err := w.Tower.Approve(ctx, "f1", policy.ByOwner); err != nil {
		t.Fatal(err)
	}
	approved := state(t, w, "f1").Observed.HeadSHA
	if _, err := w.Forge.Push("acme/web", state(t, w, "f1").PR); err != nil { // someone else pushes after the approval
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateGrounded, tower.GroundStalled)
	wantOutcomes(t, w, "holding:merge.posture", "denied:merge.posture")
	ds, _ := w.Decisions(ctx)
	last := ds[len(ds)-1].Record
	if !strings.Contains(last.Reason, "head or base changed") || last.Facts.HeadSHA == approved {
		t.Fatalf("last decision %+v", last)
	}
	if len(w.Forge.Merges()) != 0 {
		t.Fatal("merged a head the owner never saw")
	}
	checkInvariants(t, w)
}

func TestApprovalInvalidatedByBaseMove(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 8)
	dispatch(t, w, spec("f1", policy.PostureOwner))
	run(t, w, RunOptions{})
	if err := w.Tower.Approve(ctx, "f1", policy.ByOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Forge.MoveBase("acme/web", "main"); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(ctx, RunOptions{For: time.Hour}); !errors.Is(err, ErrTimeout) { // denied: no clearance in sight
		t.Fatalf("err %v", err)
	}
	wantState(t, w, "f1", tower.StateReview, "") // the owner approves the new facts
	if err := w.Tower.Approve(ctx, "f1", policy.ByOwner); err != nil {
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	wantOutcomes(t, w, "holding:merge.posture", "denied:merge.posture", "cleared:merge.posture")
	checkInvariants(t, w)
}

func TestPilotAsksTheOwner(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 9)
	dispatch(t, w, spec("f1", policy.PostureGreen), Work(5*time.Minute), Ask("which database?"), Work(5*time.Minute), OpenPR(Passing("ci", time.Minute)))
	run(t, w, RunOptions{})
	st := state(t, w, "f1")
	if st.State != tower.StateAsking || st.Question != "which database?" {
		t.Fatalf("status %+v", st)
	}
	if err := w.Tower.Answer(ctx, "f1", "postgres"); err != nil {
		t.Fatal(err)
	}
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	if got := w.Runtime.Answers[st.Session]; len(got) != 1 || got[0] != "postgres" {
		t.Fatalf("answers %v", got)
	}
	log, _ := w.Log(ctx)
	for _, l := range log {
		if strings.Contains(l.Payload, "postgres") {
			t.Fatalf("the answer's text is in the log: %+v", l)
		}
	}
	checkInvariants(t, w)
}

func TestPilotGivesUp(t *testing.T) {
	w := newWorld(t, 10)
	dispatch(t, w, spec("f1", policy.PostureGreen), Work(time.Hour), GiveUp("cannot reproduce"))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateGrounded, tower.GroundPilotFailed)
	wantOutcomes(t, w)
}

func TestPilotHangs(t *testing.T) {
	w := newWorld(t, 11)
	dispatch(t, w, spec("f1", policy.PostureGreen), Work(time.Minute), Hang())
	run(t, w, RunOptions{})
	st := state(t, w, "f1")
	wantState(t, w, "f1", tower.StateGrounded, tower.GroundTimeout)
	if len(w.Runtime.Stopped) != 1 || w.Runtime.Stopped[0] != st.Session {
		t.Fatalf("stopped %v", w.Runtime.Stopped)
	}
	if got := st.Since.Sub(Epoch); got < tower.DefaultPilotTimeout || got > tower.DefaultPilotTimeout+2*time.Minute {
		t.Fatalf("grounded after %s", got)
	}
}

func TestPilotCrashIsReplaced(t *testing.T) {
	w := newWorld(t, 12)
	w.Runtime.Script("f1", Work(10*time.Minute), Crash())
	w.Runtime.Script("f1", DefaultScript()...)
	dispatch(t, w, spec("f1", policy.PostureGreen))
	run(t, w, RunOptions{})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	if st := state(t, w, "f1"); st.Generation != 2 || len(w.Runtime.Started) != 2 {
		t.Fatalf("status %+v, starts %v", st, w.Runtime.Started)
	}
	checkInvariants(t, w)
}

func TestPilotLostTwiceGrounds(t *testing.T) {
	w := newWorld(t, 13)
	killed := 0
	dispatch(t, w, spec("f1", policy.PostureGreen), Hang())
	run(t, w, RunOptions{BeforeStep: func(_ context.Context, w *World, step int) error {
		if st := state(t, w, "f1"); st.State == tower.StateFlying && step%5 == 0 { // the fault hook: kill the session
			killed++
			return w.Runtime.Kill(st.Session)
		}
		return nil
	}})
	wantState(t, w, "f1", tower.StateGrounded, tower.GroundPilotLost)
	if killed != 2 || len(w.Runtime.Started) != 2 {
		t.Fatalf("killed %d, started %d", killed, len(w.Runtime.Started))
	}
}

func TestMergeConflictThenResolved(t *testing.T) {
	w := newWorld(t, 14)
	dispatch(t, w, spec("f1", policy.PostureGreen))
	resolved := false
	run(t, w, RunOptions{BeforeStep: func(_ context.Context, w *World, _ int) error {
		st := state(t, w, "f1")
		switch {
		case st.State == tower.StateReview && st.PR > 0 && st.Observed.HeadSHA == "":
			return w.Forge.SetConflict("acme/web", st.PR, true)
		case !resolved && st.Observed.Conflict && contains(types(t, w, "f1"), "merge_failed"):
			resolved = true // the Pilot rebases: the conflict is gone with a new push
			if err := w.Forge.SetConflict("acme/web", st.PR, false); err != nil {
				return err
			}
			_, err := w.Forge.Push("acme/web", st.PR)
			return err
		}
		return nil
	}})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	wantOutcomes(t, w, "cleared:merge.posture", "cleared:merge.posture")
	if len(w.Forge.Merges()) != 1 {
		t.Fatalf("merges %v", w.Forge.Merges())
	}
	checkInvariants(t, w)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// The daemon dies mid-merge: the forge merged but the Tower never heard back. The restarted
// Tower finds the pull request merged at the head it was cleared for, and records the takeoff.
func TestRestartMidMerge(t *testing.T) {
	w := newWorld(t, 15)
	dispatch(t, w, spec("f1", policy.PostureGreen))
	armed := false
	run(t, w, RunOptions{BeforeStep: func(ctx context.Context, w *World, _ int) error {
		st := state(t, w, "f1")
		if !armed && st.State == tower.StateReview && st.PR > 0 {
			armed = true
			return w.Forge.LoseMergeReply("acme/web", st.PR)
		}
		if contains(types(t, w, "f1"), "merge_failed") && st.State == tower.StateReview {
			return w.Restart(ctx)
		}
		return nil
	}})
	wantState(t, w, "f1", tower.StateTakeoff, "")
	if got := strings.Join(types(t, w, "f1"), " "); !strings.HasSuffix(got, "clearance merge_failed pr_observed takeoff") {
		t.Fatalf("events %s", got)
	}
	if len(w.Forge.Merges()) != 1 {
		t.Fatalf("merges %v", w.Forge.Merges())
	}
	checkInvariants(t, w)
}

func TestDemoOutcomes(t *testing.T) {
	w := newWorld(t, 42)
	if err := Demo(context.Background(), w, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range w.Tower.Flights() {
		got := string(f.State)
		if f.Reason != "" {
			got += "/" + f.Reason
		}
		if got != DemoOutcomes[f.ID] {
			t.Errorf("flight %s ended %s, want %s", f.ID, got, DemoOutcomes[f.ID])
		}
	}
	if len(w.Tower.Flights()) != len(DemoOutcomes) {
		t.Errorf("%d flights, want %d", len(w.Tower.Flights()), len(DemoOutcomes))
	}
	checkInvariants(t, w)
}

// Same seed, same script: the same event log, byte for byte, and the same audit chain head
// (LUCY-SIM-001). A different seed draws different SHAs and timings.
func TestDeterministic(t *testing.T) {
	ctx := context.Background()
	logOf := func(seed uint64, opts RunOptions) ([]Line, string) {
		w := newWorld(t, seed)
		if err := Demo(ctx, w, opts); err != nil {
			t.Fatal(err)
		}
		log, err := w.Log(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := audit.VerifyStore(ctx, w.Store)
		if err != nil {
			t.Fatal(err)
		}
		return log, rep.Head
	}
	a, headA := logOf(42, RunOptions{})
	b, headB := logOf(42, RunOptions{})
	if len(a) == 0 || headA != headB || !equalLogs(a, b) {
		t.Fatalf("two runs with seed 42 differ (heads %s, %s)", headA, headB)
	}
	// A daemon restart changes nothing in the log: restart before every step of the first two
	// virtual hours, where every flight's transitions happen, then hourly.
	c, headC := logOf(42, RunOptions{BeforeStep: func(ctx context.Context, w *World, step int) error {
		if step <= 120 || step%60 == 0 {
			return w.Restart(ctx)
		}
		return nil
	}})
	if headC != headA || !equalLogs(a, c) {
		t.Fatalf("restarting the daemon changed the log (heads %s, %s)", headA, headC)
	}
	if d, _ := logOf(43, RunOptions{}); equalLogs(a, d) {
		t.Fatal("seeds 42 and 43 gave the same log")
	}
}

func equalLogs(a, b []Line) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Every privileged action no rule allows is refused and recorded (LUCY-SIM-002): every role
// asking for every privileged kind on a flight's pull request, branch and paths, with the facts
// the Tower has (none for a request it did not prepare). Only the credential gateway's own
// credential use is allowed, by its rule.
func TestPolicyRefusesWhatNoRuleAllows(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 16)
	dispatch(t, w, spec("f1", policy.PostureGreen))
	run(t, w, RunOptions{})
	head := state(t, w, "f1").Observed
	roles := []policy.Role{policy.RoleSupervisor, policy.RoleWorker, policy.RoleResearcher, policy.RoleReviewer, policy.RoleGateway, policy.RoleDaemon, policy.RoleOwner}
	kinds := []struct {
		kind     policy.Kind
		resource string
	}{
		{policy.KindMerge, "pr/1"}, {policy.KindDelete, "/home/u/src/web"}, {policy.KindForcePush, "main"},
		{policy.KindBranchDelete, "main"}, {policy.KindHistoryRewrite, "lucy/f1-1"}, {policy.KindCredentialUse, "github"},
	}
	n := 0
	for _, role := range roles {
		for _, k := range kinds {
			actor := policy.Actor{ID: string(role), Role: role, Org: "acme"}
			if role == policy.RoleOwner || role == policy.RoleDaemon {
				actor.Org = ""
			}
			req := policy.Request{Actor: actor, Kind: k.kind, Target: policy.Target{Org: "acme", Project: "web", Resource: k.resource},
				Inputs: []policy.Input{{Source: policy.SourceAgent, Ref: "session/sim-1"}, {Source: policy.SourceTask, Ref: "task/acme/f1"}}}
			facts := policy.Facts{}
			if k.kind == policy.KindMerge { // the merged pull request's facts, without the green posture's caller
				facts = policy.Facts{Checks: head.Checks, HeadSHA: head.HeadSHA, BaseRef: head.BaseRef, BaseSHA: head.BaseSHA}
			}
			d, _, err := w.Tower.Request(ctx, actor.ID, req, facts)
			if err != nil {
				t.Fatal(err)
			}
			n++
			allowed := role == policy.RoleGateway && k.kind == policy.KindCredentialUse
			if (d.Outcome == policy.Cleared) != allowed {
				t.Errorf("%s %s: %s by %s (%s)", role, k.kind, d.Outcome, d.Rule, d.Reason)
			}
		}
	}
	// Outside text alone never raises privileges, even for the owner.
	req := policy.Request{Actor: policy.Actor{ID: "owner", Role: policy.RoleOwner}, Kind: policy.KindMerge,
		Target: policy.Target{Org: "acme", Project: "web", Resource: "pr/1"}, Inputs: []policy.Input{{Source: policy.SourcePRBody, Ref: "pr/1"}}}
	if d, _, _ := w.Tower.Request(ctx, "owner", req, policy.Facts{Posture: policy.PostureGreen, Checks: policy.ChecksGreen, HeadSHA: head.HeadSHA}); d.Outcome != policy.Denied || d.Rule != "taint.trusted-basis" {
		t.Errorf("tainted merge: %s by %s", d.Outcome, d.Rule)
	}
	ds, err := w.Decisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != n+2 { // the flight's own clearance, the matrix and the tainted request
		t.Fatalf("%d decisions recorded, want %d", len(ds), n+2)
	}
	checkInvariants(t, w)
}

func TestRunTimesOut(t *testing.T) {
	w := newWorld(t, 17)
	dispatch(t, w, spec("f1", policy.PostureGreen), Hang())
	if err := w.Run(context.Background(), RunOptions{For: 10 * time.Minute}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err %v", err)
	}
}
