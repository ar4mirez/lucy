---
id: changes
title: Changes
status: active
version: 19
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- The vault's org keys are wrapped at rest under a key derived from a vault passphrase
  (PBKDF2-HMAC-SHA256, 600,000 iterations, a random salt), so a copy of the whole vault opens
  nothing without it (decision D14). `lucy vault set` and `lucy gateway` take `--passphrase-file`
  (a file only its owner can read, outside the vault and lucy's data directory), else use the
  systemd credential `lucy-vault`, else ask once on the terminal with echo off; never an argument
  or the environment. The first `set` on a new vault, or one from before keys were wrapped, sets
  the passphrase and wraps every key; after that a plain key is refused. A wrong passphrase is
  recorded as a failed vault change with the new reason "the vault is locked or its passphrase is
  wrong".

- A runner drains on SIGTERM: it leases nothing more, lets each run it holds carry on to its end,
  then exits 0, so a planned restart or upgrade of a runner loses no work. `lucy runner drain`
  sends that to every live runner on this machine (`--org` or `--id` to pick, `--no-wait` to
  return at once) and waits until each has exited. Runners register while they run in `runners/`
  in lucy's runtime directory, one locked file each, so only a live runner is ever signalled, and
  a second runner with the same `--id` is refused. Ctrl-C, a second SIGTERM or SIGHUP still stops
  a runner at once, reporting its runs lost for the Tower to replace once.

- A daemon restart no longer loses runs whose runners kept working while it was down. Before it
  expires anything, a restarted daemon moves every lease still live when it stopped (the time of
  the newest event in the log) later by the outage, leaving it at least one default lease life,
  recorded as `run.resumed`, so each runner reattaches under its old fence and its flight keeps its
  Pilot; a runner that is really gone is expired once its moved lease ends. A lease that had run
  out before the daemon stopped is not moved, and a restart with no outage moves nothing. A
  capability token is not honoured past the lease its runner last renewed, and a moved lease is
  not moved again by another restart until its runner renews it.
- A newer Pilot generation of a flight fences the flight's run that is still queued or leased in
  the same transaction that queues it, recorded as `run.replaced`, so a flight never has two live
  runs and the old runner is refused from then on; a generation that is not newer than the
  flight's newest is refused.
- Conformance scenarios sessions-03 (a restarted daemon reattaches to a live Pilot session) and
  sessions-04 (a session is fenced before it is replaced) are executable: the corpus can fly its
  Pilots through the runner protocol with runners of their own, and checks before every step that
  no flight has two live runs.

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
