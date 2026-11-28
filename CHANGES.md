---
id: changes
title: Changes
status: active
version: 51
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- lucy records what each Pilot costs: every step's tokens (input, output, and the prompt cache's
  reads and writes) as a `usage` event of the run, and each run's cost as its harness reports it.
  `runs.list` shows a run's totals, `flights.list` and `lucy flights` a flight's, summed over every
  Pilot generation (OpenRPC revision 23: `runner.heartbeat`'s usage events, `runner.finish`'s
  `cost_micros`).
