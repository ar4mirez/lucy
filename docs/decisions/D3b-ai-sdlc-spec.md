---
id: D3b
title: "D3b: adopt as much of the ai-sdlc spec as possible"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-SPEC-001, LUCY-SPEC-004, LUCY-SPEC-005, LUCY-SPEC-012, LUCY-VAL-003, LUCY-VAL-007]
decided: 2026-11-26
---

# D3b: adopt as much of the ai-sdlc spec as possible

## Context

ai-sdlc (Apache-2.0) is a peer project with a declarative resource model (`Pipeline`, `AgentRole`,
`QualityGate`, `AutonomyPolicy`, `AdapterBinding`, `Decision`), a readiness gate, independent review
and signed attestations. Its spec is `v1alpha1`, a draft with no stability guarantee.

## Decision

Angel: "The more we can borrow and use from spec the better even if is planned." lucy adopts the spec
pinned at commit `b56cecaeef256644c3066fae2106af5c7eaa684a`
(https://github.com/ai-sdlc-framework/ai-sdlc/blob/b56cecaeef256644c3066fae2106af5c7eaa684a/spec/spec.md):

- its resource schemas as lucy's config format (LUCY-SPEC-001, LUCY-SPEC-004);
- the readiness check before dispatch (LUCY-SPEC-012);
- the independent reviewer, a different model family from the implementer (LUCY-VAL-003);
- signed proofs per PR (LUCY-VAL-007).

## Adoption map

36 concepts: 17 adopted, 13 adapted to lucy's model (one captain, orgs, explicit autonomy), 6 deferred.
Deferred: `Pipeline` stages (deprecated by the spec), complexity routing, `agentCard`, the priority
policy, the session hierarchy, and the Workflow document. The per-concept rows are the `SPEC`
requirements in [docs/prd/spec.md](../prd/spec.md).

## Consequences

- The spec is pinned and mapped through an internal model, so a spec bump never breaks an org
  (LUCY-SPEC-005); each later bump is its own small item.
- The vendored schemas keep their Apache-2.0 licence, with a NOTICE entry when they land.
- Autonomy is never inferred: lucy may propose a promotion, only the captain grants it.
