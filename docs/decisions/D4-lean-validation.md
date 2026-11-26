---
id: D4
title: "D4: lucy's own lean validation pipeline"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-VAL-001, LUCY-VAL-002, LUCY-VAL-005, LUCY-VAL-008, LUCY-VAL-010]
decided: 2026-11-26
---

# D4: lucy's own lean validation pipeline

## Context

no-mistakes sets today's quality bar but is heavy: its telemetry shows about 10.5M processed tokens and
17 agent-minutes per PR before review, 60% of the tokens spent on an agent driving test runs.

## Decision

- lucy has its own validation: the same quality of results, but lightweight, fast and far lower token
  use. Inspired by no-mistakes; it includes ai-sdlc's independent reviewer and signed proofs natively.
- Don't reinvent needlessly: orgs may import the no-mistakes skill as an optional skill (LUCY-VAL-010).
- Principle for all of lucy: build as much as possible inside lucy; glue to outside tools only when
  necessary, to avoid a fragile multi-tool chain.
- The pipeline lands in the milestone where lucy starts running real work, so lucy never depends on
  no-mistakes. Under [D2](D2-first-milestone.md) as revised, that is **m1** (ar4mirez).

## Consequences

- Build, test and lint run as plain processes; agents only review and fix, in the worker's warm
  session (LUCY-VAL-001, LUCY-VAL-002). Estimated 0.9-3.3M tokens per PR, 70-90% less validation.
- Push is guarded by every hard gate on the exact tree (LUCY-VAL-005).
- "Same quality" is measured, not assumed: a replay benchmark comes first (LUCY-VAL-008).

## Note on milestone numbering

The decision log's D4 text names "M2, ar4mirez", written before D2 was revised. D2 v2 moved ar4mirez
to m1, and the rule "the milestone where lucy runs real work" puts the pipeline in m1.
