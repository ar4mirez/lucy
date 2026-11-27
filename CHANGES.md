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

- lucy's own security scans in CI, on every pull request, every push to `main` and weekly, each
  failing the run on a finding: govulncheck for Linux and macOS (a reachable known vulnerability
  in a dependency or the Go standard library), gosec over every rule with a reviewed baseline of
  inline suppressions, gitleaks over the new commits, and dependency review on pull requests (a
  vulnerable new dependency or a licence outside a permissive allowlist). OpenSSF Scorecard
  reports weekly to code scanning. `mise run security` runs the scanners locally at the versions
  CI pins.
- gosec's first findings were fixed or reviewed: unchecked close and rollback errors on error
  paths are discarded explicitly, and the doc checker reads files through a root-scoped handle.
  The policy rule set is version 4 (comment-only change; no rule changed).
- The STRIDE threat model is now decision D12, with an eighth trust boundary for lucy's own build
  and release pipeline and a check of every row m0 lands. The policy engine's technical notes moved
  to `docs/tech/policy.md`.
- Every release is installed through mise on Linux and macOS in CI after publishing, with
  attestation checks forced on.

- A conformance corpus: every safety rule an agent supervisor must keep, and every class of past
  incident, is a scenario that runs against lucy on the simulation harness. Corpus v1 has 88
  scenarios in 17 rule classes (merge authority, checks, approvals, attribution, bounded retries,
  Pilot liveness, the owner channel, untrusted input, roles, airspace isolation, the delete guard,
  unlanded work, closing references, durability, sessions, public text, validation): 58 run today
  and pass, each also with the daemon restarted before every step, and 30 are pending on the
  milestone that enables them. `mise run conformance` runs it, CI runs it on every change, and
  `docs/security/conformance.md` lists every scenario with its status.
- New m1 requirements found by the corpus: a Pilot reaches the owner only through the Tower
  (LUCY-RUN-013), and pull request text is linted before push (LUCY-VAL-011).
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
