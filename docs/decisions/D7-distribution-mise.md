---
id: D7
title: "D7: distribute through mise and GitHub Releases"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-REL-001, LUCY-REL-002, LUCY-REL-003, LUCY-REL-004, LUCY-REL-005, LUCY-REL-006]
decided: 2026-11-26
---

# D7: distribute through mise and GitHub Releases

## Context

Angel: keep it simple at first, distribute through mise connected to GitHub, and release often.

## Decision

- Install with the mise GitHub backend: `mise use -g github:ar4mirez/lucy` (LUCY-REL-004).
- goreleaser builds static binaries for linux and darwin, amd64 and arm64, with checksums
  (LUCY-REL-001); a keyless cosign bundle signs `checksums.txt` (LUCY-REL-002); each archive gets a
  GitHub artifact attestation (LUCY-REL-003). mise verifies attestations and SLSA provenance for
  `github:` tools by default.
- Release from conventional commits on main; tags trigger goreleaser (LUCY-REL-005).
- `lucy self-update` verifies the new binary; `--to <version>` rolls back (LUCY-REL-006).
- Homebrew and other channels later (Homebrew in m3).

## Consequences

- The release runbook is [docs/runbooks/release.md](../runbooks/release.md).
- Release signing uses GitHub's OIDC identity, so no long-lived signing key exists to leak.
