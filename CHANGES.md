---
id: changes
title: Changes
status: active
version: 14
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- `lucy vault set` and `rm` record every change in the audit chain through the daemon, by org,
  name and operation only, before the vault is touched and again once the change is done or has
  failed; with no daemon running the change is refused and the vault is left as it was. The API
  gains `vault.record` (OpenRPC revision 12).
