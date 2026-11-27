---
id: conformance-coverage
title: Conformance corpus coverage
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-CORP-001, LUCY-CORP-002]
generated: true
---

# Conformance corpus coverage

Generated from `internal/corpus` by `mise run conformance-doc`; do not edit by hand. Every safety rule
an agent supervisor must keep, and every class of past incident, is a scenario that runs against
lucy on the simulation harness (LUCY-CORP-001, LUCY-CORP-002). An airspace moves onto lucy only
when its corpus is green. `mise run conformance` runs it, and CI runs it on every change.

Corpus v1: 88 scenarios, **58 executable** (green: CI fails on any red one) and **30 pending** on
the milestone named in their row. Each executable scenario runs twice, as written and with the
daemon restarted before every step, and both runs must leave the same event log.

## By rule class

| Rule class | Executable | Pending |
|---|---|---|
| merge-authority | 5 | 3 |
| checks | 5 | 0 |
| approvals | 5 | 0 |
| attribution | 4 | 0 |
| bounded-retry | 4 | 1 |
| liveness | 5 | 2 |
| owner-channel | 3 | 2 |
| untrusted-input | 3 | 1 |
| roles | 4 | 0 |
| org-isolation | 1 | 1 |
| delete-guard | 8 | 3 |
| unlanded-work | 4 | 2 |
| closing-refs | 1 | 3 |
| durability | 4 | 2 |
| sessions | 2 | 3 |
| public-artifacts | 0 | 3 |
| validation | 0 | 4 |

## Scenarios

Source is `rule` for a standing safety rule and `incident` for a class of incident
that happened. Status `executable` means the scenario runs today; `pending` names the milestone and
requirements that make it executable.

| ID | Rule class | Source | Severity | Rule | Status |
|---|---|---|---|---|---|
| merge-01 | merge-authority | rule | critical | A merge lands only on a recorded Clearance for the exact head it merges | executable |
| merge-02 | merge-authority | rule | critical | The owner posture holds a merge until the owner approves it | executable |
| merge-03 | merge-authority | rule | critical | Waiting never approves: a holding merge stays holding however long the owner is away | executable |
| merge-04 | merge-authority | rule | critical | A change on the fixed list holds for the owner even under the green posture, in every category | executable |
| merge-05 | merge-authority | rule | critical | The client posture merges only on the client's confirmation relayed by the owner, not on the owner's own approval | executable |
| merge-06 | merge-authority | incident | critical | Every merge goes through one typed merge function, so no shell pipeline can turn a refused merge into a landing | pending: m1 (LUCY-VAL-009) |
| merge-07 | merge-authority | rule | critical | Only the head commit a verified proof bundle names may merge | pending: m1 (LUCY-VAL-007) |
| merge-08 | merge-authority | rule | critical | A timeout anywhere in a pipeline never approves a merge | pending: m1 (LUCY-SPEC-014) |
| checks-01 | checks | rule | critical | Red checks never merge, in any posture | executable |
| checks-02 | checks | rule | critical | The owner's approval does not merge red checks | executable |
| checks-03 | checks | rule | critical | A required check that never reported blocks the merge, even when every reported check is green | executable |
| checks-04 | checks | incident | high | A check still reported as running is not green: nothing merges and the flight is grounded once it stalls | executable |
| checks-05 | checks | rule | medium | A flaky check is not final: the merge waits for its re-run and lands once it is green | executable |
| approvals-01 | approvals | rule | critical | An approval is void after a new push: the new head merges only on a new approval | executable |
| approvals-02 | approvals | rule | critical | An approval is void after the base branch moves | executable |
| approvals-03 | approvals | rule | critical | An approval names one action: it never clears another path, and a matching one clears only its own | executable |
| approvals-04 | approvals | incident | critical | An approved delete lapses when its path is re-pointed, and never follows it out of the roots | executable |
| approvals-05 | approvals | rule | critical | Naming the owner in a request's inputs approves nothing: approvals come only from the owner's recorded decisions | executable |
| attribution-01 | attribution | incident | high | A merge someone else does on the forge ends the flight outside lucy, citing no Clearance | executable |
| attribution-02 | attribution | incident | high | The owner merging by hand after lucy's merges were refused is recorded outside lucy, with the owner's account | executable |
| attribution-03 | attribution | rule | high | A forge that cannot name who merged gets unknown attribution, even for lucy's own merge whose reply was lost | executable |
| attribution-04 | attribution | rule | high | A merge whose reply was lost is lucy's takeoff only when the forge names lucy's account, and it is never merged twice | executable |
| retry-01 | bounded-retry | rule | high | A merge the forge keeps refusing is tried at most three times, then holds for the owner with the forge's error | executable |
| retry-02 | bounded-retry | rule | high | A lost Pilot is replaced a bounded number of times, then the flight is grounded | executable |
| retry-03 | bounded-retry | incident | critical | A refused merge is never recorded as landed | executable |
| retry-04 | bounded-retry | rule | high | A merge refused for a conflict needs a fresh Clearance once the forge reports it resolved | executable |
| retry-05 | bounded-retry | incident | medium | A Pilot that cannot start during a transient outage is retried with backoff, not grounded or left idle | pending: m1 (LUCY-SPEC-008, LUCY-RUN-001) |
| liveness-01 | liveness | incident | medium | A busy Pilot is not stuck: steady activity is never timed out, however long the work | executable |
| liveness-02 | liveness | rule | medium | A silent Pilot is stopped and the flight grounded after the Pilot timeout | executable |
| liveness-03 | liveness | rule | medium | A Pilot waiting on the owner's answer is never timed out, and resumes on the answer | executable |
| liveness-04 | liveness | incident | high | A Pilot that reports done without a pull request is grounded, never treated as landed | executable |
| liveness-05 | liveness | rule | medium | A Pilot that gives up grounds the flight, with its reason kept as untrusted text | executable |
| liveness-06 | liveness | incident | medium | A Pilot that needs a human (a trust or onboarding prompt) is held for the owner, not wedged | pending: m1 (LUCY-RUN-008) |
| liveness-07 | liveness | incident | medium | Every skill a Pilot loads is verified by content hash at spawn, so a lost link cannot silently change behaviour | pending: m1 (LUCY-RUN-009) |
| channel-01 | owner-channel | rule | high | A Pilot's question reaches the owner only as a bounded Tower record | executable |
| channel-02 | owner-channel | incident | high | Input reaches a Pilot only while it is asking; anything else is refused, never typed into the session | executable |
| channel-03 | owner-channel | rule | high | Only the owner's answer resumes an asking Pilot, and the log keeps its digest, not its text | executable |
| channel-04 | owner-channel | rule | high | A Pilot has no channel to the owner: every surface shows Pilot text only as the Tower's record of it | pending: m1 (LUCY-RUN-013) |
| channel-05 | owner-channel | rule | critical | Only the owner's words close a decision; no agent can answer one on the owner's behalf | pending: m1 (LUCY-SPEC-013) |
| trust-01 | untrusted-input | rule | critical | Outside text alone never backs a privileged action, whoever asks and whatever the other facts | executable |
| trust-02 | untrusted-input | rule | high | A request that rests partly on outside text is marked tainted in its record | executable |
| trust-03 | untrusted-input | rule | high | An input from a source lucy does not know is refused | executable |
| trust-04 | untrusted-input | rule | critical | Every input is tagged with its source where it enters lucy, never by the model that read it | pending: m1 (LUCY-RUN-002, LUCY-POL-004) |
| roles-01 | roles | rule | critical | A Pilot never merges, not even its own green pull request with the owner's approval | executable |
| roles-02 | roles | incident | critical | The supervisor never writes to a project; only a ship worker does | executable |
| roles-03 | roles | rule | critical | Only the airspace's credential gateway uses credentials; no agent, the owner or the daemon ever does directly | executable |
| roles-04 | roles | rule | critical | Roles that never write never delete or rewrite anything | executable |
| isolation-01 | org-isolation | rule | critical | An agent of one airspace never reads, writes or merges in another | executable |
| isolation-02 | org-isolation | rule | critical | With several airspaces supervised at once, a cross-airspace read is refused and audited | pending: m2 (LUCY-ORG-001) |
| delete-01 | delete-guard | incident | critical | A delete of the filesystem root is refused, even with the owner's approval | executable |
| delete-02 | delete-guard | incident | critical | A delete of the home directory or any directory above it is refused | executable |
| delete-03 | delete-guard | incident | critical | A delete that climbs out of its root with .. is refused | executable |
| delete-04 | delete-guard | incident | critical | A delete through a symlink that resolves outside the roots is refused | executable |
| delete-05 | delete-guard | incident | critical | A delete outside every declared root is refused | executable |
| delete-06 | delete-guard | rule | critical | A root itself is never deleted, only what is inside it | executable |
| delete-07 | delete-guard | rule | critical | A path with invisible or lookalike characters, or one that does not resolve, is refused | executable |
| delete-08 | delete-guard | rule | high | A delete inside the flight's scratch dir is allowed; inside the project it holds for the owner | executable |
| delete-09 | delete-guard | incident | critical | The delete executor removes only the resolved path it was cleared for, without following symlinks | pending: m1 (LUCY-SEC-005) |
| delete-10 | delete-guard | incident | high | Each flight has its own scratch dir, so one flight's cleanup never touches another live flight's files | pending: m1 (LUCY-RUN-006) |
| delete-11 | delete-guard | incident | critical | Code a Pilot or a validation agent runs that deletes the filesystem root leaves the owner's home intact | pending: m1 (LUCY-SIM-003, LUCY-SBX-001) |
| unlanded-01 | unlanded-work | rule | critical | The default branch is never force-pushed, deleted or rewritten, even with the owner's approval | executable |
| unlanded-02 | unlanded-work | rule | critical | Force-pushing, deleting or rewriting any other branch needs the owner | executable |
| unlanded-03 | unlanded-work | rule | critical | No branch is rewritten in a project whose default branch lucy does not know | executable |
| unlanded-04 | unlanded-work | rule | critical | A refspec, an option or another ref namespace is never taken for a branch name | executable |
| unlanded-05 | unlanded-work | rule | critical | A worktree holding unlanded work is never reset or released; only a landed-work proof frees it | pending: m1 (LUCY-RUN-007) |
| unlanded-06 | unlanded-work | incident | critical | A worktree is never handed to a new flight while a live flight still holds it | pending: m1 (LUCY-RUN-007) |
| closing-01 | closing-refs | incident | high | A pull request that would close an issue its flight did not intend never merges, approved or not | executable |
| closing-02 | closing-refs | incident | high | Closing references are read from the pull request and checked against the flight: partial work is "Part of", never "Closes" | pending: m1 (LUCY-VAL-009) |
| closing-03 | closing-refs | incident | high | A closing keyword counts wherever it appears: in quoted prose of the body and in any commit of the pull request | pending: m1 (LUCY-VAL-009) |
| closing-04 | closing-refs | incident | medium | An issue that a partial merge closed anyway is reopened and reported to the owner | pending: m1 (LUCY-VAL-009) |
| durability-01 | durability | rule | critical | A daemon restart right after a merge whose reply was lost lands it once, as lucy's takeoff | executable |
| durability-02 | durability | rule | high | A cleared merge that failed before it happened is tried again after a restart, without a new decision | executable |
| durability-03 | durability | rule | high | A holding merge survives a restart and still needs the owner | executable |
| durability-04 | durability | incident | high | A replaced consumer cannot acknowledge work: acks are bound to the generation that read them | executable |
| durability-05 | durability | incident | critical | Killing the daemon or a runner mid-merge, filling the disk or skewing the clock loses no work and merges at most once | pending: m1 (LUCY-SIM-003) |
| durability-06 | durability | incident | high | A validation job that exhausts memory is stopped by its own cap, never by killing work shared with other flights | pending: m2 (LUCY-ORG-003) |
| sessions-01 | sessions | incident | critical | A crashed Pilot is replaced by one new session, never two live sessions for one flight | executable |
| sessions-02 | sessions | rule | high | A Pilot session that dies mid-work is replaced once, and the flight still lands | executable |
| sessions-03 | sessions | incident | critical | A restarted daemon reattaches to a live Pilot session before it starts another | pending: m1 (LUCY-RUN-004, LUCY-RUN-005) |
| sessions-04 | sessions | incident | critical | A supervisor session is fenced before it is replaced: never two live sessions per role | pending: m1 (LUCY-RUN-004) |
| sessions-05 | sessions | rule | high | Steering is addressed by airspace, flight and session generation, and refused when stale or ambiguous | pending: m1 (LUCY-RUN-004, LUCY-API-002) |
| public-01 | public-artifacts | incident | high | Pull request text and commit messages carry no internal notes, owner quotes, local paths or other airspaces' names | pending: m1 (LUCY-VAL-011) |
| public-02 | public-artifacts | incident | high | A bare @handle in public text is refused unless it is formatted as code | pending: m1 (LUCY-VAL-011) |
| public-03 | public-artifacts | rule | high | Nothing that looks like a secret reaches public text | pending: m1 (LUCY-VAL-011) |
| validation-01 | validation | rule | critical | The Pilot that wrote a change never decides a finding against it; such findings go to the owner | pending: m1 (LUCY-VAL-002, LUCY-SPEC-013) |
| validation-02 | validation | rule | high | Fix rounds are bounded: after the last one the finding becomes a decision for the owner | pending: m1 (LUCY-VAL-002) |
| validation-03 | validation | rule | medium | A review by the implementer's own model family is recorded as same-family | pending: m1 (LUCY-VAL-003) |
| validation-04 | validation | incident | high | A push happens only after every hard gate passed on the exact tree pushed; a reworded commit is validated again | pending: m1 (LUCY-VAL-005) |
