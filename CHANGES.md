---
id: changes
title: Changes
status: active
version: 54
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- The supervisor asks rules before any model. Built-in safety rules send a question about a secret,
  a credential, a payment or an irreversible or production change to you, never to a model. Your
  own ordered rules in the org's Airspace (`spec.supervisor.rules`, RE2 matches) either suggest a
  fixed answer or send the question to you. Rules call nothing and spend nothing, so they apply
  with the Airspace alone, without `--supervise`. The model settings are now optional, all four or
  none. See the [supervisor runbook](docs/runbooks/supervisor.md) (OpenRPC revision 26).
- A model supervisor can suggest answers to Pilots' questions. An org opts in through its Airspace
  (`spec.supervisor`: a gateway route, a small and a large model, a daily token budget) and the
  daemon arms it with `lucy daemon --supervise`; both are needed, and it is off by default. Its model
  calls go only through the org's credential gateway, with a short-lived capability bound to that
  route, so the daemon holds no key. It suggests only: you answer. `lucy status` shows each org's
  tokens today against its budget (OpenRPC revision 25); a spent budget, a refusal or an error
  leaves the question to you, recorded as `supervisor.declined`.
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
