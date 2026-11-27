---
id: prd-security
title: "PRD: security"
status: active
version: 3
owner: ar4mirez
requirements: [LUCY-SEC-001, LUCY-SEC-002, LUCY-SEC-003, LUCY-SEC-004, LUCY-SEC-005, LUCY-SEC-006, LUCY-SEC-007, LUCY-SEC-008, LUCY-SEC-009, LUCY-SEC-010, LUCY-SEC-011, LUCY-SEC-012, LUCY-SEC-013]
---

# PRD: security

Area `SEC`. Background: [SECURITY.md](../../SECURITY.md), the [threat model](../decisions/D12-threat-model.md)
and the [checklist map](../security/checklist.md). Many checklist items are met by requirements in
other areas; the checklist map names them.

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-SEC-001 | lucy runs as the user, never root; state files are 0600 and dirs 0700 | m0 | checklist 1.2 |
| LUCY-SEC-002 | CI runs vulnerability, dependency and licence, static-analysis and secret scanning on lucy's own code and fails on findings | m0 | checklist 4.6 |
| LUCY-SEC-003 | The STRIDE threat model is kept as an ADR and re-checked at every milestone exit | m0 | checklist 1.5 |
| LUCY-SEC-004 | Vulnerabilities are reported through GitHub private advisories, as SECURITY.md says | m0 | checklist 5.3 |
| LUCY-SEC-005 | Every delete lucy makes refuses `/`, `$HOME` and anything outside lucy's managed roots | m1 | incident 1 |
| LUCY-SEC-006 | API rate limits per device and pairing-attempt limits | m1 | checklist 2.6 |
| LUCY-SEC-007 | `lucy preflight --check` checks a host baseline: disk encryption, firewall, `/home` snapshots, OS updates | m1 | checklist 4.7 |
| LUCY-SEC-008 | Review applies a security lens to security-sensitive diffs; project SAST and dependency scans are hard gates | m1 | checklist 5.6, 5.7 |
| LUCY-SEC-009 | Deterministic detection rules over events: token patterns, unexpected egress, repeated denials, cross-org attempts, new devices | m2 | checklist 5.2 |
| LUCY-SEC-010 | `lucy incident` freezes an org, exports evidence and turns each lesson into a corpus scenario | m2 | checklist 5.3 |
| LUCY-SEC-011 | Key rotation and encrypted per-org backups | m2 | checklist 3.7, 4.3 |
| LUCY-SEC-012 | Token-reuse anomaly checks for remote sessions | m3 | checklist 3.8 |
| LUCY-SEC-013 | DAST per project, and Kubernetes RBAC with workload identity for the Agent Substrate runner | m4 | checklist 4.4, 5.7 |
