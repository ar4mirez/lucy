---
id: changes
title: Changes
status: active
version: 46
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- Only you can override a QualityGate. A gate whose `override.requiredRole` (or a visual rule whose
  `override.approvers`) names anyone else is still loaded, as the spec writes it, but `lucy config
  status` shows the condition `Override` (`OverrideIsOwner`) saying lucy lets only you override
  it, and the daemon logs a warning. Its `requiresJustification` is kept.

- `lucy config reload` (the API's `config.reload`, OpenRPC revision 21) and SIGHUP make the daemon
  load its config again without a restart. A config with no refused resource is applied: its
  statuses are recorded, its new Ready Flights dispatched (with `--dispatch`), its flights in flight
  tightened. A config with refused resources is not applied, and the command lists why, but its
  strictening still applies: a flight whose Runway or AutonomyPolicy became invalid or vanished
  moves to owner. A config directory that cannot be read, or no longer exists, fails the reload:
  the last good config stays whole, no flight is tightened, `daemon.config_reload_failed` records
  why, and `lucy config status` shows the last reload that failed or was refused. A loosening never
  applies to a flight in flight. The roles' blocked paths and the
  gateway's routes still take a restart.

- Demoting a Runway's autonomy level now tightens its flights already in flight, on `lucy config
  reload`, SIGHUP or the next start: each moves to the stricter posture, recorded as `task.posture_tightened` with the old
  and new posture and why, and its next merge is decided again under it. A promotion applies only
  to Flights dispatched afterwards, never to one in flight. A flight whose posture can no longer be
  resolved from the config moves to owner (a client flight stays client): its Runway is removed or
  refused, its Runway's AutonomyPolicy is removed or has a field lucy refuses, or neither its Runway
  nor its Flight states a posture any more.

- A project's merge posture can come from the spec's AutonomyPolicy: a Runway names a policy and a
  level (`autonomyPolicy`, `autonomyLevel`), and lucy reads only that level's
  `guardrails.requireApproval`: `all` is the owner posture, `security-critical-only` the green
  posture, with the fixed list still needing you; `none` and `architecture-changes-only` are
  refused. `clientConfirms: true` on a Runway adds the client's confirmation, relayed by you. A
  Flight no longer needs its own `autonomy` when its Runway sets one, and its own may only tighten
  it. Every other field of a policy a Runway names is refused by name, and a Flight whose posture
  cannot be set is held with reason `AutonomyInvalid`. Promotion and demotion stay yours, by hand.
  `flights.list` reports the posture a Flight is flown under (OpenRPC revision 20).

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
