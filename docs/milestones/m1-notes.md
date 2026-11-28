---
id: m1-notes
title: "m1: notes per slice"
status: active
version: 36
owner: ar4mirez
requirements: []
---

# m1: notes per slice

What each slice of [m1](m1.md) built and what it still waits on, in the plan's build order. The
slice table, requirement status and exit tests stay in the plan.

- **1 to 5.** Runner protocol, the Claude Code adapter, the berth sandbox, the credential gateway,
  the vault and capability tokens, and session ownership: see [notes for slices 1 to 5](m1-notes-1.md).
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
- **7a, LUCY-SPEC-017 in full.** lucy passes the spec's Core conformance level. The suite's Core fixtures (every `pipeline/valid-*` and `quality-gate/valid-*`, and every `invalid-*`) are vendored at the pin and listed in PROVENANCE; `TestCoreConformance` loads each: a valid one is admitted once written as an org's config (in a namespace, without `status`), an invalid one is refused on the schema, and as shipped a valid one is refused only under lucy's two documented rules (`MissingNamespace`, LUCY-SPEC-002; `StatusInConfig`, LUCY-SPEC-003). The audit-chain fixture passes (LUCY-SPEC-009). The worktree pool and the backlog use the spec's schemas, vendored at the pin: an org declares its pool with a `WorktreePool` resource, accepted with only `layout: workspace-scoped` and `cleanup.onMerge: true` (members the loader fills with their default are accepted at it); `rootDir` and every other field are refused by name, and an org whose WorktreePool lucy cannot accept (refused by the schema or by lucy, or two of them) has its Flights held as `WorktreePoolInvalid`. `flights.list` and `lucy flights --json` write each Flight as a `BacklogTask` checked against the vendored schema. Reading backlog markdown as a source of Flights is not part of m1
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
- **7b, QualityGate overrides (LUCY-SPEC-010).** A QualityGate is accepted as the spec writes
  it, so Core conformance holds, but lucy lets only the owner override a gate: a gate's
  `override.requiredRole`, or a visual rule's `override.approvers`, naming anyone else gets the
  condition `Override` (reason `OverrideIsOwner`) in `lucy config status` and a warning in the
  daemon's log at start and on each applied reload. `requiresJustification` is kept as stated.
  The validation slice (11) takes an override only as the owner's recorded word.
- **7.** m0 already validates QualityGate and AutonomyPolicy against the vendored schemas on load;
  the owner is the only override role.
- **8, account state (LUCY-RUN-008).** `harness.Converger`: the runner opens the org's synthetic
  home as an `os.Root` and lets the adapter re-assert its account state there before each spawn;
  Claude Code's writes `.claude.json` and `.claude/settings.json` only, keeping every key it does
  not own, refuses a symlinked file or directory, and is idempotent. A run whose harness needs a human is held, not
  wedged: `harness.Prerequisite` (Claude Code's checks only that a credentials file or an
  `oauthAccount` is there, never what it holds) makes the runner finish the run `needs_owner` with a
  known need instead of spawning; the run's reason is that need, never runner text; the Tower
  records `task.pilot_exited` (needs_owner) and its own `task.pilot_asked` (with `need`), so the
  question reaches `lucy questions` and `lucy decisions`, and the owner's answer moves the flight
  back to dispatched for the next generation. Skills (LUCY-RUN-009): `internal/skills` embeds
  one skill per Pilot role and `builtin/MANIFEST`, each file's SHA-256 (a test recomputes them);
  `skills.Install` writes them into the adapter's `SkillsDir` (`.claude/skills`) in the org's home
  through its Root, reads each back against the manifest, refuses a linked directory, and removes
  every other entry through the runner's path guard. A session still runs with skills off
  (`--disable-slash-commands`): a skill's `allowed-tools` would skip the prompt, and turning skills
  on may load a repository's own; that waits on a live contract test (TODO(LUCY-RUN-009)). The
  role's instructions reach the session instead in lucy's appended system prompt, before the
  repository's instructions: `skills.Instructions` gives the role's skill body, hash-verified, as
  `harness.Session.Role`. Every run is a ship worker's so far; the lease names the role once
  research and review runs fly.
- **9, cost (LUCY-RUN-011).** Per step: Claude Code's adapter turns each assistant turn's `usage`
  into a `usage` event (input, output, cache read and cache written tokens), streamed like any run
  event (`runner.heartbeat`, `run.streamed`), and summed on the run. Per task: the session's
  `total_cost_usd` rides on its exit and `runner.finish` as `cost_micros`, millionths of a dollar,
  bounded and untrusted like all runner reports; `flights.list` sums a flight's runs over every
  generation, and `lucy flights` shows them. Supervision (LUCY-RUN-010): `tower.Supervisor` is asked once
  about each new Pilot question, in the step that records it; its proposal is recorded
  (`task.answer_proposed`, naming the supervisor) and shown in `lucy questions` and
  `lucy decisions` as a suggestion. It never reaches the Pilot and never closes the question, under
  any posture (corpus rule channel-05, now executable); the owner answers, or accepts the
  suggestion with `lucy answer <flight> --accept-proposal`, recorded as the owner's answer. The
  Tower's own questions are never put to it; with none configured nothing changes. Delegating
  unattended answers is an m2 open question. Next: the model-backed supervisor (small model, then
  large, then the owner), through the org's gateway, against a fake model server, off by default,
  and per-org daily budgets.
- **13.** Hard prerequisite: [#36](https://github.com/ar4mirez/lucy/issues/36), runs with an OS boundary of their own. No Pilot runs real
  work on the host before it lands, since a host Pilot shares the daemon's uid and API authority.
  Shadow mode (slice 10) runs no Pilot and is unaffected.
- **14.** m0 ships only the unix socket, so this adds the HTTPS transport for the one API: the same
  `/rpc/v1` handler, loopback and tailnet binds, TLS, and paired-device credentials checked per call.
- **16.** Adds offline export and verification of the audit log (m0 verifies it only through the
  daemon). Each slice turns the corpus scenarios pending on it executable
  ([coverage](../security/conformance.md)).
