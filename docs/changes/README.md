---
id: changes-archive
title: Change history by version
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Change history by version

[CHANGES.md](../../CHANGES.md) holds only what changed since the last release, under
`## Unreleased`. This directory holds one file per released version, so no file grows without bound.

## Rotation, at every release

1. Move the `## Unreleased` section of CHANGES.md into a new file `docs/changes/<version>.md`
   (for example `docs/changes/0.1.0.md`), retitled `# <version> (<release date>)`.
2. Give the new file front matter (`id: changes-<version>`, `status: active`, `version: 1`, the
   requirement IDs the release delivered).
3. Leave an empty `## Unreleased` section at the top of CHANGES.md.
4. Add the version to the list below, newest first.

The release runbook ([docs/runbooks/release.md](../runbooks/release.md)) runs these steps.

## Versions

- [0.2.0](0.2.0.md) (2026-11-26)
- [0.1.0](0.1.0.md) (2026-11-26)
