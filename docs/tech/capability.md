---
id: tech-capability
title: Capability tokens
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-CRED-003]
---

# Capability tokens

What a run presents to its org's credential gateway to use a route (TECH.md section 6). The code
is `internal/capability`; the gateway checks tokens through the daemon
([gateway](gateway.md)); the owner lists and revokes them with `lucy capability`.

- **Issue.** When a runner leases a run (`runner.lease`), the daemon issues a token for it and
  returns it in the lease, to that runner only: `lcap_` and 32 random bytes, base64url. It is
  scoped to (org, run, actions, expiry): the run's org, the run and the lease's fencing token, the
  org's gateway routes, and an expiry five minutes after issue. The daemon keeps
  only its SHA-256: the `capability.issued` event and the `capabilities` projection hold the
  digest, never the token, and the token's id is the digest's first 16 hex digits. If a token
  cannot be issued, the lease is not handed out.
- **Renew.** Each successful `runner.heartbeat` of a live lease returns a fresh token for the same
  run, fence, org and routes, and revokes the ones before it (reason `rotated`) in the same
  transaction; the runner keeps only the newest. A renewed token expires five minutes on, but
  never past four hours from the lease's first token: after that there is no renewal and the run
  loses gateway access. A token the owner revoked is not renewed, and a heartbeat that says stop
  renews nothing and revokes the run's tokens (`stopped`). Finishing a run revokes them
  (`finished`); a lease that runs out gets no heartbeat, is refused at once, and the daemon's
  lease-expiry timer revokes its tokens (`run ended`).
- **Check.** Each request to the gateway carries the token in `X-Lucy-Capability`; a request
  without one is refused before any credential is read, and the header is never forwarded. The
  daemon checks the token in `gateway.verify` before the gateway reads the credential or looks the
  upstream up, and again in `gateway.use`, before the policy decision: it must be one it issued
  (compared on the digest in constant time), not revoked, for this org and for this route, before
  its expiry, and its run must still be leased under the same fence with that lease not run out.
  So a token dies with its run, and a token from a replaced or expired lease is refused, even
  before the daemon records the expiry. A refused token is recorded as `gateway.denied` with its
  id and run and the reason (unknown, revoked, expired, out of scope, run ended); a cleared request
  records the run and capability id with its decision, and the run is an input of the decision.
- **Revoke.** The owner revokes one by its id (`capability.revoke`, `lucy capability revoke`),
  recorded as `capability.revoked`; the gateway refuses it from then on. `capabilities.list` and
  `lucy capability ls` show ids, org, run, routes and times, never a token.
- **Where it goes.** The runner keeps the newest token in memory (`runner.Lease.Capability`,
  replaced by each heartbeat's, never serialized or logged). A run reaches its org's gateway
  through its own gateway socket, `.lucy-gateway.sock` in its 0700 scratch directory (0600), named
  in `LUCY_RUN_GATEWAY`: the runner forwards each request on it to the org's gateway socket
  (`lucy runner --gateway-dir`, the runtime directory's `gateway/` by default), drops any
  capability or forwarding header the run sent, and adds the run's current token, renewed after
  each heartbeat. So the token never enters the Pilot's process: its environment and the files in
  its directory hold none, which runner tests check, and a call that reaches the org's gateway any
  other way carries no token and is refused. The socket closes when the run ends.

## Not built yet

- TODO(LUCY-CRED-003): pointing the worker's tools (git, the forge CLI, the harness) at the run's
  gateway socket, once the run's directory is the repository's worktree (slice 6) and the gateway
  can serve from inside the container (berth#216).
- TODO(LUCY-CRED-003): actions narrower than the org's routes, per task, once tasks declare what
  they need (slice 7a).
