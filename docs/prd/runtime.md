---
id: prd-runtime
title: "PRD: runners, sessions, sandbox and credentials"
status: active
version: 3
owner: ar4mirez
requirements: [LUCY-RUN-001, LUCY-RUN-002, LUCY-RUN-003, LUCY-RUN-004, LUCY-RUN-005, LUCY-RUN-006, LUCY-RUN-007, LUCY-RUN-008, LUCY-RUN-009, LUCY-RUN-010, LUCY-RUN-011, LUCY-RUN-012, LUCY-RUN-013, LUCY-SBX-001, LUCY-SBX-002, LUCY-SBX-003, LUCY-CRED-001, LUCY-CRED-002, LUCY-CRED-003, LUCY-CRED-004, LUCY-CRED-005, LUCY-CRED-006]
---

# PRD: runners, sessions, sandbox and credentials

Areas `RUN`, `SBX` and `CRED`. Background: [D3a](../decisions/D3a-berth-sandbox.md),
[D3c](../decisions/D3c-credential-free-workers.md), [D5](../decisions/D5-session-ownership.md) and
[R1](../decisions/R1-adversarial-review.md) (P0-3, P1-4, P1-5, P1-7, P1-10, P1-12).

## RUN: runners, sessions and supervision

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-RUN-001 | A runner protocol (lease task, stream events, heartbeat, fencing token); this machine is the first runner | m1 | P1-4 |
| LUCY-RUN-002 | Claude Code runs over its structured protocol; a PTY is only a viewing path or a named, quarantined adapter | m1 | P0-3 |
| LUCY-RUN-003 | Harness adapters are pinned; nightly contract tests run against the latest harness; failing upgrades are held | m1 | P1-12 |
| LUCY-RUN-004 | Sessions hold a lease with generation and heartbeat; reattach before spawn; fence, then replace; never two live sessions per role | m1 | D5, incident 2 |
| LUCY-RUN-005 | Every session survives all its viewers closing and the daemon restarting | m1 | D5 |
| LUCY-RUN-006 | Every task gets its own TMPDIR; every org's sessions get a synthetic HOME | m1 | D3a |
| LUCY-RUN-007 | Built-in worktree pool: slots leased in SQLite, reset on acquire, released only with a landed-work proof | m1 | Rationale: unlanded work is never torn down |
| LUCY-RUN-008 | Account state (onboarding, trust, hooks) is re-asserted before every spawn; a task needing a human is held, not wedged | m1 | incident 3 |
| LUCY-RUN-009 | Bundled skills carry content hashes and are re-linked and verified at every spawn | m1 | incident 4 |
| LUCY-RUN-010 | Supervision routes events through rules, then a small model, then a large one; supervisor tokens per org-day are tracked | m1 | P1-7 |
| LUCY-RUN-011 | Token cost is recorded per task and per step | m1 | P1-6 |
| LUCY-RUN-012 | Shadow mode, then takeover by task class; `lucy handback` returns a class to the existing supervisor in one command | m1 | P1-10 |
| LUCY-RUN-013 | A Pilot has no channel to the owner: its questions and output reach the owner only as the Tower's records, rendered by lucy's surfaces | m1 | Corpus v1 (channel-04) |

## SBX: org sandbox

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-SBX-001 | Every worker and validation agent runs in its org's berth container through berth's API; idle orgs are stopped | m1 | D3a |
| LUCY-SBX-002 | The runtime is a provider seam, so a cloud runner can be added without changing the core | m1 | D3c |
| LUCY-SBX-003 | lucy never restarts or recreates an org container with live sessions without the owner's confirmation | m1 | D3a |

## CRED: credential gateway

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-CRED-001 | A per-org gateway process outside the agents' container adds tokens only to allowlisted hosts and paths, never on redirects | m1 | D3c, P1-5 |
| LUCY-CRED-002 | Credentials are encrypted at rest with a key per org | m1 | P1-5 |
| LUCY-CRED-003 | Capability tokens are scoped to (org, task, action, expiry) | m1 | P1-5 |
| LUCY-CRED-004 | The gateway denies private, link-local and metadata addresses, fails closed and logs every request to the audit chain | m1 | checklist 2.1, 5.1 |
| LUCY-CRED-005 | No token is ever inside a container; `lucy preflight --check` scans environments and mounts to prove it | m1 | D3c |
| LUCY-CRED-006 | Offboarding an org provably erases its data and rotates its credentials | m2 | P1-5 |
