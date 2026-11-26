---
id: D2
title: "D2: m1 runs one org end to end"
status: accepted
version: 3
owner: ar4mirez
requirements: [LUCY-RUN-012, LUCY-CORP-003, LUCY-ORG-007]
decided: 2026-11-26
---

# D2: m1 runs one org end to end

## Context

The first milestone has to prove lucy's value. A read-only view is cheap but proves little; running
real work proves the design.

## Decision

- **m1 runs one org, ar4mirez, end to end.** m2 and onward complete the replacement of the existing
  supervisor.
- The ledger and the `lucy preflight` checks fold into m1 as part of running the org, not a separate milestone.
- ar4mirez is the first org to move off the existing supervisor.
- No calendar estimates: work is planned by implementation order and token cost, because agentic
  development changes the time model.

## Consequences

- m0 must deliver the foundation m1 needs (API, event store, policy, spec model, simulation, corpus).
- m1 is the largest milestone; it starts in shadow mode and takes over one task class at a time with a
  one-command rollback (LUCY-RUN-012), per [R1](R1-adversarial-review.md) P1-10.
- An org cuts over only when its corpus is green and 20 consecutive tasks land with no lost work
  (LUCY-CORP-003, LUCY-ORG-007).

## Revisions

- **v1 (2026-11-26):** milestone 1 was "A: a read-only ledger plus doctor", catching this week's
  incident classes.
- **v2 (2026-11-26):** m1 switched to one org end to end, because running real work proves more
  than a read-only ledger.
