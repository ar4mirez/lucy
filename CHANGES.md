---
id: changes
title: Changes
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- `lucy daemon` runs lucy's control plane in the foreground: it opens the event store and serves
  lucy's typed API on a unix socket, `$XDG_RUNTIME_DIR/lucy/lucy.sock` (0600, in a 0700 directory),
  refusing connections from other users by their kernel peer credentials. It never opens a network
  listener. `lucy daemon stop` stops it through the API.
- `lucy status` shows the running daemon's version, API version, uptime and event log head;
  `lucy events [--after <seq>] [--limit <n>]` lists events from its log. Both exit 1 with a hint
  when no daemon is running.
- The API itself (ADR D10): restricted JSON-RPC 2.0 over HTTP at `/rpc/v1` with `hello`, `health`,
  `status.get`, `events.list`, `rpc.discover` and `daemon.shutdown`; strict input validation, size
  limits and timeouts; typed errors that never carry internal details; every mutating call recorded
  as an `api.command` event before it runs. Its OpenRPC schema is `internal/api/openrpc.json`.
- Runbook for the daemon and its API ([docs/runbooks/daemon.md](docs/runbooks/daemon.md)).
- The policy engine: deterministic Go rules decide every privileged action (merge, delete,
  force-push, branch delete, history rewrite, credential use) and cross-org access, with default
  deny. A decision is `allow`, `deny` or `require-captain` and names its rule, reason and the rule
  set's version and SHA-256; any evaluation error denies. Merges need green, fully reported checks
  and the project's posture (captain by default); credentials are for the gateway role only; a
  privileged request based only on outside text (issues, PR comments, web pages, tool output) is
  denied.
- A path guard for deletes: it refuses `/`, `$HOME` and anything outside the declared project and
  scratch roots after resolving symlinks, plus `..` tricks and invisible or lookalike characters.
- API methods `policy.evaluate` (asks for a decision; the daemon records it as a `policy.decision`
  event before answering) and `policy.decisions` (pages the recorded decisions).

### Changed

- API params are now checked strictly inside nested objects too: unknown or case-folded members
  in an object inside params, or inside an array of objects, are refused.
