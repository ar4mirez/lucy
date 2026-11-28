---
id: changes
title: Changes
status: active
version: 41
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Changed

- A Pilot that cannot start during an outage no longer grounds its flight: the failed start is
  recorded (`task.pilot_start_failed`) and retried after a backoff that doubles with each failure,
  and `lucy flights` shows why and when it is tried next. Only a start the runner protocol refuses
  for good (an invalid task, a run id another flight holds, an older generation) still grounds the
  flight with `start_failed`.

- The daemon's background work runs as converge loops woken by the event log: the Tower steps its
  flights as soon as a commit lands (a Pilot's question reaches `lucy questions` within a second of
  the runner's heartbeat, not on the next poll), still polls while a flight is active for what the
  log does not carry, and backs off a flight whose step fails, that flight alone, instead of
  retrying it on every poll; `flights.list` shows its `step_error` and `retry_at` (OpenRPC
  revision 19). Lease expiry runs the same way, so a finished run's capability token is revoked
  when the run's end is recorded, not on the next sweep, and still revoked when an expiry fails.
