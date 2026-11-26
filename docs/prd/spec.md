---
id: prd-spec
title: "PRD: ai-sdlc spec model"
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-SPEC-001, LUCY-SPEC-002, LUCY-SPEC-003, LUCY-SPEC-004, LUCY-SPEC-005, LUCY-SPEC-006, LUCY-SPEC-007, LUCY-SPEC-008, LUCY-SPEC-009, LUCY-SPEC-010, LUCY-SPEC-011, LUCY-SPEC-012, LUCY-SPEC-013, LUCY-SPEC-014, LUCY-SPEC-015, LUCY-SPEC-016, LUCY-SPEC-017, LUCY-SPEC-018, LUCY-SPEC-019, LUCY-SPEC-020]
---

# PRD: ai-sdlc spec model

Area `SPEC`. lucy adopts as much of the ai-sdlc spec as it can ([D3b](../decisions/D3b-ai-sdlc-spec.md)),
pinned at commit `b56cecaeef256644c3066fae2106af5c7eaa684a` (`v1alpha1`, draft). The adoption map
(36 concepts: 17 adopted, 13 adapted, 6 deferred) is summarised in [D3b](../decisions/D3b-ai-sdlc-spec.md);
the Source column below names the spec section, RFC or schema each requirement comes from.

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-SPEC-001 | Config uses the resource envelope (apiVersion, kind, metadata, spec, status); core kinds stay `ai-sdlc.io/v1alpha1` | m0 | spec 2 |
| LUCY-SPEC-002 | `metadata.namespace` is required and names the org | m0 | spec 3 |
| LUCY-SPEC-003 | Config files hold spec; the database holds status and never writes back into config | m0 | spec 4 |
| LUCY-SPEC-004 | The spec's JSON Schemas are vendored at the pinned commit; config is validated on load (2020-12, unknown fields rejected) | m0 | spec 7, 8 |
| LUCY-SPEC-005 | Spec resources map through an internal model, so a spec bump never breaks an org | m0 | P2-13 |
| LUCY-SPEC-006 | lucy kinds (Org, Project, Account, Task, Session, client-confirm) live under lucy's own apiVersion with schemas | m0 | spec 10 |
| LUCY-SPEC-007 | Status is reported as conditions (True, False, Unknown with a reason) on every surface | m0 | spec 6 |
| LUCY-SPEC-008 | Converge loops are level-triggered, idempotent reconcilers with backoff, woken by the event log | m0 | spec 9 |
| LUCY-SPEC-009 | The audit log is a hash chain (RFC 8785 + SHA-256) that passes the spec's cross-SDK fixture | m0 | spec 12 |
| LUCY-SPEC-010 | QualityGate and AutonomyPolicy are modelled and validated; the owner is the only override role | m0 | spec 5.3, 5.4 |
| LUCY-SPEC-011 | The vendored schemas validate the spec's own examples in CI | m0 | D3b |
| LUCY-SPEC-012 | Dispatch is refused until a task has a goal, delivery path, autonomy and acceptance test (readiness check) | m1 | RFC-0011 |
| LUCY-SPEC-013 | Decisions waiting on the owner are Decision resources projected from the event log; only the owner's words close them | m1 | RFC-0035 |
| LUCY-SPEC-014 | Research and ship pipelines use FailurePolicy, CredentialPolicy, ApprovalPolicy and PullRequest config; a timeout never approves a merge | m1 | spec 5.1 |
| LUCY-SPEC-015 | AgentRole covers supervisor, ship worker, researcher and reviewer; blockedPaths feed the delete guard | m1 | spec 5.2 |
| LUCY-SPEC-016 | AdapterBinding is the provider seam: berth is Sandbox, the gateway is SecretStore, lucy's pipeline is CodeAnalysis | m1 | spec 5.5 |
| LUCY-SPEC-017 | lucy passes the spec's Core conformance level; worktree pool and backlog use the spec's schemas | m1 | spec 11 |
| LUCY-SPEC-018 | AgentRole handoffs, demotion triggers and Adapter conformance | m2 | spec 5.2, 5.4, 11 |
| LUCY-SPEC-019 | lucy proposes autonomy promotions from metrics; only the owner grants one; spec fixes go upstream | m3 | spec 5.4, P2-13 |
| LUCY-SPEC-020 | Deferred concepts (Workflow document, complexity routing, priority policy, agentCard, session hierarchy) are priced one by one | m4 | D3b |
