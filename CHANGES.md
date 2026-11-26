---
id: changes
title: Changes
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

- The Tower's flight state machine: a flight goes from dispatch through a Pilot session and its
  pull request to a Clearance from the policy engine and the Takeoff (the merge), or is grounded
  (the Pilot gave up, hung, was lost twice, or the pull request stalled). Every transition is a
  `task.*` event; a restarted Tower resumes every flight from the log. The owner posture holds a
  merge until the owner approves, and the approval lapses when the head or base moves.
  A cleared merge the forge fails with an error is tried again, backing off, and after three
  failures the flight holds for the owner with the forge's error.
  A merge the forge reports by another account, or by none it can name, ends the flight
  `outside` (`task.merged_outside`), never as lucy's takeoff.
- A simulation harness: a fake Pilot runtime, a fake forge and a virtual clock run whole flights
  deterministically in tests, with fault hooks for daemon restarts and Pilot crashes.
- Config is ai-sdlc spec resources (spec commit `b56ceca`): `lucy config validate [<dir>]` checks
  every `.yaml`, `.yml` and `.json` file against the vendored draft 2020-12 schemas and prints each
  resource's `Valid` condition and every violation. Unknown fields, a missing
  `metadata.namespace` and a `status` in a file are refused; schema defaults are applied.
- lucy's own kinds `Airspace`, `Runway`, `Flight` and `Pilot` under `lucy/v1alpha1`, as schema
  stubs.
- YAML config refuses anchors, aliases, merge keys and custom tags, and loading is size-limited;
  symlinked config files are not followed.
- `lucy daemon` loads `$XDG_CONFIG_HOME/lucy/resources` (or `--resources <dir>`) at start and
  records each resource's status in the event store, never in the files; `lucy config status` and
  the `resources.list` API method show it, along with the latest load's refusals that name no
  resource (a parse error, a missing namespace or name, a duplicate).
- Every event is now a hash-chained audit entry in the spec's wire format (RFC 8785 + SHA-256,
  schemaVersion 2), passing the spec's cross-SDK fixture. `lucy audit verify` and the
  `audit.verify` API method check the daemon's chain against its event log.
- The event log now starts each daemon run with a `config.loaded` event.
