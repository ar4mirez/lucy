---
id: m1-notes
title: "m1: notes per slice"
status: active
version: 28
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
  which go through the Tower the daemon hosts (slice 7a). Done: what remains of slice 2 is blocked on D3c, a real model login:
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
- **6, worktrees and the run's environment.** Each run's scratch directory (its working directory
  and TMPDIR, deleted through the path guard) and the org's synthetic HOME, `home/` under the
  runner's root, 0700, kept and shared by the org's runs and never the user's home, so a Pilot
  reads nothing of it. The root is bound to its org on first use (`runner.ClaimRoot`, `.org` in it,
  made exclusively) and refused, with `runner.ErrRootOrg`, to a runner of any other org, both by
  `lucy runner` before it dials and by the executor before it makes a HOME or a run directory, so
  the HOME is per org by construction; the scratch delete removes only a direct `run-*`
  child of the root, never the home. A harness's model login has to be set up in the org home
  (D3c). The worktree pool ([worktree](../tech/worktree.md), `internal/worktree`) has its core:
  slots of a bare clone per org and repository, leased in the store, reset on acquire and freed
  only once the work landed, including a squash merge the forge reports or a pushed branch. A host
  run of a project whose Runway names a repository has its slot reserved with its lease
  (`runner.lease`'s `worktree` and `worktree_state`, OpenRPC revision 14) and prepared in the
  background, outside the call's deadline; the runner starts it once a heartbeat reports the slot
  ready, and the harness reads the checkout's instructions. A finished run's unlanded slot is
  quarantined, not counted against the pool's size; `lucy worktree ls` shows it and the owner's
  `lucy worktree discard` removes it, recorded first, through the path guard. A container run gets
  no slot until it can be mounted (berth#216). Left: the Tower giving a slot back at takeoff (slice
  7a) and private repositories (credentials through the gateway). The path guard's delete executor
  (`Guard.RemoveAll`, through an `os.Root`) takes both recursive deletes; the inventory of deletes
  is in SECURITY.md. Slice 6 is done but for those two.
- **7a, task lifecycle.** Readiness (LUCY-SPEC-012): a `Flight` resource states `goal`, `runway`,
  `delivery` (pull-request for a ship Flight, report for a research one), `autonomy` (owner, green
  or client; never assumed) and `acceptance`; each load gives every admitted Flight a `Ready`
  condition (`spec.FlightReadiness`), True only when it has all of them and its runway is an admitted
  Runway of its namespace, else False naming what it lacks; a complete research Flight is False with
  reason Unsupported, since lucy does not fly research Flights yet. The Tower refuses to dispatch a flight
  that is not ready (`tower.Readiness`, `ErrNotReady`) and records nothing; it no longer assumes the
  owner posture. The daemon hosts the Tower: it steps its flights on its poll interval and serves its
  Pilots' questions through `lucy questions` and `lucy answer`; only started with `--dispatch` does
  it dispatch the Ready Flights it loads (by name, once), each recorded with a `Dispatched`
  condition (InFlight for one it flies from its log; DispatchDisabled, TakeoverGate, NameInUse, Finished, PipelineInvalid, DispatchFailed when not). A Runway
  whose remote is not a local fixture is refused, at dispatch and at every lease, until #36 and the owner's go/no-go, and the gateway
  refuses every git push; its forge is a stub that merges nothing until the validation slice. Decisions (LUCY-SPEC-013):
  `internal/decision` projects every decision waiting on the owner from the log (the
  `decision_catalog` projection) into the spec's Decision resource, vendored with the regexp shim
  the schema needs; `decisions.list` and `lucy decisions` list them, `flights.approve` and
  `lucy approve` give a holding merge the owner's word. Roles (LUCY-SPEC-015): `internal/role` bundles one AgentRole per role (builtin/, namespace
  `lucy`); an org's AgentRole of a role's name adds its blockedPaths, and the daemon gives them,
  with the run's checkout, to `runner.permit` as facts for the policy engine's `role.blocked-paths`
  rule. Pipelines (LUCY-SPEC-014): `internal/pipeline` bundles the ship and research Pipelines
  (builtin/, namespace `lucy`), reads an org's replacement, and the daemon dispatches each Flight
  with what the Tower takes from its shape's Pipeline (`tower.Pipeline` in `task.dispatched`).
  Parse is an allowlist: `triggers` and `providers` (required by the format, not acted on yet),
  and the stage fields lucy flies; every other field, and an escalating approval timeout, is
  refused with `pipeline.ErrUnsupported` naming its path. An org Pipeline lucy cannot fly (loader
  or Parse refusal) is held in the catalog as its error: the daemon starts and holds that org's
  Flights of its shape as `PipelineInvalid`, never on the built-in one. Left in 7a: wiring those (LUCY-SPEC-014), the
  roles' other constraints and tools and their session instructions (LUCY-SPEC-015). The worktree pool and backlog
  (LUCY-SPEC-017): `worktree.ParsePool` and `PoolErrors` check each org's `WorktreePool`, the dispatch holds an org's
  Flights as `WorktreePoolInvalid` when it cannot be accepted, and `internal/backlog` writes a Flight as a `BacklogTask`
  for `lucy flights --json`. Core conformance (LUCY-SPEC-017): the suite's Core fixtures are vendored under
  `internal/spec/testdata/conformance` and `TestCoreConformance` runs them through the loader.
- **7b.** Converge loops (LUCY-SPEC-008): `converge.Run` runs a `Reconciler` at start, when the
  store's `Changed` channel closes (a commit that appended), at the time it asked for, and at least
  every resync; never twice within its minimum interval, and after a failure only once a doubling
  backoff has passed, which events do not cut short. The hosted Tower and the lease expiry are its
  first loops; the Tower is woken by a heartbeat's commit instead of waiting out its poll. A
  flight's failed step backs off that flight alone, on the Tower's clock and in memory (a restart
  retries every flight at once), shown as its status's step error and retry time; only a failure
  outside a flight's step backs off the whole Tower. Lease expiry and token revocation run in the
  same pass whichever fails, and back off at most `ExpiryInterval`. A Pilot start that fails (an
  outage) is recorded (`task.pilot_start_failed`) and retried on a backoff computed from the
  recorded times, so a restart keeps that schedule and the corpus's restart-every-step replay stays
  identical; only a start the runtime refuses for good (`tower.ErrStartRefused`: an invalid task, a
  run id another flight holds, a stale generation) grounds the flight (retry-05).
  Postures from AutonomyPolicy follow, as decided: a Runway names a policy and a level, and only
  `guardrails.requireApproval` maps (`all` to owner, `security-critical-only` to green).
- **7a, LUCY-SPEC-014 in full.** lucy bundles an ai-sdlc Pipeline for each Flight shape (`ship`, `research`), and an org replaces one with a Pipeline of that name in its namespace. Parse accepts only these fields: `triggers` and `providers` (required by the format, not acted on yet), and `stages`, each with `name` (pilot, and for ship merge), `agent` (pilot only), `onFailure` (pilot only: `strategy` abort or retry, `maxRetries` for retry), and `approval` (merge only: `required`, `blocking` true, `timeout` with `onTimeout` abort). The members the spec loader fills with their schema default are accepted only at that default. Every other field is refused as not supported yet, naming its path. The pilot stage's FailurePolicy sets how many lost Pilots the Tower replaces; the merge stage's ApprovalPolicy can require the owner for every merge whatever the posture (never waive one) and bound a held merge, grounding the flight on the timeout; an escalating timeout, or one with no onTimeout, is refused, since the Tower acts only on abort; onTimeout auto-approve is refused at load, on any stage, so a timeout never approves a merge. An org Pipeline lucy cannot fly (rejected by the spec loader, or refused by Parse) never falls back to the built-in one: the daemon starts, warns, and holds that org's Flights of that shape with Dispatched False / PipelineInvalid, naming the Pipeline and why; other orgs are unaffected, and only a broken built-in Pipeline stops the daemon. TODO(LUCY-SPEC-014): wire the refused fields as lucy comes to act on them (a stage's credentials through the gateway, the PullRequest config to the Pilot, retryDelay in the Tower); policies move to the spec's Workflow document once it is vendored
- **7b, postures (LUCY-POL-005, LUCY-SPEC-010).** A Runway names an AutonomyPolicy in its namespace
  and a level (`autonomyPolicy`, `autonomyLevel`, both or neither); `internal/autonomy` reads only
  that level's `guardrails.requireApproval`: `all` is the owner posture, `security-critical-only`
  the green posture (the fixed list still needs the owner); `none` and `architecture-changes-only`
  are refused. The fields the format requires (`levels`, `promotionCriteria`, `demotionTriggers`;
  per level `level`, `name`, `permissions`, `guardrails`, `monitoring`) and a level's
  `description` are accepted and not acted on; every other field of a policy a Runway names is
  refused by its path (TODO(LUCY-SPEC-018), m2). `clientConfirms: true` on a Runway is the client
  posture, on top of the owner's word. A Flight needs no `autonomy` when its Runway sets one, and
  its own may only tighten it (green, owner, client, in that order); one that would loosen it, or
  whose Runway names a policy lucy cannot fly, is held with `Dispatched` reason `AutonomyInvalid`.
  The dispatch records the effective posture. Promotion and demotion are the owner's, by hand.
  At each start, and on each reload (`lucy config reload`, SIGHUP), the daemon tightens every flight in flight whose config now asks for a stricter
  posture (its Runway demoted, its Flight's autonomy tightened), and moves a flight whose posture
  can no longer be resolved from the config to owner (a client flight stays client): its Runway is
  removed or refused, its Runway's AutonomyPolicy can no longer be read, or neither its Runway nor
  its Flight states a posture any more. When the Runway states none but the Flight does, the
  Flight's own applies, tightening only. Each is recorded as `task.posture_tightened` (from, to,
  why), and the flight's next merge is decided again. A promotion is never applied in flight: it
  holds for flights dispatched after it.
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
