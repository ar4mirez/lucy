---
id: changes
title: Changes
status: active
version: 12
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- Capability tokens: when a runner leases a run, the daemon issues it a token for its org's
  gateway, scoped to the run under that lease, the org's routes and five minutes, and keeps only
  the token's digest. Each heartbeat renews it with a fresh token and revokes the one before, never
  past four hours from the lease's start; finishing, stopping or losing the lease revokes it. The
  gateway requires the token on every request, never forwards it, and the daemon checks it before
  the gateway reads any credential or looks any upstream up, and again before deciding a credential
  use: a token that is unknown, revoked, expired, for another org or route, or whose run's lease
  has ended is refused and recorded. `lucy capability ls` lists them by id, never a token, and `lucy capability revoke`
  ends one. The API gains `runner.lease`'s, `runner.heartbeat`'s and `gateway.use`'s capability,
  `gateway.verify`, `capabilities.list` and `capability.revoke` (OpenRPC revision 11). No token reaches a Pilot's environment.
- The per-org credential vault: `lucy vault set --org <org> <name>` stores a credential, read from
  stdin only (never an argument, the environment or a terminal), sealed with AES-256-GCM under
  that org's own key and bound to its org and name; `lucy vault ls` lists names and when each was
  set, never a value; `lucy vault rm` deletes one. Keys are kept apart from the sealed credentials,
  and every file stays inside the vault directory. `lucy gateway` now reads its org's credentials
  from the vault, and only its own; a route whose credential is missing is refused before any
  upstream is looked up.
- The per-org credential gateway, the start of m1 slice 4: a worker calls `/<route>/<path>` on its
  org's gateway instead of the service, and the gateway forwards only what the route's allowlist
  names (methods on a path prefix) to an `https://` upstream, with the worker's own credential
  headers removed and the route's credential added. An upstream that resolves to a private,
  loopback, link-local, metadata or other non-public address is refused, and the connection goes
  only to the address that was checked. Each forwarded request is decided by the policy engine and
  recorded before anything is sent; a redirect goes back to the worker and is never followed; a
  credential the upstream echoes back is scrubbed from the answer, which is asked for whole and
  unencoded (an encoded answer is refused); and every request, forwarded or
  refused, is in the audit chain without its credential. Any failure refuses. A test proves a
  Pilot's environment holds none of the runner's credentials.
- `lucy gateway --org <org>` runs an org's credential gateway as its own process. Its routes are an
  ai-sdlc `AdapterBinding` with `interface: SecretStore` and `type: lucy-gateway` in the org's
  namespace, which the daemon reads strictly: a binding that does not read gives the org no routes.
  The gateway listens on a 0600 Unix socket and records every request through the daemon, which
  decides each credential use from its own routes; a gateway cannot add a route or point one
  elsewhere. A path of any length is recorded, with the SHA-256 of the whole path, and a method
  other than GET, HEAD, POST, PUT, PATCH, DELETE or OPTIONS is refused and recorded. The API gains
  the `gateway` scope and `gateway.routes`, `gateway.use`, `gateway.deny` and `gateway.complete`
  (OpenRPC revision 10).
- The org sandbox seam, the start of m1 slice 3: a Sandbox provider runs an org's container, named
  by an ai-sdlc `AdapterBinding` with `interface: Sandbox`, and berth is the first one, reached only
  through its documented API on its socket. Starting, restarting or stopping an org's container
  stops the work in it, so each goes to the policy engine first, as the new `container` action
  kind with the sessions live in that container as facts (policy rule set version 6, OpenRPC
  revision 8). With none it is cleared; with live sessions it holds until the owner confirms
  stopping exactly those sessions, a confirmation lapses when a session starts or ends and is used
  once, and no agent may ask. While a cleared restart or stop runs, no new session is placed in that
  container: a runner in a container does not count that org's runs as waiting, and if it leases
  meanwhile it is refused with the new `sandbox_busy` error and leases again later.
  `runner.queued` takes the runner's placement for this; without one it counts as before. Every
  decision is recorded before the container is touched, and what berth did after it. The daemon
  does not use the seam yet, so nothing acts on a live container.
- `lucy sandbox held` lists the container operations that wait on the owner (the org, the
  operation, the waiting decision and the sessions it would stop), and
  `lucy sandbox confirm <org> <op> <decision>` confirms stopping exactly those sessions; lucy's
  next request for the operation is then cleared, once. A decision a newer request has replaced
  is refused, so the owner confirms only what was shown. The API gains `sandbox.held` (read) and
  `sandbox.confirm` (decide) (OpenRPC revision 9). Until the daemon opens an org's Sandbox
  binding, nothing is held and a confirmation is reported unavailable.
- Idle org containers are stopped: an org's container that is running with no run queued or live
  in it, and no work for 30 minutes, is stopped by the daemon's ordinary guarded `down`, so a
  session that starts in the meantime holds the stop for the owner. Only orgs lucy holds a Sandbox
  binding for are checked, and only a container lucy started itself (lucy's latest successful `up`
  or `down` of it is an `up`) is stopped; one someone else started is left alone, and a failed
  stop is tried again on the next check. Noticing a restart made outside lucy needs berth to
  report the container's start time (a berth follow-up). The daemon does not run it yet.

### Changed

- The secret scan's reviewed exceptions for made-up test credentials are now keyed by their exact
  values in `.gitleaks.toml`, not by line, so they no longer drift when a line moves.
