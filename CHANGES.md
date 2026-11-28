---
id: changes
title: Changes
status: active
version: 37
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- An org declares its worktree pool with the spec's `WorktreePool` resource. lucy accepts only
  what it acts on, `layout: workspace-scoped` and `cleanup.onMerge: true`, and refuses `rootDir` and
  every other field by name: the pool stays under lucy's guarded data directory. An org whose
  WorktreePool lucy cannot accept, or that declares two, has its Flights held with reason
  `WorktreePoolInvalid`. `lucy flights` lists every loaded Flight with its readiness and the Tower's
  state; `lucy flights --json` writes each as the spec's `BacklogTask` document, checked against the
  vendored schema. The API gains `flights.list` (OpenRPC revision 18).

- lucy passes the ai-sdlc spec's Core conformance level: every Core fixture of the spec's own
  conformance suite, vendored at the pinned commit, is accepted or rejected as the suite requires.
  lucy refuses a spec-valid document only under its two own rules: a resource must name its org in
  `metadata.namespace`, and a config file never carries `status`.

- Flights are flown on ai-sdlc Pipeline resources: lucy bundles a `ship` and a `research`
  Pipeline, and an org replaces one with a `Pipeline` of that name in its namespace. The pilot
  stage's `onFailure` (`abort`, or `retry` with `maxRetries`) sets how many lost Pilots are
  replaced; the merge stage's `approval` can require your approval for every merge, whatever the
  autonomy posture, and bound how long a merge holds (`timeout` with `onTimeout: abort` grounds
  the flight). A timeout never approves a merge: a Pipeline with `onTimeout: auto-approve` is
  refused. An org Pipeline lucy cannot fly (one the spec loader rejects, or one with a refused
  field) never falls back to the built-in one: the daemon starts, logs a warning, and holds that
  org's Flights of that shape with reason `PipelineInvalid`, naming the Pipeline and why. Only
  these fields are accepted: `triggers` and `providers` (required by the format, not acted on
  yet), and per stage `name`, `agent`, `onFailure` (`strategy`, `maxRetries`) and `approval`
  (`required`, `blocking`, `timeout`, `onTimeout`). Every other field, and an escalating timeout,
  is refused as not supported yet, naming its path, rather than ignored.

- Roles are ai-sdlc AgentRole resources: lucy bundles one for each of its roles (supervisor,
  worker, researcher, reviewer), and an org adds to one with an `AgentRole` of that name in its
  namespace. A role's `constraints.blockedPaths` globs are enforced: the policy engine (rule set
  version 7, rule `role.blocked-paths`) denies a Pilot's file-write and delete tools on a blocked
  path, relative to its checkout and ignoring case, whatever else would allow it. A Pilot's shell
  commands go to the engine as exec requests, which it refuses by default. An org AgentRole with a
  blocked path the engine cannot read stops the daemon from starting, naming the resource. The
  path guard's delete executor can carry a role's blocked paths, and then refuses, deleting
  nothing, a target that matches one or that holds an entry that does. The API's `runner.permit`
  accepts the `delete` kind (OpenRPC revision 17).

- Decisions waiting on you are a catalog: a Pilot's question, a merge holding for your approval and
  a container operation holding for your confirmation are each projected from the event log into
  the spec's Decision resource (RFC-0035). One is open exactly while the Tower or the sandbox waits
  on you (a merge whose retries failed holds too): your recorded answer, approval or confirmation
  answers it, and new facts that end the wait supersede it; nothing expires it and no agent can
  settle it. `lucy decisions` lists them
  (`--all` for the closed ones, `--json` for the spec resources), and the new `lucy approve
  <flight>` approves a merge a flight holds for. The API gains `decisions.list` and
  `flights.approve`.

- The daemon hosts the Tower: `lucy questions` lists its Pilots' open questions and `lucy answer`
  relays the owner's answer. It dispatches nothing unless started with `lucy daemon --dispatch`;
  then it dispatches every Flight in the config directory whose `Ready` condition is True, once, by
  the Flight's name, and starts its Pilot through the runner protocol. Each Ready Flight carries a
  `Dispatched` condition (`lucy config status`): True (`Dispatched`, or `InFlight` for a flight the
  Tower flies from its log, with or without `--dispatch`), or False with reason `DispatchDisabled`,
  `TakeoverGate` (its Runway names a repository whose remote is not a local fixture under lucy's
  data directory or the system temp directory: real repositories are refused until #36 and the
  owner's go/no-go), `NameInUse` (a flight of another org or runway has its name), `Finished` (a
  finished flight has its name), `PipelineInvalid` (the org's Pipeline for the Flight's shape is
  invalid), `WorktreePoolInvalid` (the org's WorktreePool cannot be accepted) or `DispatchFailed`. The Tower's forge is a stub that reads no pull
  request and merges nothing until the validation slice. The gate also holds at every lease: a run
  whose Runway names a real remote gets no checkout, only the gate as its worktree error.
- The credential gateway refuses every git push while the takeover gate is closed: a request whose
  path ends in `/git-receive-pack` or whose query asks for `git-receive-pack` is refused, recorded
  as `gateway.denied` and never forwarded, and a binding whose route allows a path naming
  `git-receive-pack` is refused at load. Reads are not affected.
- Flights have a readiness check: a `Flight` resource states its `goal`, its delivery path
  (`runway`, and `delivery`: `pull-request` for a ship Flight, `report` for a research one), its
  `autonomy` posture (`owner`, `green` or `client`) and its `acceptance` test. Every admitted Flight
  carries a `Ready` condition in `lucy config status` saying what it lacks, and a flight that lacks
  any of them is never dispatched; the Tower no longer assumes the owner posture when none is given.

- Runs work in their own checkout: a run placed on the host, of a project whose Runway names a
  `repository`, gets a slot of the daemon's worktree pool, reset to the Runway's `defaultBranch` on
  a branch of its own, and the runner runs the work there (its working directory, where its file
  tools are allowed and where its harness reads the repository's instructions), keeping the run's
  scratch directory as its TMPDIR. `runner.lease` reserves the slot at once, without running git,
  and returns its `worktree`, `branch` and `worktree_state: preparing`; the daemon prepares it in
  the background (bounded by 30 minutes, apart from the call's deadline, so a slow first clone no
  longer fails the run), `runner.heartbeat` reports `worktree_state` (`ready` or `failed`, with
  `worktree_error`), as does `runner.slot`, a read-only query the runner polls (from 250 ms up to
  5 s) so the work starts within about a second of the slot being ready (OpenRPC revisions 14 and
  15). A stopped or timed-out preparation never breaks a slot. A run placed in its org's container
  gets no slot yet and works in its scratch directory. Only public repositories can be fetched for now.
- A finished run's slot is given back when its work has landed, now including a branch pushed to
  the remote; otherwise it is quarantined, its work kept, and no longer counts against the pool's
  size, so the next run gets a new slot. `lucy worktree ls [--org <org>]` (`worktree.list`) shows
  every slot and why a quarantined one is held; `lucy worktree discard <org>/<repo>/<slot> --confirm
  <org>/<repo>/<slot>` (`worktree.discard`, the owner only) removes a quarantined or broken slot,
  recorded first, through the path guard, keeping its branch in the clone until git gc.

- The path guard's delete executor: every recursive delete lucy makes now deletes only a path
  strictly inside a declared root, never `/`, your home or a root itself, through an `os.Root` on
  that root, so a symlink, the one asked for or one inside, is removed and never followed. Both
  recursive deletes, a run's scratch directory and a discarded worktree slot, go through it.
  SECURITY.md lists every delete lucy makes.

- The worktree pool's core (`internal/worktree`): per org and repository, `git worktree` slots of a
  bare clone of the remote, each leased by one run, recorded in the event store before git touches
  it (`worktree.added`, `worktree.leased`, `worktree.prepared`, `worktree.prepare_failed`,
  `worktree.released`, `worktree.quarantined`, `worktree.broken`, `worktree.discarded`), reset to
  the freshly fetched base branch, and given back only when the work in it has landed: no commit
  beyond its base, a head on the base branch, the forge's merge of exactly its head, or every
  commit on a ref of the remote. git runs with no user or system configuration and only the https,
  ssh and file transports.

### Changed

- A run's HOME is no longer your home directory: every run gets its org's synthetic home, `home/`
  under the runner's `--root` (0700, kept across runs and shared by the org's runs), so a Pilot
  reads nothing of your home. A harness that needs a login of its own finds none there until one is
  set up in that home. A runner root is bound to its org on first use (a `.org` file in it, 0600), and
  a runner for another org refuses that root, so two orgs never share a HOME. The runner refuses an
  org home that is a symlink or open to others, and its scratch delete removes only a run's own
  `run-*` directory.
