---
id: changes
title: Changes
status: active
version: 29
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- The daemon hosts the Tower: at start it dispatches every Flight in the config directory whose
  `Ready` condition is True, once, by the Flight's name, and starts its Pilot through the runner
  protocol; `lucy questions` lists its Pilots' open questions and `lucy answer` relays the owner's
  answer. Until the validation slice connects a forge, the Tower's forge is a stub that reads no
  pull request and merges nothing, so no flight lands: point no Flight at a real repository yet.

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
