---
id: changes
title: Changes
status: active
version: 3
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- The runner protocol, the first part of milestone m1: a runner leases a queued run under a
  fencing token, renews the lease with a heartbeat that also streams the run's events into the
  event log, and finishes the run with its outcome. A lease that is not renewed in time expires,
  the run is reported lost, and anything sent under the old fencing token is refused, so a runner
  that was cut off can never report into, or finish, work that has already been replaced. Leases
  survive a daemon restart. The API gains `runner.queued`, `runner.lease`, `runner.heartbeat`,
  `runner.finish` and `runs.list`, a `run` scope and a `fenced` error (OpenRPC revision 2).
- `lucy runner -- <command>`: this machine as the first runner. It runs the command once per leased
  run in a fresh scratch directory, with only PATH, HOME, TMPDIR and the run's `LUCY_RUN_*`
  variables in its environment, streams each line of output, and deletes the directory through the
  path guard when the command exits.
- The flight state machine can start Pilots through the runner protocol; simulated flights land
  through it, including a crashed Pilot, a runner cut off past its lease and daemon restarts.

### Changed

- The m1 plan is active, with its 16 scope steps broken into PR-sized slices sized in tokens.
