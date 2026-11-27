---
id: D13
title: "D13: a run reaches its org's gateway through a socket its runner fronts"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-CRED-003, LUCY-CRED-005]
decided: 2026-11-27
---

# D13: a run reaches its org's gateway through a socket its runner fronts

## Context

Each leased run gets a capability token for its org's credential gateway
([capabilities](../tech/capability.md), LUCY-CRED-003). The gateway refuses any request without
one. The run's Pilot and the tools it starts have to reach the gateway somehow, but no credential
may ever be in the worker ([D3c](D3c-credential-free-workers.md), LUCY-CRED-005), and the token is
the closest thing to one a run holds. m1 slice 4 left the choice of how the token reaches the
Pilot's tools to the owner, because every option puts the token's authority within the Pilot's
reach.

Options considered:

1. **A git credential helper, or a token file in the run's directory.** The runner writes the
   token where the tools read it. The token is then a file the Pilot can read, copy or print into
   its output. It also has to be rewritten on every renewal.
2. **An environment variable.** The runner puts the token in the run's environment. Every process
   the Pilot starts inherits it, and it can end up in output, crash dumps and child processes'
   logs.
3. **A per-run socket the runner fronts.** The runner opens a Unix socket in the run's scratch
   directory, names it in `LUCY_RUN_GATEWAY`, and forwards each request on it to the org's gateway
   socket. On the way it drops any capability header the run sent and adds the run's current
   token. The token stays in the runner's memory.

## Decision

A run reaches its org's gateway only through a per-run socket its runner fronts (option 3).
The token never enters the Pilot's process, environment or files. Renewal stays inside the runner:
each heartbeat's new token replaces the old one in the forwarder. The socket closes when the run
ends.

The socket keeps other users out with file modes alone: the run's directory is 0700 and the socket
is 0600. Isolation between orgs therefore holds across containers or uids, not within one: every
process of the runner's uid can open every run socket of that uid. Until runs are in their org's
container (berth#216), the daemon serves one host org. The first run leased to a runner on the host
sets it, recorded as `runner.host_org_set`; a host lease for any other org is refused with
`host_org`, and other orgs need container placement. A runner on the host names its org
(`lucy runner --org`; `runner_org` without one). Only the owner changes the host org
(`lucy runner host-org <org>`, the `runner.host_org` method, recorded), and not while any host run
is live, so no two orgs' runs are on the host at once. A runner in an org's container leases as
before, because the container already belongs to one org.

## Consequences

- The token cannot be read, copied or printed by the run, and a call that reaches the org's
  gateway any other way carries no token and is refused.
- **Residual risk:** any process of the run can act with the run's token for as long as the run
  lives, through its socket, within the run's org, the org's routes and the lease. That is the
  authority the token grants anyway; the socket only keeps the token itself out of reach. A
  compromised Pilot can make requests the policy engine allows for its org, and every one is
  decided and recorded, but it cannot carry the token out of the run.
- A Unix socket path is at most 107 bytes on Linux and 103 on macOS, so the runner refuses to start
  under a root too long for a run's socket.
- Still to build: pointing the worker's tools (git, the forge CLI, the harness) at the socket
  (slice 6), and making the gateway reachable from inside the org's container (berth#216).
