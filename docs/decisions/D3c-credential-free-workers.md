---
id: D3c
title: "D3c: credential-free workers; a runtime seam for cloud orgs"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-CRED-001, LUCY-CRED-005, LUCY-SBX-002]
decided: 2026-11-26
---

# D3c: credential-free workers; a runtime seam for cloud orgs

## Context

Agent Substrate (Apache-2.0, 1.0) proves a "zero credentials in the sandbox" pattern: an egress
gateway outside the sandbox injects tokens. It needs Kubernetes with microVMs, which is too heavy for
one 11 GB workstation.

## Decision

- Workers never hold secrets, from the first worker: a per-org gateway adds tokens to approved
  outbound requests, alongside berth's firewall (LUCY-CRED-001, LUCY-CRED-005).
- Cloud orgs stay possible through a runtime-provider seam (LUCY-SBX-002); no Kubernetes provider now.

## Consequences

- A compromised or confused worker has nothing to exfiltrate.
- **Open risk:** the worker's own model login. The plan is for the gateway to inject it; if Claude
  Code's subscription login cannot work that way, a per-org login file mounted read-only is an
  explicit exception the captain must approve (tracked in [m1](../milestones/m1.md)).
- An Agent Substrate runner is an m4 item (LUCY-BRD-002).
