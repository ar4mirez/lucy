---
id: changes
title: Changes
status: active
version: 15
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- Each run reaches its org's credential gateway through its own gateway socket, opened by the
  runner in the run's directory and named in `LUCY_RUN_GATEWAY`: the runner forwards each request
  with the run's capability token added, renewed after each heartbeat, and drops any token the run
  sent, so the token never enters the Pilot's process, environment or files. `lucy runner
  --gateway-dir` names where the orgs' gateway sockets are (`gateway/` in lucy's runtime directory
  by default).
- `lucy vault set` and `rm` record every change in the audit chain through the daemon, by org,
  name and operation only, before the vault is touched and again once the change is done or has
  failed; with no daemon running the change is refused and the vault is left as it was. The API
  gains `vault.record` (OpenRPC revision 12).
