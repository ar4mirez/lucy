---
id: changes
title: Changes
status: active
version: 39
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Changed

- The daemon's background work runs as converge loops woken by the event log: the Tower steps its
  flights as soon as a commit lands (a Pilot's question reaches `lucy questions` within a second of
  the runner's heartbeat, not on the next poll), still polls while a flight is active for what the
  log does not carry, and backs off after a failed step instead of retrying on every poll. Lease
  expiry runs the same way, so a finished run's capability token is revoked when the run's end is
  recorded, not on the next sweep.
