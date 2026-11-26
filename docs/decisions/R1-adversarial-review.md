---
id: R1
title: "R1: adversarial architecture review, adopted"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-CORP-001, LUCY-POL-001, LUCY-RUN-002, LUCY-RUN-001, LUCY-API-001, LUCY-SIM-001, LUCY-RUN-012, LUCY-VAL-008, LUCY-REPO-006]
decided: 2026-11-26
---

# R1: adversarial architecture review, adopted

## Context

An independent review of the design report and decisions D1-D6, by a reviewer who did not write the
design. Every item is adopted.

## Items

| ID | Finding | Change | Lands |
|---|---|---|---|
| P0-1 | The product is the judgment layer; a rewrite drops tacit rules | Conformance corpus of every safety rule and incident; an org moves only when it is green | m0-m2 |
| P0-2 | LLM proposes, policy disposes | Deterministic policy engine on every privileged action; outside text never raises privileges | m0 |
| P0-3 | PTY control caused this week's incidents | Structured protocols first; a PTY only for viewing | m1 |
| P1-4 | Control plane vs runners | Runner protocol: lease, events, heartbeat, fencing | m1 |
| P1-5 | The daemon is a high-value target | Per-org encrypted credentials, separate gateway process, scoped tokens, offboard and erase | m1-m2 |
| P1-6 | Accounts and quota | Cost per task and step (m1); seat pools, failover, budgets (m2) | m1-m2 |
| P1-7 | Supervisor tokens | Rules, then a small model, then a large one; tokens per org-day tracked | m1 |
| P1-8 | Five surfaces drift | One typed API; surfaces hold no logic | m0 |
| P1-9 | Simulation and chaos as architecture | Fake harness, forge and clock; chaos suite | m0-m1 |
| P1-10 | Cutover risk | Shadow mode, takeover by task class, one-command handback | m1 |
| P1-11 | Unmeasured validation | Replay benchmark; cross-family reviewer | m1 |
| P1-12 | Harness churn | Pinned adapters, nightly contract tests, held upgrades | m1 |
| P2-13 | Spec dependency | Pin, internal model, upstream contributions | m0, m3 |
| P2-14 | Upgrades while work runs | Migrations, runner-safe restarts, versioned prompts, flags | m0-m3 |
| P2-15 | One 11 GB machine | Resource-aware scheduler | m2 |
| P2-16 | Knowledge | Typed, expiring per-org knowledge | m2 |
| P2-17 | Observability | Traces, flight recorder, replay | m1 |
| P2-18 | Scope and bus factor | Small core; Lavish and gh-axi stay external; non-goals in the README | m0 |
| P2-19 | Migration | `lucy import firstmate` with a dry run | m2 |

## Where the review and a decision differ, the decision stands

- Viewers and remote control stay in m1 ([D5](D5-session-ownership.md)), kept cheap by the m0 API.
- The credential gateway lands with the first worker in m1 ([D3c](D3c-credential-free-workers.md)),
  already hardened per P1-5; offboarding follows in m2.
- Build-inside ([D4](D4-lean-validation.md)) and a small core (P2-18) meet in the middle: lucy builds
  its own GitHub client, quota data and decision cards; Lavish and the worker CLIs stay optional.

## Consequences

Net addition to the plan: 27-56 PRs, about 0.7-3.1B processed tokens, already inside the milestone
totals.
