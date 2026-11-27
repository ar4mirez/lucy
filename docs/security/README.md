---
id: security-docs
title: Security documents
status: active
version: 4
owner: ar4mirez
requirements: []
---

# Security documents

[SECURITY.md](../../SECURITY.md) has the reporting policy and a summary of the model.

| Document | Holds |
|---|---|
| [D12](../decisions/D12-threat-model.md) | The STRIDE threat model: eight trust boundaries, each row with the milestone that checks it (a decision record) |
| [checklist.md](checklist.md) | The owner's 38-item security checklist mapped to milestones and requirement IDs |
| [conformance.md](conformance.md) | The conformance corpus: every scenario with its rule class and status (generated) |
| [reviews/sast-baseline.md](reviews/sast-baseline.md) | The reviewed gosec suppressions in lucy's code, checked against the code by a test |

Reviews and scan reports live in `reviews/`, one file each. How lucy's own scans run:
[security scans runbook](../runbooks/security-scans.md).
