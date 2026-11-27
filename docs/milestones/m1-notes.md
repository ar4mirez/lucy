---
id: m1-notes
title: "m1: notes per slice"
status: active
version: 6
owner: ar4mirez
requirements: []
---

# m1: notes per slice

What each slice of [m1](m1.md) built and what it still waits on, in the plan's build order. The
slice table, requirement status and exit tests stay in the plan.

- **1, runner protocol.** `internal/runner`: lease, heartbeat (which also streams the run's events
  into the log), finish, and expiry, as typed methods of the one API (`runner.queued`,
  `runner.lease`, `runner.heartbeat`, `runner.finish`, `runs.list`; scope `run`) recorded on
  `run/<org>/<run>` streams and projected into `runner_runs`. The fence is the sequence number of
  the event that granted the lease, so fences only grow; an expired, finished or replaced lease is
  refused (`fenced`). `runner.Runtime` is the Tower's Runtime seam over it; `lucy runner` is the
  local runner with a process executor. The in-container runner is the same binary placed with
  `--placement container`; mounting the socket and per-runner credentials wait for slices 3 and 4.
  Done: the rest of LUCY-RUN-001, the in-container runner reaching the daemon through berth with
  its own credentials, is part of slices 3 and 4.
- **2, Claude Code adapter.** `internal/harness` is the seam (an `Adapter` names a session's
  arguments and environment, checks the pinned version and decodes the protocol);
  `internal/harness/claudecode` drives `claude --print` over stream-json both ways, pinned to one
  version, with no MCP servers, no settings file but lucy's own (no user, project or local source,
  nothing pre-allowed, no hooks of its own, skills and slash commands off) and every permission prompt sent to it.
  lucy appends the run directory's `CLAUDE.md` (or `AGENTS.md`) and its in-directory `@path`
  imports, one level deep and capped at 64 KiB a file and 96 KiB in all, to the system prompt as
  repository text that grants no permission. TODO(LUCY-RUN-006): the run directory stays an empty
  scratch directory until slice 6 checks the repository out there, so nothing is injected yet.
  It streams the Pilot's AskUserQuestion prompt as asking; a prompted file tool (read, edit,
  write) on a path inside the run's directory goes to `runner.permit`, where the policy engine
  decides it with the Pilot as a worker of the lease's org; so do a shell command (Bash, as
  `exec`) and a fetch or search (WebFetch, WebSearch, as `fetch`), which no rule allows yet;
  every other prompt is refused.
  `lucy runner --harness claude-code` runs it. The Tower's answer travels as `run.answered` (a
  digest) and reaches the runner in every heartbeat until the runner acknowledges it; a runner
  whose answer a restart lost asks again. The contract is the recorded transcripts under
  `testdata/<pinned version>`, replayed in CI; `harness-check.yml` checks the newest release each
  night without a secret and reports it ready or held, never failing. The owner sees the Pilots' open questions with
  `lucy questions` and answers with `lucy answer`, over `questions.list` and `questions.answer`,
  which go through the Tower; until slice 7a hosts the Tower in the daemon they list nothing and
  answer nothing. Done: what remains of slice 2 is blocked on D3c, a real model login:
  transcripts captured from real sessions of the pinned version (today's follow the documented
  protocol), and checking hook callbacks against a real session.
- **3, berth sandbox.** `internal/sandbox` is the runtime seam ([sandbox](../tech/sandbox.md)): a
  `Provider` runs org containers, opened from an ai-sdlc `AdapterBinding` with `interface: Sandbox`
  (`sandbox.Providers`), and `internal/sandbox/berth` is the first one, a client of berth's
  documented API v1 on its socket, checked against an excerpt of berth's OpenAPI document and a
  fake berth (`berthtest`); tests and CI never reach a real berth. Every `up`, `restart` or `down`
  goes through `sandbox.Sandbox`, which asks the policy engine with the org container's live
  sessions as facts (the `container` kind, rule set version 6), records the decision before acting,
  and holds while sessions are live until the owner confirms stopping exactly those sessions; a
  confirmation lapses when they change and is spent once used. A cleared `restart` or `down` holds
  the container in the runner broker until berth is done, so no session is placed in it meanwhile;
  the hold is rebuilt from the log and expires on its own. The owner lists what waits with
  `lucy sandbox held` (`sandbox.held`) and confirms one decision with `lucy sandbox confirm`
  (`sandbox.confirm`); a decision a newer request replaced is refused. An org's running container with
  no work for 30 minutes is stopped by the same guarded `down` (`Sandbox.StopIdle`), and only for
  the orgs lucy holds a binding for, and only when lucy's latest successful `up` or `down` of it
  was an `up` (a failed stop is retried on the next check): a container someone else started is
  left alone (seeing an outside restart of one lucy started needs berth to report the container's
  start time, a berth follow-up). Slice 3 is blocked on berth#216, filed for berth: an API to start a supervised process
  in an org's container, mount lucy's socket there, pin lucy's version in the image and report when
  a container started. What remains once it lands: the daemon opens the org's binding, runs the idle
  stop and places the in-container runner through the seam, and the berth provider is checked
  against a real berth on a test host (TODO(LUCY-SBX-001)); until then its contract is proven
  against the fake berth only. Slice 3's remaining work resumes when berth#216 lands; slices
  continue in order meanwhile.
- **4a, credential gateway.** `internal/gateway` ([gateway](../tech/gateway.md)): one gateway per
  org, outside its container. A worker calls `/<route>/<path>`; the route's allowlist (methods on a
  segment-bounded path prefix) decides what is forwarded, to an `https://` upstream only. The name
  is resolved once and refused if any address is private, loopback, link-local, metadata, shared,
  reserved or multicast; the connection goes only to the checked address. The worker's own
  credential headers are removed, the route's credential is added, the use is decided by the
  policy engine and recorded before anything is sent, a redirect goes back to the worker, an
  echoed credential is scrubbed, and every request, forwarded or denied, is in the audit chain; any
  failure refuses. A test proves a Pilot's environment carries none of the runner's credentials.
  An org's routes are a `SecretStore` AdapterBinding of type `lucy-gateway`, which the daemon
  reads strictly (a binding that does not read gives the org no routes); `lucy gateway --org`
  serves them on a 0600 socket and records every request through the daemon (`gateway.routes`,
  `gateway.use`, `gateway.deny`, `gateway.complete`, scope `gateway`), which decides from its own
  routes. Left in 4a: the socket reachable only from the org's container (berth#216) and the
  worker's tools pointed at it; the vault and capability tokens are 4b.
- **4b, vault and capability tokens.** `internal/vault` ([vault](../tech/vault.md)): each org's
  credentials sealed with AES-256-GCM under the org's own 32-byte key, bound to the org and name,
  keys kept apart from the sealed files, every file operation confined to the vault by `os.Root`.
  `lucy vault set|ls|rm` manages it (a value only from stdin, never a terminal), and `lucy gateway`
  reads its own org's credentials from it, before it looks any upstream up. Each org key is
  wrapped under a key derived from the vault passphrase ([D14](../decisions/D14-vault-passphrase-kek.md),
  PBKDF2-HMAC-SHA256, `Vault.Unlock`), which `lucy vault set` and `lucy gateway` read from
  `--passphrase-file` or the terminal; a vault from before
  wrapping is migrated on its first unlock. Capability tokens ([capabilities](../tech/capability.md)): the daemon issues each
  leased run a token for its org's routes, bound to the lease and at most four hours, keeping only
  its digest; the gateway requires it and the daemon checks it before deciding, and the owner
  lists and revokes them with `lucy capability`. Every `lucy vault set` and `rm` is recorded in
  the audit chain through the daemon, before and after the change, by name only; with no daemon
  it refuses.
- **What remains of slice 4.** Each item and what it waits on:
  1. Built, as decided in [D14](../decisions/D14-vault-passphrase-kek.md): the org keys wrapped
     under a passphrase-derived key (#32). Left: changing the passphrase.
  2. Built, as decided in [D13](../decisions/D13-run-gateway-socket.md): a run reaches its org's
     gateway through its own gateway socket, which the runner fronts and which adds the run's
     token, so the runner never puts the token in the Pilot's process; the host serves one org.
     A host Pilot still shares the daemon's uid and API authority for its own org: [#36](https://github.com/ar4mirez/lucy/issues/36). Left: pointing the worker's tools at it (slice 6 and berth#216).
  3. The gateway's socket reachable only from the org's container, and the worker's tools pointed
     at it: blocked on berth#216.
  4. Health checks and restart of the gateway process (LUCY-CRED-004): with `lucy preflight`'s
     service setup (slice 15).
  5. `lucy preflight --check` scanning containers' environments and mounts for tokens
     (LUCY-CRED-005): slice 15.
  6. Capability actions narrower than the org's routes, per task: slice 7a.
- **5, session ownership.** A run's lease outlives the daemon. A runner keeps its work going while
  the daemon is down and retries; a restarted daemon, before it expires anything, moves every lease
  still live when it stopped (the newest event in the log) later by the outage, at least
  `runner.DefaultTTL` from now, recorded as `run.resumed` (`Broker.Resume`), so the runner renews
  under its old fence and the Tower sees the same Pilot rather than a lost one; a restart with no
  outage moves nothing. The move keeps the run, not the runner's authority: a capability token is
  honoured only up to the expiry the runner itself last earned (`RunInfo.Earned`), and a moved
  lease is not moved again before its runner renews it. Queuing a newer Pilot generation fences
  the flight's live run in the same transaction (`run.replaced`), and a stale generation is
  refused. `sim.World` flies Pilots through the runner protocol when `Options.Runners` is set, with
  faults for a daemon outage (`Down`) and a runner cut off (`Cut`); the corpus uses it for
  sessions-03 and sessions-04. A runner cut off from the daemon keeps its Pilot running until it
  reconnects and is fenced; the Pilot's capability token dies with the lease, so it cannot act
  through the gateway meanwhile. A runner drains on SIGTERM or `lucy runner drain` (`Runner.Drain`):
  it leases nothing more and exits once its runs end, so a planned restart or upgrade loses no
  work; runners register in `runners/` in lucy's runtime directory, a locked file each, so the
  drain signals only live ones. An unplanned crash still loses the runner's runs, each replaced
  once; a per-run keeper process is a later option, to weigh with #36's per-run OS boundary, as
  both change how a run's process is owned. An in-place upgrade ([upgrade runbook](../runbooks/upgrade.md)) restarts the
  daemon first, then drains and restarts the runners (drain applies to runners from this release
  on; a 0.6.0 runner is left to finish its runs, then replaced by one started with `--org`);
  `TestUpgradeFromV060` checks it against a
  store v0.6.0's own code wrote with a run leased (`internal/daemon/testdata/upgrade/v0.6.0.sql`).
  Slice 5 is done.
- **7.** m0 already validates QualityGate and AutonomyPolicy against the vendored schemas on load;
  the owner is the only override role.
- **13.** Hard prerequisite: [#36](https://github.com/ar4mirez/lucy/issues/36), runs with an OS boundary of their own. No Pilot runs real
  work on the host before it lands, since a host Pilot shares the daemon's uid and API authority.
  Shadow mode (slice 10) runs no Pilot and is unaffected.
- **14.** m0 ships only the unix socket, so this adds the HTTPS transport for the one API: the same
  `/rpc/v1` handler, loopback and tailnet binds, TLS, and paired-device credentials checked per call.
- **16.** Adds offline export and verification of the audit log (m0 verifies it only through the
  daemon). Each slice turns the corpus scenarios pending on it executable
  ([coverage](../security/conformance.md)).
