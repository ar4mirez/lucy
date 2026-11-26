---
id: prd-operations
title: "PRD: setup, doctor, operations, multi-org and breadth"
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-OPS-001, LUCY-OPS-002, LUCY-OPS-003, LUCY-OPS-004, LUCY-OPS-005, LUCY-OPS-006, LUCY-ORG-001, LUCY-ORG-002, LUCY-ORG-003, LUCY-ORG-004, LUCY-ORG-005, LUCY-ORG-006, LUCY-ORG-007, LUCY-BRD-001, LUCY-BRD-002, LUCY-BRD-003, LUCY-BRD-004]
---

# PRD: setup, doctor, operations, multi-org and breadth

Areas `OPS`, `ORG` and `BRD`. Background: [D8 built-in vs external](../decisions/D8-built-in-vs-external.md)
and [R1](../decisions/R1-adversarial-review.md) (P1-6, P2-14 to P2-19).

## OPS: setup, doctor and operations

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-OPS-001 | `lucy setup` plans first, then installs every external tool from an embedded, pinned manifest through mise in lucy's own space | m1 | D8 |
| LUCY-OPS-002 | `lucy doctor` checks every inventory row, the incident classes and the security invariants, reports conditions and repairs with `--repair` | m1 | D8 |
| LUCY-OPS-003 | lucy takes a daily online database backup into the state dir; `lucy backup --to` copies it off the machine | m1 | report 4.2 |
| LUCY-OPS-004 | OpenTelemetry traces per task, a flight recorder per session and `lucy replay` | m1 | P2-17 |
| LUCY-OPS-005 | The daemon restarts and upgrades without stopping runners or their sessions | m1 | P2-14 |
| LUCY-OPS-006 | Per-org feature flags, versioned prompts with rollback and restore drills | m3 | P2-14 |

## ORG: every org on lucy

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-ORG-001 | Multi-org supervisors with per-org credentials; a cross-org read is refused and audited | m2 | report 4.4 |
| LUCY-ORG-002 | Seat pools, quota-aware placement, twin config dirs, failover and return, hard per-org budget stops | m2 | P1-6 |
| LUCY-ORG-003 | A resource-aware scheduler: memory caps per runner, per-org concurrency, priorities, preemption | m2 | P2-15 |
| LUCY-ORG-004 | Typed decisions, preferences and learnings per org, with provenance and expiry, recalled per task | m2 | P2-16 |
| LUCY-ORG-005 | `lucy import firstmate` carries history, decisions and postures over, with a dry run first | m2 | P2-19 |
| LUCY-ORG-006 | The client-confirm posture needs the captain's relayed decision through the API | m2 | report 3 |
| LUCY-ORG-007 | An org cuts over only after 20 consecutive tasks on lucy with no lost work | m2 | report 9.1b |

## BRD: breadth, priced per item

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-BRD-001 | More harnesses (Codex, ACP harnesses) behind the same adapter contract | m4 | report 4.6 |
| LUCY-BRD-002 | An Agent Substrate runner on the runner protocol | m4 | D3c |
| LUCY-BRD-003 | ai-memory as an optional, per-org memory provider | m4 | report 3 |
| LUCY-BRD-004 | `lucy init` scaffolds this doc structure (full or lite profile) in a project | m4 | D9 |
