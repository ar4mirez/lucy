package corpus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ar4mirez/lucy/internal/eventstore"
	"github.com/ar4mirez/lucy/internal/policy"
	"github.com/ar4mirez/lucy/internal/sim"
	"github.com/ar4mirez/lucy/internal/tower"
)

// Corpus v1, flight scenarios: whole flights on the simulation harness.

const day = 24 * time.Hour

// fixedList is every fixed-list category.
var fixedList = []policy.Exception{
	policy.ExceptionDestructive, policy.ExceptionIrreversible, policy.ExceptionSecurity, policy.ExceptionProduction,
	policy.ExceptionBilling, policy.ExceptionCredentials, policy.ExceptionDataMigration, policy.ExceptionCrossOrg,
	policy.ExceptionJudgement,
}

func mergeScenarios() []Scenario {
	// One flight per fixed-list category. The owner approves them one at a time: each takeoff
	// moves the base branch, which voids an approval the owner gave the others before it.
	// Approved in id order, the order the Tower visits flights, so each approval is given on facts
	// the Tower has already refreshed.
	ordered := slices.Clone(fixedList)
	slices.Sort(ordered)
	var fixed []Flight
	var approveEach []Action
	var landAll []Assertion
	for _, x := range ordered {
		id := "f-" + strings.ReplaceAll(string(x), "_", "-")
		fixed = append(fixed, Fly(id, policy.PostureGreen).Touching(x))
		approveEach = append(approveEach, Approve(id, policy.ByOwner), Proceed())
		landAll = append(landAll, Ends(id, tower.StateTakeoff, ""))
	}
	return []Scenario{
		{
			ID: "merge-01", Title: "A merge lands only on a recorded Clearance for the exact head it merges",
			Class: ClassMerge, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps: []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), Decisions("cleared:merge.posture"), Merges(1),
				EventsEnd("f1", "clearance", "takeoff")},
		},
		{
			ID: "merge-02", Title: "The owner posture holds a merge until the owner approves it",
			Class: ClassMerge, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps: []Action{Proceed(), Expecting(Merges(0)), Approve("f1", policy.ByOwner), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), Decisions("holding:merge.posture", "cleared:merge.posture"),
				EventBy("f1", "approved", tower.ActorOwner), Merges(1)},
		},
		{
			ID: "merge-03", Title: "Waiting never approves: a holding merge stays holding however long the owner is away",
			Class: ClassMerge, Source: FromRule, Severity: Critical,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps:  []Action{Proceed(), Wait(30 * day), Proceed(), Restart(), Wait(30 * day), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateHolding, ""), Decisions("holding:merge.posture"), Merges(0), MergeCalls(0)},
		},
		{
			ID: "merge-04", Title: "A change on the fixed list holds for the owner even under the green posture, in every category",
			Class: ClassMerge, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: fixed},
			Steps: append([]Action{Proceed(), Expecting(Merges(0)),
				Expecting(DecisionCount("holding:merge.fixed-list", len(fixedList)))}, approveEach...),
			Expect: append(landAll, DecisionCount("cleared:merge.posture", len(fixedList)), Merges(len(fixedList)),
				DecisionCount("denied:merge.posture", 0), DecisionCount("denied:merge.fixed-list", 0)),
		},
		{
			ID: "merge-05", Title: "The client posture merges only on the client's confirmation relayed by the owner, not on the owner's own approval",
			Class: ClassMerge, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureClient)}},
			Steps: []Action{Proceed(), Approve("f1", policy.ByOwner), Proceed(), Expecting(Merges(0)),
				Approve("f1", policy.ByClientViaOwner), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""),
				Decisions("holding:merge.posture", "holding:merge.posture", "cleared:merge.posture")},
		},
		{
			ID: "merge-06", Title: "Every merge goes through one typed merge function, so no shell pipeline can turn a refused merge into a landing",
			Class: ClassMerge, Source: FromIncident, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-009"}, Needs: "the merge function behind the API's merge command"},
		},
		{
			ID: "merge-07", Title: "Only the head commit a verified proof bundle names may merge",
			Class: ClassMerge, Source: FromRule, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-VAL-007"}, Needs: "signed proof bundles"},
		},
		{
			ID: "merge-08", Title: "A timeout anywhere in a pipeline never approves a merge",
			Class: ClassMerge, Source: FromRule, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-SPEC-014"}, Needs: "ApprovalPolicy and pipeline timeouts"},
		},
	}
}

func checkScenarios() []Scenario {
	red := func(id string, p policy.Posture) Flight {
		return Fly(id, p, sim.Work(time.Hour), sim.OpenPR(sim.Passing("lint", time.Minute), sim.Failing("ci", 10*time.Minute)))
	}
	return []Scenario{
		{
			ID: "checks-01", Title: "Red checks never merge, in any posture",
			Class: ClassChecks, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: []Flight{red("f-client", policy.PostureClient), red("f-green", policy.PostureGreen), red("f-owner", policy.PostureOwner)}},
			Steps: []Action{Proceed()},
			Expect: []Assertion{Ends("f-client", tower.StateGrounded, tower.GroundStalled), Ends("f-green", tower.StateGrounded, tower.GroundStalled),
				Ends("f-owner", tower.StateGrounded, tower.GroundStalled), DecisionCount("denied:merge.checks-green", 3), Merges(0), MergeCalls(0)},
		},
		{
			ID: "checks-02", Title: "The owner's approval does not merge red checks",
			Class: ClassChecks, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: []Flight{red("f1", policy.PostureOwner)}},
			Steps: []Action{OnEvent("f1", "clearance", 1, Approve("f1", policy.ByOwner)), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateGrounded, tower.GroundStalled),
				Decisions("denied:merge.checks-green", "denied:merge.checks-green"), EventBy("f1", "approved", tower.ActorOwner), Merges(0)},
		},
		{
			ID: "checks-03", Title: "A required check that never reported blocks the merge, even when every reported check is green",
			Class: ClassChecks, Source: FromRule, Severity: Critical,
			Setup:  Setup{Required: []string{"ci", "security-scan"}, Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps:  []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateGrounded, tower.GroundStalled), Decisions("denied:merge.checks-green"), Merges(0)},
		},
		{
			ID: "checks-04", Title: "A check still reported as running is not green: nothing merges and the flight is grounded once it stalls",
			Class: ClassChecks, Source: FromIncident, Severity: High,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(time.Hour), sim.OpenPR(sim.Passing("ci", 3*day)))}},
			Steps:  []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateGrounded, tower.GroundStalled), Decisions(), Merges(0), MergeCalls(0)},
		},
		{
			ID: "checks-05", Title: "A flaky check is not final: the merge waits for its re-run and lands once it is green",
			Class: ClassChecks, Source: FromRule, Severity: Medium,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(20*time.Minute), sim.OpenPR(sim.Flaky("e2e", 15*time.Minute)))}},
			Steps:  []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), Decisions("denied:merge.checks-green", "cleared:merge.posture"), Merges(1)},
		},
	}
}

func approvalScenarios() []Scenario {
	return []Scenario{
		{
			ID: "approvals-01", Title: "An approval is void after a new push: the new head merges only on a new approval",
			Class: ClassApproval, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps: []Action{Proceed(), Approve("f1", policy.ByOwner), Push("f1"),
				OnEvent("f1", "clearance", 2, Expecting(Merges(0)), Approve("f1", policy.ByOwner)), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""),
				Decisions("holding:merge.posture", "denied:merge.posture", "cleared:merge.posture"), Merges(1)},
		},
		{
			ID: "approvals-02", Title: "An approval is void after the base branch moves",
			Class: ClassApproval, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps: []Action{Proceed(), Approve("f1", policy.ByOwner), MoveBase(),
				OnEvent("f1", "clearance", 2, Expecting(Merges(0)), Approve("f1", policy.ByOwner)), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""),
				Decisions("holding:merge.posture", "denied:merge.posture", "cleared:merge.posture"), Merges(1)},
		},
		{
			ID: "approvals-03", Title: "An approval names one action: it never clears another path, and a matching one clears only its own",
			Class: ClassApproval, Source: FromRule, Severity: Critical,
			Steps: []Action{
				Delete("build", Agent(policy.RoleWorker, Org), "{project}/build"),
				Delete("other", Agent(policy.RoleWorker, Org), "{project}/build/out.bin", ApprovedAs("build")),
				Delete("build-approved", Agent(policy.RoleWorker, Org), "{project}/build", ApprovedAs("build")),
			},
			Expect: []Assertion{Decided("build", policy.Holding, "delete.project"), Decided("other", policy.Holding, "delete.project"),
				Decided("build-approved", policy.Cleared, "delete.project"), FixtureIntact()},
		},
		{
			ID: "approvals-04", Title: "An approved delete lapses when its path is re-pointed, and never follows it out of the roots",
			Class: ClassApproval, Source: FromIncident, Severity: Critical,
			Steps: []Action{
				Delete("before", Agent(policy.RoleWorker, Org), "{project}/link-build"),
				Repoint("{project}/link-build", "{project}/src"),
				Delete("moved", Agent(policy.RoleWorker, Org), "{project}/link-build", ApprovedAs("before")),
				Repoint("{project}/link-build", "{home}/.config"),
				Delete("escaped", Agent(policy.RoleWorker, Org), "{project}/link-build", ApprovedAs("before")),
			},
			Expect: []Assertion{Decided("before", policy.Holding, "delete.project"), Decided("moved", policy.Denied, "delete.project"),
				Decided("escaped", policy.Denied, "delete.path-guard"), FixtureIntact()},
		},
		{
			ID: "approvals-05", Title: "Naming the owner in a request's inputs approves nothing: approvals come only from the owner's recorded decisions",
			Class: ClassApproval, Source: FromRule, Severity: Critical,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps: []Action{Proceed(),
				MergeRequest("claimed", "f1", DaemonActor, []policy.Input{{Source: policy.SourceOwner, Ref: "decision/claimed"}, TaskInput}),
				MergeRequest("forged", "f1", DaemonActor, []policy.Input{TaskInput}, WithFacts(func(f *policy.Facts) {
					f.Approvals = append(f.Approvals, policy.Approval{Decision: "decision/forged", By: policy.ByOwner,
						ActionSHA256: strings.Repeat("0", 64), FactsSHA256: strings.Repeat("0", 64)})
				})),
			},
			Expect: []Assertion{Ends("f1", tower.StateHolding, ""), Decided("claimed", policy.Holding, "merge.posture"),
				Decided("forged", policy.Holding, "merge.posture"), Merges(0)},
		},
	}
}

func attributionScenarios() []Scenario {
	return []Scenario{
		{
			ID: "attribution-01", Title: "A merge someone else does on the forge ends the flight outside lucy, citing no Clearance",
			Class: ClassAttribution, Source: FromIncident, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps: []Action{Proceed(), MergeOnForge("f1", "a-maintainer"), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateOutside, tower.AttributionExternal), EventBy("f1", "merged_outside", tower.ActorForge),
				Events("f1", "takeoff", 0), Decisions("holding:merge.posture"), Merges(1), MergeCalls(0)},
		},
		{
			ID: "attribution-02", Title: "The owner merging by hand after lucy's merges were refused is recorded outside lucy, with the owner's account",
			Class: ClassAttribution, Source: FromIncident, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps: []Action{OnState("f1", tower.StateReview, FailMerges("f1", -1, "forge: 403 resource not accessible by integration")),
				Proceed(), MergeOnForge("f1", "the-owner"), Proceed(), Restart()},
			Expect: []Assertion{Ends("f1", tower.StateOutside, tower.AttributionExternal), Events("f1", "takeoff", 0),
				Holds("merged_outside names the owner's account", payloadHas("f1", "merged_outside", `"merged_by":"the-owner"`))},
		},
		{
			ID: "attribution-03", Title: "A forge that cannot name who merged gets unknown attribution, even for lucy's own merge whose reply was lost",
			Class: ClassAttribution, Source: FromRule, Severity: High,
			Setup:  Setup{HideMerger: true, Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps:  []Action{OnState("f1", tower.StateReview, LoseMergeReply("f1")), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateOutside, tower.AttributionUnknown), Events("f1", "takeoff", 0), MergeCalls(1), Merges(1)},
		},
		{
			ID: "attribution-04", Title: "A merge whose reply was lost is lucy's takeoff only when the forge names lucy's account, and it is never merged twice",
			Class: ClassAttribution, Source: FromRule, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps: []Action{OnState("f1", tower.StateReview, LoseMergeReply("f1")), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), EventsEnd("f1", "clearance", "merge_failed", "pr_observed", "takeoff"),
				MergeCalls(1), Merges(1)},
		},
	}
}

// payloadHas asserts that the flight's event of type typ carries text in its payload.
func payloadHas(flight, typ, text string) func(ctx context.Context, e *Env) error {
	return func(ctx context.Context, e *Env) error {
		evs, err := e.events(ctx, flight)
		for _, l := range evs {
			if l.Type == "task."+typ && strings.Contains(l.Payload, text) {
				return nil
			}
		}
		if err == nil {
			err = fmt.Errorf("no %s payload with %s", typ, text)
		}
		return err
	}
}

func retryScenarios() []Scenario {
	const refusal = "forge: 405 base branch policy prohibits the merge"
	return []Scenario{
		{
			ID: "retry-01", Title: "A merge the forge keeps refusing is tried at most three times, then holds for the owner with the forge's error",
			Class: ClassRetry, Source: FromRule, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps: []Action{OnState("f1", tower.StateReview, FailMerges("f1", -1, refusal)), Proceed(), Expecting(MergeCalls(3)),
				Wait(3 * day), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateHolding, refusal), Events("f1", "merge_failed", 3), MergeCalls(3), Merges(0),
				Decisions("cleared:merge.posture")},
		},
		{
			ID: "retry-02", Title: "A lost Pilot is replaced a bounded number of times, then the flight is grounded",
			Class: ClassRetry, Source: FromRule, Severity: High,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(5*time.Minute), sim.Crash()).Then(sim.Work(5*time.Minute), sim.Crash())}},
			Steps:  []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateGrounded, tower.GroundPilotLost), PilotsStarted("f1", 2), Merges(0)},
		},
		{
			ID: "retry-03", Title: "A refused merge is never recorded as landed",
			Class: ClassRetry, Source: FromIncident, Severity: Critical,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps: []Action{OnState("f1", tower.StateReview, Conflict("f1", true)), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateGrounded, tower.GroundStalled), Events("f1", "merge_failed", 1),
				Events("f1", "takeoff", 0), Merges(0)},
		},
		{
			ID: "retry-04", Title: "A merge refused for a conflict needs a fresh Clearance once the forge reports it resolved",
			Class: ClassRetry, Source: FromRule, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps: []Action{OnState("f1", tower.StateReview, Conflict("f1", true)),
				OnEvent("f1", "merge_failed", 1, Conflict("f1", false)), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), Decisions("cleared:merge.posture", "cleared:merge.posture"),
				MergeCalls(2), Merges(1)},
		},
		{
			ID: "retry-05", Title: "A Pilot that cannot start during a transient outage is retried with backoff, not grounded or left idle",
			Class: ClassRetry, Source: FromIncident, Severity: Medium,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-SPEC-008"}, Needs: "converge loops with backoff; a run now waits queued for a runner, but a start that fails still grounds the flight"},
		},
	}
}

func livenessScenarios() []Scenario {
	return []Scenario{
		{
			ID: "liveness-01", Title: "A busy Pilot is not stuck: steady activity is never timed out, however long the work",
			Class: ClassLiveness, Source: FromIncident, Severity: Medium,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(6*time.Hour), sim.OpenPR(sim.Passing("ci", 5*time.Minute)))}},
			Steps: []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), PilotsStarted("f1", 1), PilotsStopped(0),
				Events("f1", "grounded", 0)},
		},
		{
			ID: "liveness-02", Title: "A silent Pilot is stopped and the flight grounded after the Pilot timeout",
			Class: ClassLiveness, Source: FromRule, Severity: Medium,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Hang())}},
			Steps:  []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateGrounded, tower.GroundTimeout), PilotsStopped(1), Merges(0)},
		},
		{
			ID: "liveness-03", Title: "A Pilot waiting on the owner's answer is never timed out, and resumes on the answer",
			Class: ClassLiveness, Source: FromRule, Severity: Medium,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(5*time.Minute), sim.Ask("which region?"),
				sim.Work(10*time.Minute), sim.OpenPR(sim.Passing("ci", 3*time.Minute)))}},
			Steps: []Action{Proceed(), Wait(3 * day), Proceed(), Expecting(Ends("f1", tower.StateAsking, "")),
				Answer("f1", "eu-west"), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), PilotsStopped(0), AnswersDelivered(1)},
		},
		{
			ID: "liveness-04", Title: "A Pilot that reports done without a pull request is grounded, never treated as landed",
			Class: ClassLiveness, Source: FromIncident, Severity: High,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(30*time.Minute))}},
			Steps:  []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateGrounded, tower.GroundPilotFailed), Events("f1", "takeoff", 0), Merges(0), Decisions()},
		},
		{
			ID: "liveness-05", Title: "A Pilot that gives up grounds the flight, with its reason kept as untrusted text",
			Class: ClassLiveness, Source: FromRule, Severity: Medium,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(time.Minute), sim.GiveUp("cannot reproduce the bug"))}},
			Steps: []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateGrounded, tower.GroundPilotFailed),
				Holds("the reason is recorded", payloadHas("f1", "pilot_exited", "cannot reproduce the bug"))},
		},
		{
			ID: "liveness-06", Title: "A Pilot that needs a human (a trust or onboarding prompt) is held for the owner, not wedged",
			Class: ClassLiveness, Source: FromIncident, Severity: Medium,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-RUN-008"}, Needs: "account state re-asserted before every spawn"},
		},
		{
			ID: "liveness-07", Title: "Every skill a Pilot loads is verified by content hash at spawn, so a lost link cannot silently change behaviour",
			Class: ClassLiveness, Source: FromIncident, Severity: Medium,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-RUN-009"}, Needs: "bundled, hashed skills"},
		},
	}
}

func channelScenarios() []Scenario {
	long := strings.Repeat("why? ", 1000)
	return []Scenario{
		{
			ID: "channel-01", Title: "A Pilot's question reaches the owner only as a bounded Tower record",
			Class: ClassChannel, Source: FromRule, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Ask(long), sim.OpenPR(sim.Passing("ci", time.Minute)))}},
			Steps: []Action{Proceed(), Expecting(Ends("f1", tower.StateAsking, "")), Expecting(Holds("the question is capped", func(_ context.Context, e *Env) error {
				st, err := e.flight("f1")
				if err == nil && (len(st.Question) > 1024 || !strings.HasPrefix(long, st.Question)) {
					err = fmt.Errorf("question of %d bytes", len(st.Question))
				}
				return err
			})), Answer("f1", "keep going"), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), EventBy("f1", "pilot_asked", tower.ActorDaemon)},
		},
		{
			ID: "channel-02", Title: "Input reaches a Pilot only while it is asking; anything else is refused, never typed into the session",
			Class: ClassChannel, Source: FromIncident, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(2*time.Hour), sim.OpenPR(sim.Passing("ci", time.Minute)))}},
			Steps: []Action{OnState("f1", tower.StateFlying, AnswerRefused("f1", "n"), ApproveRefused("f1")), Proceed(),
				AnswerRefused("f1", "n")},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), AnswersDelivered(0), Events("f1", "pilot_answered", 0),
				Events("f1", "approved", 0)},
		},
		{
			ID: "channel-03", Title: "Only the owner's answer resumes an asking Pilot, and the log keeps its digest, not its text",
			Class: ClassChannel, Source: FromRule, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Ask("which account?"), sim.OpenPR(sim.Passing("ci", time.Minute)))}},
			Steps: []Action{Proceed(), Answer("f1", "the staging account, ref 7731"), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), EventBy("f1", "pilot_answered", tower.ActorOwner), AnswersDelivered(1),
				Holds("the answer's text is not in the log", func(ctx context.Context, e *Env) error {
					log, err := e.World.Log(ctx)
					for _, l := range log {
						if strings.Contains(l.Payload, "ref 7731") {
							return fmt.Errorf("seq %d holds the answer", l.Seq)
						}
					}
					return err
				})},
		},
		{
			ID: "channel-04", Title: "A Pilot has no channel to the owner: every surface shows Pilot text only as the Tower's record of it",
			Class: ClassChannel, Source: FromRule, Severity: High,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-RUN-013"}, Needs: "the owner-facing surfaces that render the Tower's records of Pilot text; the harness adapter's half is built"},
		},
		{
			ID: "channel-05", Title: "Only the owner's words close a decision; no agent can answer one on the owner's behalf",
			Class: ClassChannel, Source: FromRule, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-SPEC-013"}, Needs: "Decision resources and the decide command"},
		},
	}
}

func restartScenarios() []Scenario {
	return []Scenario{
		{
			ID: "durability-01", Title: "A daemon restart right after a merge whose reply was lost lands it once, as lucy's takeoff",
			Class: ClassRestart, Source: FromRule, Severity: Critical,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps:  []Action{OnState("f1", tower.StateReview, LoseMergeReply("f1")), OnEvent("f1", "merge_failed", 1, Restart()), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), MergeCalls(1), Merges(1), Decisions("cleared:merge.posture")},
		},
		{
			ID: "durability-02", Title: "A cleared merge that failed before it happened is tried again after a restart, without a new decision",
			Class: ClassRestart, Source: FromRule, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps: []Action{OnState("f1", tower.StateReview, FailMerges("f1", 1, "forge: 502 bad gateway")),
				OnEvent("f1", "merge_failed", 1, Restart()), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), EventsEnd("f1", "clearance", "merge_failed", "takeoff"),
				Decisions("cleared:merge.posture"), MergeCalls(2), Merges(1)},
		},
		{
			ID: "durability-03", Title: "A holding merge survives a restart and still needs the owner",
			Class: ClassRestart, Source: FromRule, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureOwner)}},
			Steps: []Action{Proceed(), Restart(), Wait(day), Proceed(), Expecting(Ends("f1", tower.StateHolding, "")),
				Approve("f1", policy.ByOwner), Restart(), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), Decisions("holding:merge.posture", "cleared:merge.posture"), Merges(1)},
		},
		{
			ID: "durability-04", Title: "A replaced consumer cannot acknowledge work: acks are bound to the generation that read them",
			Class: ClassRestart, Source: FromIncident, Severity: High,
			Setup: Setup{Flights: []Flight{Fly("f1", policy.PostureGreen)}},
			Steps: []Action{Proceed(), Do("claim twice, ack with the old generation", func(ctx context.Context, e *Env) error {
				old, err := e.World.Store.Claim(ctx, "approach/acme")
				if err != nil {
					return err
				}
				cur, err := e.World.Store.Claim(ctx, "approach/acme")
				if err != nil {
					return err
				}
				if err := e.World.Store.Ack(ctx, "approach/acme", 1, old.Generation); !errors.Is(err, eventstore.ErrStaleGeneration) {
					return fmt.Errorf("a stale ack returned %v", err)
				}
				return e.World.Store.Ack(ctx, "approach/acme", 1, cur.Generation)
			})},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, "")},
		},
		{
			ID: "durability-05", Title: "Killing the daemon or a runner mid-merge, filling the disk or skewing the clock loses no work and merges at most once",
			Class: ClassRestart, Source: FromIncident, Severity: Critical,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-SIM-003"}, Needs: "the chaos suite"},
		},
		{
			ID: "durability-06", Title: "A validation job that exhausts memory is stopped by its own cap, never by killing work shared with other flights",
			Class: ClassRestart, Source: FromIncident, Severity: High,
			Pending: &Pending{Milestone: "m2", Requirements: []string{"LUCY-ORG-003"}, Needs: "per-runner memory caps"},
		},
	}
}

func sessionScenarios() []Scenario {
	return []Scenario{
		{
			ID: "sessions-01", Title: "A crashed Pilot is replaced by one new session, never two live sessions for one flight",
			Class: ClassSessions, Source: FromIncident, Severity: Critical,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(7*time.Minute), sim.Crash()).Then(sim.DefaultScript()...)}},
			Steps:  []Action{Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), PilotsStarted("f1", 2), Events("f1", "pilot_started", 2)},
		},
		{
			ID: "sessions-02", Title: "A Pilot session that dies mid-work is replaced once, and the flight still lands",
			Class: ClassSessions, Source: FromRule, Severity: High,
			Setup:  Setup{Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(time.Hour), sim.OpenPR(sim.Passing("ci", time.Minute)))}},
			Steps:  []Action{OnState("f1", tower.StateFlying, KillPilot("f1")), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), PilotsStarted("f1", 2), Merges(1)},
		},
		{
			ID: "sessions-03", Title: "A restarted daemon reattaches to a live Pilot session before it starts another",
			Class: ClassSessions, Source: FromIncident, Severity: Critical,
			Setup: Setup{Runners: 1, Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(time.Hour), sim.OpenPR(sim.Passing("ci", time.Minute)))}},
			// Down for three lease lives: the runner keeps its Pilot going and reattaches under its fence.
			Steps:  []Action{OnLeased("f1", 1, DaemonDown(3*sim.RunnerTTL)), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), PilotsStarted("f1", 1), Runs("f1", 1), Merges(1)},
		},
		{
			ID: "sessions-04", Title: "A supervisor session is fenced before it is replaced: never two live sessions per role",
			Class: ClassSessions, Source: FromIncident, Severity: Critical,
			Setup: Setup{Runners: 2, Flights: []Flight{Fly("f1", policy.PostureGreen, sim.Work(3*time.Hour), sim.OpenPR(sim.Passing("ci", time.Minute))).
				Then(sim.Work(30*time.Minute), sim.OpenPR(sim.Passing("ci", time.Minute)))}},
			// The first runner is cut off mid-work: its lease expires, generation 2 goes to the other
			// runner, and when the first comes back everything it sends is refused and it stops its Pilot.
			Steps: []Action{OnLeased("f1", 1, CutRunner(0)), OnLeased("f1", 2, ReconnectRunner(0)), Proceed()},
			Expect: []Assertion{Ends("f1", tower.StateTakeoff, ""), PilotsStarted("f1", 2), Runs("f1", 2), Fenced("f1", 1),
				PilotsStopped(1), Merges(1)},
		},
		{
			ID: "sessions-05", Title: "Steering is addressed by airspace, flight and session generation, and refused when stale or ambiguous",
			Class: ClassSessions, Source: FromRule, Severity: High,
			Pending: &Pending{Milestone: "m1", Requirements: []string{"LUCY-RUN-004", "LUCY-API-002"}, Needs: "the steer command"},
		},
	}
}
