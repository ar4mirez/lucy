---
id: D9
title: "D9: repository doc structure and doc rules"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-REPO-002, LUCY-REPO-003, LUCY-REPO-004, LUCY-REPO-005, LUCY-BRD-004]
decided: 2026-11-26
---

# D9: repository doc structure and doc rules

## Context

The doc structure follows a common agentic-docs layout, with improvements. lucy's readiness check will later read docs as data, so they must be machine-readable.

## Decision

- Root docs: AGENTIC.md, AGENTS.md, CLAUDE.md (a one-line import of AGENTS.md, no duplicate),
  CHANGES.md (rotated per version into docs/changes/), EXTENSIONS.md, PRODUCT.md, SECURITY.md, TECH.md.
- Doc directories: docs/decisions, docs/milestones, docs/prd, docs/runbooks, docs/security,
  docs/changes.
- Improvements: a size cap per file and a split by area; stable requirement IDs with an index; front
  matter with status, version, owner and requirement IDs on every doc (LUCY-REPO-002 to
  LUCY-REPO-005).
- A lite profile for small projects; `lucy init` scaffolds the structure later (LUCY-BRD-004).

## Consequences

- The rules are written in [AGENTIC.md](../../AGENTIC.md) and enforced by `go test ./internal/doccheck`.
- Docs are a restricted YAML front matter subset so lucy needs no YAML dependency to read them.
