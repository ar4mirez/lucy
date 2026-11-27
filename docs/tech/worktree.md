---
id: tech-worktree
title: The worktree pool
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-RUN-007]
---

# The worktree pool

Where a run's checkout of its repository comes from (TECH.md section 6). The code is
`internal/worktree`. The rule it keeps: unlanded work is never torn down.

- **Layout.** Under the pool's root (0700), per org and repository: `base.git`, a bare clone of the
  repository's remote, fetched into `refs/remotes/origin` before every lease and release; and
  `slot-<n>`, slot n, a `git worktree` of it. A pool holds at most `Max` slots per repository (4 by
  default); a lease when every slot is leased or broken is refused (`ErrBusy`). An existing clone
  of a different remote is refused.
- **State.** Each slot's life is recorded on the repository's `worktree/<org>/<repo>` stream:
  `worktree.added`, `worktree.leased` (the run, its branch, the base and the commit it was reset
  to), `worktree.released` (the head it held and how its work was proven landed) and
  `worktree.broken`. The `worktree_slots` projection is the pool's whole state, so a restart knows
  every slot and who holds it. A slot is recorded as leased in the same transaction that picks it,
  before git touches it, so two runs never share one.
- **Acquire.** A free slot (or a new one while the pool is under its size) is reset to the base
  branch as just fetched, with everything else in it removed (`checkout --force`, `reset --hard`,
  `clean -ffdx`), and checked out on a fresh branch `lucy/<run>`. Only a slot given back with its
  work landed is ever free, so the reset loses nothing. A slot that cannot be reset or made, such as
  one whose directory exists though the pool did not make it, is recorded as broken and never leased
  again.
- **Release.** Refused (`ErrUnlanded`), with the slot still leased, unless the worktree has no
  uncommitted change (tracked or untracked; ignored files do not count) and its head is landed:
  - `empty`: the head is the base commit the slot was leased at, so there is nothing to keep;
  - `ancestor`: the head is on the base branch as fetched now (a merge or a fast-forward); or
  - `merged`: the forge reported merging a pull request whose head is exactly the slot's head, and
    the merge commit (or squashed commit) it named is on the base branch. This is how a squash
    merge, which leaves the slot's own commits off the base, is proven.
- **git.** The pool runs git with an environment of its own: no system or user configuration
  (`GIT_CONFIG_NOSYSTEM`, `GIT_CONFIG_GLOBAL=/dev/null`), the pool's root as HOME, no credential
  prompt, a C locale, and only the `https`, `ssh` and `file` transports (`GIT_ALLOW_PROTOCOL`), so
  a remote can never name a transport that runs a command. Org and repository names are labels, a
  base must be a branch name (`git check-ref-format --branch`), a remote may not start with `-` or
  name a `::` transport, and every path is passed after `--`. The pool deletes no file or directory.

## Not built yet

- TODO(LUCY-RUN-007): runs placed in their slot (the runner's working directory is the slot's
  worktree, with its synthetic HOME and TMPDIR kept), the Tower releasing a slot with its takeoff's
  merge as the proof, `lucy worktree ls`, and removing a broken slot through the path guard
  (LUCY-SEC-005).
