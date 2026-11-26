---
id: runbook-release
title: Cutting a release
status: draft
version: 1
owner: ar4mirez
requirements: [LUCY-REL-001, LUCY-REL-002, LUCY-REL-003, LUCY-REL-004, LUCY-REL-005, LUCY-REPO-005]
---

# Cutting a release

**Stub.** The release pipeline lands in m0 slice 2 ([m0 brief](../milestones/m0.md)); this runbook is
filled in with it. Until then there are no releases.

## Planned flow

1. Merges to `main` with conventional commits keep a release PR up to date.
   TODO(LUCY-REL-005): choose and configure the release-PR tool.
2. Before merging the release PR, rotate CHANGES.md: move `## Unreleased` to
   `docs/changes/<version>.md` ([rotation](../changes/README.md)).
3. Merging the release PR tags `v<version>`; the tag triggers goreleaser.
   TODO(LUCY-REL-001): static binaries for linux and darwin, amd64 and arm64, plus `checksums.txt`.
4. TODO(LUCY-REL-002): a keyless cosign signature bundle over `checksums.txt`.
5. TODO(LUCY-REL-003): a GitHub artifact attestation for each archive.

## Check the release

- TODO(LUCY-REL-004): on Linux and macOS, `mise use -g github:ar4mirez/lucy@<version>` installs it and
  mise reports the attestation verified; `lucy --version` prints `<version>`.

## Roll back

- Install the previous version with `mise use -g github:ar4mirez/lucy@<previous>`; once
  `lucy self-update` exists, `lucy self-update --to <previous>`.
