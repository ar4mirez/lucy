---
id: prd-quality
title: "PRD: simulation, conformance corpus and validation"
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-SIM-001, LUCY-SIM-002, LUCY-SIM-003, LUCY-CORP-001, LUCY-CORP-002, LUCY-CORP-003, LUCY-CORP-004, LUCY-VAL-001, LUCY-VAL-002, LUCY-VAL-003, LUCY-VAL-004, LUCY-VAL-005, LUCY-VAL-006, LUCY-VAL-007, LUCY-VAL-008, LUCY-VAL-009, LUCY-VAL-010]
---

# PRD: simulation, conformance corpus and validation

Areas `SIM`, `CORP` and `VAL`. Background: [R1 adversarial review](../decisions/R1-adversarial-review.md)
(P0-1, P1-9, P1-11) and [D4 lean validation](../decisions/D4-lean-validation.md).

## SIM: simulation and chaos

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-SIM-001 | A fake harness, fake forge and fake clock drive the supervisor state machine deterministically | m0 | P1-9 |
| LUCY-SIM-002 | The simulation suite shows the policy engine refusing every privileged action no rule allows | m0 | P0-2, P1-9 |
| LUCY-SIM-003 | A chaos suite kills the daemon or runner mid-merge, fills the disk, skews the clock, upgrades the harness, tries rm -rf and exhausts quota; kill-chain scenarios included | m1 | P1-9, checklist 1.7 |

## CORP: conformance corpus

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-CORP-001 | A corpus harness runs executable scenarios against lucy in the simulation | m0 | P0-1 |
| LUCY-CORP-002 | Corpus v1 encodes every safety rule and recorded incident of the existing supervisor; red is expected in m0 | m0 | P0-1 |
| LUCY-CORP-003 | The corpus covers ar4mirez's flows and is green before ar4mirez moves | m1 | P0-1, P1-10 |
| LUCY-CORP-004 | The corpus covers each client org's rules and is green before that org moves | m2 | P0-1 |

## VAL: lean validation pipeline

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-VAL-001 | Build, test and lint run as plain processes with failures-only output (`lucy check`); no agent drives them | m1 | D4 |
| LUCY-VAL-002 | Fixes run in the implementing worker's warm session, at most two rounds, then a Decision | m1 | D4 |
| LUCY-VAL-003 | Review is diff-scoped from a context pack, by a different model family than the implementer, or recorded as same-family | m1 | D4, P1-11 |
| LUCY-VAL-004 | The docs check runs only when the diff touches a documented surface, on a cheap model | m1 | D4 |
| LUCY-VAL-005 | Push happens only after every hard gate passed on the exact tree; CI is watched with zero tokens | m1 | D4 |
| LUCY-VAL-006 | Stage results are cached by tree hash and stage config hash | m1 | D4 |
| LUCY-VAL-007 | Each PR gets a sigstore-signed proof bundle, verified before merge; only the proof's head commit merges | m1 | D3b, RFC-0042 |
| LUCY-VAL-008 | Before replacing anything, the pipeline replays past PRs and matches no-mistakes' blocking catches | m1 | P1-11 |
| LUCY-VAL-009 | One merge function: never red, never with a required check unreported, closing references checked | m1 | P0-2, D4 |
| LUCY-VAL-010 | Orgs may import the no-mistakes skill as an optional, pinned, hash-verified skill | m1 | D4 |
