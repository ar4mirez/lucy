---
id: changes
title: Changes
status: active
version: 16
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- A daemon restart no longer loses runs whose runners kept working while it was down. Before it
  expires anything, a restarted daemon gives every held lease a grace window of two lease lives,
  recorded as `run.resumed`, so each runner reattaches under its old fence and its flight keeps its
  Pilot; a runner that is really gone is expired once the window ends. A capability token is not
  honoured during the window until its runner renews the lease, and a lease already in a window
  is not extended by another restart.

- Each run reaches its org's credential gateway through its own gateway socket, opened by the
  runner in the run's directory and named in `LUCY_RUN_GATEWAY`: the runner forwards each request
  with the run's capability token added, renewed after each heartbeat, and drops any token the run
  sent, so the runner never puts the token in the Pilot's process, environment or files. A Pilot
  on the host still shares the daemon's uid and so its API authority for its own org (#36). `lucy runner
  --gateway-dir` names where the orgs' gateway sockets are (`gateway/` in lucy's runtime directory
  by default). The design and its residual risk are recorded as decision D13. The runner refuses to
  start under a `--root` too long for a run's socket, symlinks resolved, naming the longest it
  takes.
- `lucy vault set` and `rm` record every change in the audit chain through the daemon, by org,
  name and operation only, before the vault is touched and again once the change is done or has
  failed; with no daemon running the change is refused and the vault is left as it was. The API
  gains `vault.record` (OpenRPC revision 12).

### Changed

- Host placement serves one org. `lucy runner` requires `--org` for every placement, and `runner.lease`
  and `runner.queued` take an `org` (OpenRPC revision 13); a lease without one is refused
  with the new `runner_org` error (-32008). The first run leased on the host sets the host org,
  recorded as `runner.host_org_set`; a host lease for another org is refused with the new
  `host_org` error (-32009) and that runner exits, since same-uid processes can reach each other's
  sockets. The owner changes it with `lucy runner host-org <org>` (`runner.host_org`, admin),
  refused while any host run is live.
- Container placement is refused until it can be proven: a placement a runner declares is never
  trusted. `runner.lease` takes a `placement_proof`, which the daemon checks with a placement
  verifier; none exists until berth can issue a proof (berth#216), so every container lease is
  refused with the new `placement_proof` error (-32010), and `lucy runner --placement container`
  refuses before it dials the daemon.
- `lucy runner --id` defaults to an id unique to the process (the org, the host name and the
  pid) instead of `local`, and each org's runs get their own runner root by default
  (`$XDG_DATA_HOME/lucy/runner/<org>`).
