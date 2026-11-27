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
  path guard when the command exits. The command runs in its own process group, which is killed when
  the run ends or stops. Ctrl-C, SIGTERM or a closed terminal (SIGHUP, unless started under `nohup`)
  stops the runner and its runs.
- `lucy runner --harness claude-code`: the runner drives Claude Code over its stream-json protocol
  instead of streaming lines. The runner refuses any Claude Code version but the one lucy is
  pinned to (2.1.332), so an upgrade is held until it passes the adapter's contract. Each session
  starts with no MCP servers, no user-level settings or hooks and no saved transcript. A Pilot's
  question is streamed as `asking` and answered only with the owner's answer, which the Tower
  relays through the runner's heartbeat and the log keeps only as a digest; every other permission
  prompt is refused. The `runner.heartbeat` result gains `answer` and `asking`, and its events an
  optional `question` id, so a question the owner answered never re-opens (OpenRPC revision 3).
- The harness contract: recorded protocol transcripts of the pinned Claude Code version, replayed
  in CI without a harness, a model or a login, and a nightly check of the newest release that
  reports it ready or held and never fails.
- The flight state machine can start Pilots through the runner protocol; simulated flights land
  through it, including a crashed Pilot, a runner cut off past its lease and daemon restarts.

### Changed

- The m1 plan is active, with its 16 scope steps broken into PR-sized slices sized in tokens.
