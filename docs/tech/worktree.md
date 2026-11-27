---
id: tech-worktree
title: The worktree pool
status: active
version: 3
owner: ar4mirez
requirements: [LUCY-RUN-007]
---

# The worktree pool

Where a run's checkout of its repository comes from (TECH.md section 6). The code is
`internal/worktree`. The rule it keeps: unlanded work is never torn down.

- **Layout.** Under the pool's root (0700), per org and repository: `base.git`, a bare clone of the
  repository's remote, fetched into `refs/remotes/origin` before every fresh preparation and
  release; and `slot-<n>`, slot n, a `git worktree` of it. A pool holds at most `Max` slots per
  repository (4 by default) that are leased or broken; a quarantined or discarded slot is not
  counted. A lease when that many are leased or broken and none is free is refused (`ErrBusy`). An
  existing clone of a different remote is refused.
- **States.** A slot is `free`, `leased` (with its preparation `preparing`, `ready` or `failed`),
  `quarantined` (its run finished with work that has not landed), `broken` or `discarded`. Its life
  is recorded on the repository's `worktree/<org>/<repo>` stream: `worktree.added`,
  `worktree.leased` (the run, its branch and base), `worktree.prepared` (the commit it was reset
  to), `worktree.prepare_failed` (why), `worktree.released` (the head it held and which proof
  applied), `worktree.quarantined` (the head and why), `worktree.broken` and `worktree.discarded`
  (by whom, the run, its branch and head). The `worktree_slots` projection is the pool's whole
  state, so a restart knows every slot and who holds it.
- **Reserve, in the lease.** `runner.lease` reserves the run's slot in one store transaction and
  runs no git: the slot the run holds already, else a free slot, else a new one (numbers only
  grow; a discarded number is never used again). The base must be a branch name by git's rules,
  checked without git. The lease returns the slot's path and branch with `worktree_state:
  preparing`.
- **Prepare, in the background.** The daemon then prepares the slot under its own context, bounded
  by `WorktreePrepareTimeout` (30 minutes by default) and cancelled only when the daemon stops,
  never by the API call's deadline. A fresh lease makes the clone if needed, runs `git worktree
  prune` and a fetch, resolves the base, frees each quarantined slot whose work has landed since,
  and resets the slot to the base with everything else in it removed (`checkout --force`, `reset
  --hard`, `clean -ffdx`), or makes its worktree when it has none, on a fresh branch
  `lucy/<run>-<hash>` (the run id with anything but letters, digits, `.`, `_` and `-` made `-`, then
  the first 8 hex digits of the SHA-256 of the run id, so two runs never share a branch). Ready is
  recorded with the base commit. Only a slot whose work landed, or never started, is free, so the
  reset loses nothing.
- **How a preparation fails.** A failure of the slot's own worktree (a `git worktree add` or reset
  that fails, a path that is not a directory) is recorded as broken, with git's error: the slot is
  never leased again. A failure that is not the slot's own (the fetch, a base the remote lacks, a
  clone of another remote, the run's branch holding commits that have not landed) is recorded as
  `prepare_failed` with the error; a cancellation or a timeout is recorded the same way with the
  fixed reason that the preparation stopped, and never as broken. Either leaves the slot reusable: a
  fresh lease, which never held the run's work, is given back free, and the next preparation of it
  repairs or resets it. The run sees `failed` with the reason.
- **The runner waits.** `runner.slot`, a read-only query (it neither renews the lease nor rotates
  the token, and records no command, event or audit entry; refused as `fenced` unless the runner
  holds the run under that fence), reports the run's `worktree_state` (and `worktree_error` when
  failed); `runner.heartbeat` reports the same. A runner holds a preparing lease without starting
  the work, polling `runner.slot` from 250 ms, doubling to 5 s, and heartbeating only on the
  lease's usual schedule (a third of its life), so waiting adds no renewals or token rotations. It
  starts the work at the first poll or heartbeat that reports `ready`, so a slot prepared in
  moments starts its run within about a second; it finishes the run as failed with the reason when
  one reports `failed` (or none), or when the preparation is still not done a minute past the
  lease's `worktree_deadline` (when the daemon stops preparing it at the latest), and as stopped
  when the Tower stops it first. A lease with no worktree starts at once.
- **The lease fails at once** when the pool's root is not a directory only its owner can open, or
  the org or repository is not a valid name: those are checked when the slot is reserved, and the
  lease carries `worktree_error`. A preparation records how it ended on every path, a failed read
  of the slot or a failed record of ready included.
- **Restarts.** When the daemon starts, before it leases anything, a slot still recorded as
  preparing (the daemon stopped without recording how its preparation ended) is recorded as
  stopped: its run sees `failed`, and a retry, a new generation, gets a fresh preparation. The slot
  of each run that has finished is released, as a release cut short by the stop would have been.
  A daemon that stops cleanly waits for its preparations and releases to record how they ended.
- **A run that holds a slot already.** Reserving again (a retry, or a restart after the lease was
  recorded) returns the same slot, prepared again. Its worktree is made again when it is missing or
  not a worktree of `base.git`, put on the run's branch (`checkout -B` at the base, which keeps
  untracked and ignored files) when it is clean at the lease's base commit, and otherwise left as
  it is, since it may hold the run's work. When the run's branch has commits beyond the base, the
  worktree is made on that branch, or a clean one checked out on it, and the branch is never moved.
- **Release, when the run finishes.** In the background too. A slot never made ready for the run
  is given back as it is (`unprepared`): the run's work never started in it. Otherwise its work has
  landed when the worktree has no uncommitted change (tracked or untracked; ignored files do not
  count) and its head is:
  - `empty`: the base commit the slot was leased at, so there is nothing to keep;
  - `ancestor`: on the base branch as fetched now (a merge or a fast-forward);
  - `merged`: exactly the head of a pull request the forge reported merging, whose merge commit
    (or squashed commit) is on the base branch (squash merges leave the slot's own commits off the
    base); or
  - `pushed`: a commit every one of whose ancestors is on a ref of the remote (`refs/remotes/origin/*`
    after a fetch), so a run that pushed its branch frees its slot.

  The proof that applied is recorded with the release.
- **Quarantine.** A slot whose work has not landed is quarantined, recorded with why: `uncommitted
  changes`, `commits on no remote ref`, or `its work could not be checked` (the fetch or a read of
  the worktree failed). Its work is kept untouched; it is never leased again and not counted
  against `Max`, so the pool grows a new slot and no repository deadlocks. A later release of it,
  or the next fresh preparation of the repository, frees it once its work has landed (its branch
  pushed, say). A release whose context ends records nothing and leaves the slot as it was.
- **`lucy worktree ls [--org <org>]`** (`worktree.list`, read scope) shows each slot: its
  `<org>/<repo>/<number>`, state, preparation, run, branch, head, and why it is held when
  quarantined or broken.
- **`lucy worktree discard <org>/<repo>/<slot> --confirm <org>/<repo>/<slot>`**
  (`worktree.discard`, admin scope: the owner only) is refused unless the confirmation repeats the
  slot exactly and the slot is quarantined or broken, never free or leased. It records
  `worktree.discarded` before it removes anything, then removes the slot's directory through the
  path guard's `RemoveAll`, with the pool's root as the one root, and runs `git worktree prune` in
  `base.git`. The run's branch stays in `base.git`, so its commits are recoverable until `git gc`;
  work never committed is gone. A discard whose removal failed can be repeated.
- **Concurrency.** A Pool runs one preparation, release or discard of a repository at a time, so
  git steps on `base.git` never race; a reservation is one store transaction, so two runs never
  share a slot. One Pool per store is the unit of concurrency.
- **A clone left without origin.** If making `base.git` stopped after `git init` and before the
  remote was added, the next preparation adds it, as long as the clone holds no refs; otherwise it
  is refused.
- **git.** The pool runs git with an environment of its own: no system or user configuration
  (`GIT_CONFIG_NOSYSTEM`, `GIT_CONFIG_GLOBAL=/dev/null`), the pool's root as HOME, no credential
  prompt, a C locale, and only the `https`, `ssh` and `file` transports (`GIT_ALLOW_PROTOCOL`), so
  a remote can never name a transport that runs a command. Org and repository names are labels, a
  remote may not start with `-` or name a `::` transport, a release's proof must name full object
  ids (40 or 64 hex digits) and is passed after `--end-of-options`, and every path is passed after
  `--`. Besides a discarded slot, what the pool lets git remove is `base.git`'s stale worktree
  registrations (`git worktree prune`) and, when a free slot is reset for a new lease, that slot's
  tracked changes and its untracked and ignored files. A held or quarantined slot is never reset.

- **Placing runs.** A run placed on the host, of a project whose Runway (`lucy/v1alpha1` `Runway`,
  in the org's namespace) names a `repository` (`owner/name`), works in a slot of it
  (`worktree.Source`), at the run's base (the Runway's `defaultBranch`, `main` by default). The slot
  is the program's working directory, where its prompted file tools are allowed and where its
  harness reads the repository's instructions, while its TMPDIR stays the run's scratch directory
  and its HOME the org's. A slot that cannot be reserved is reported (`worktree_state: failed`,
  `worktree_error`), and the runner fails the run without starting it; a project whose Runway names
  no repository gets no checkout. A run placed in its org's container gets no slot and works in its
  scratch directory there, since the slot's path is the host's. The remote is
  `https://github.com/<owner>/<name>.git`; the daemon's git has no credentials of its own, so a
  private repository cannot be fetched yet.

## Not built yet

- TODO(LUCY-RUN-007): the Tower giving a slot back at takeoff with its merge as the proof (the hosted
  Tower's forge merges nothing until the validation slice), and fetching a private repository (its credentials through the
  org's gateway).
- TODO(LUCY-SBX-001): mounting the slot into the org's container for a container-placed run
  (berth#216).
