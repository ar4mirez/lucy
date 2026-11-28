---
id: changes
title: Changes
status: active
version: 52
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- A supervisor can suggest an answer to a Pilot's question. The suggestion is recorded
  (`task.answer_proposed`, naming the supervisor) and shown beside the question in `lucy
  questions` and `lucy decisions`; it never reaches the Pilot and never closes the question, under
  any posture: only your words do. `lucy answer <flight> --accept-proposal <proposal>`, naming the
  proposal `lucy questions` shows, answers with the suggestion, recorded as your answer; it is
  refused when the question or its suggestion changed since you saw it (OpenRPC revision 24). With
  no supervisor configured, which is the default, nothing changes.

- lucy records what each Pilot costs: every step's tokens (input, output, and the prompt cache's
  reads and writes) as a `usage` event of the run, and each run's cost as its harness reports it.
  `runs.list` shows a run's totals, `flights.list` and `lucy flights` a flight's, summed over every
  Pilot generation, with how many runs that is (OpenRPC revision 23: `runner.heartbeat`'s usage
  events, `runner.finish`'s `cost_micros`).
