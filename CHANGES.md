---
id: changes
title: Changes
status: active
version: 27
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- Runs work in their own checkout: a run of a project whose Runway names a `repository` is leased
  a slot of the daemon's worktree pool when a runner leases it, reset to the Runway's
  `defaultBranch` on a branch of its own, and the runner runs the work there (its working
  directory, where its file tools are allowed and where its harness reads the repository's
  instructions), keeping the run's scratch directory as its TMPDIR. `runner.lease` returns the
  slot's `worktree` and `branch`, or `worktree_error` when none could be leased, in which case the
  run fails without starting (OpenRPC revision 14). When the run finishes, its slot is given back if
  its work landed and kept otherwise. Only public repositories can be fetched for now.

- The path guard's delete executor: every recursive delete lucy makes now deletes only a path
  strictly inside a declared root, never `/`, your home or a root itself, through an `os.Root` on
  that root, so a symlink, the one asked for or one inside, is removed and never followed. A
  run's scratch directory, the one recursive delete, goes through it. SECURITY.md lists every
  delete lucy makes.

- The worktree pool's core (`internal/worktree`): per org and repository, `git worktree` slots of a
  bare clone of the remote, each leased by one run, recorded in the event store before git touches
  it (`worktree.added`, `worktree.leased`, `worktree.released`, `worktree.release_refused`,
  `worktree.broken`), reset to the freshly fetched base branch on acquire, and given back only
  when the work in it has landed: no commit beyond its base, a head on the base branch, or the
  forge's merge of exactly its head. A slot holding uncommitted changes or unmerged commits stays
  leased, and the refusal is recorded. git runs with no user or
  system configuration and only the https, ssh and file transports. Runs are not placed in their
  slot yet.

### Changed

- A run's HOME is no longer your home directory: every run gets its org's synthetic home, `home/`
  under the runner's `--root` (0700, kept across runs and shared by the org's runs), so a Pilot
  reads nothing of your home. A harness that needs a login of its own finds none there until one is
  set up in that home. A runner root is bound to its org on first use (a `.org` file in it, 0600), and
  a runner for another org refuses that root, so two orgs never share a HOME. The runner refuses an
  org home that is a symlink or open to others, and its scratch delete removes only a run's own
  `run-*` directory.
