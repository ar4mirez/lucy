---
id: product
title: "lucy: product specification"
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-REPO-006, LUCY-CORP-002, LUCY-RUN-011, LUCY-RUN-010, LUCY-ORG-007]
---

# lucy: product specification

## 1. Summary

lucy replaces a supervision stack that today lives in cloned folders and scripts (firstmate, its
worktree pool, its validation daemon and per-org "second mate" homes) with one installed Go binary.
One captain (Angel) runs several organisations' agent work through it. Each org is hard-isolated:
its own account, credentials, sandbox container, projects, memory and merge rules. lucy dispatches
tasks, owns every agent session, validates work with its own lean pipeline, and lands PRs only when
deterministic policy allows it.

## 2. Who it serves

- **The captain**: one person who decides, grants autonomy and answers escalations from a terminal,
  Claude chat, a dashboard or a phone.
- **Orgs**: the captain's own projects and client organisations, each with its own posture (for
  example captain-confirm, merge-on-green with exceptions, or client-confirm).
- **Agents**: supervisors, workers, scouts and reviewers that lucy runs as untrusted executors.

## 3. Goals

1. **Keep the judgment, move the mechanics into code.** Every firstmate safety rule and recorded
   incident becomes an executable scenario in a conformance corpus; an org moves to lucy only when its
   corpus is green (LUCY-CORP-002, LUCY-ORG-007).
2. **No folder, no repo.** State lives in XDG dirs and SQLite; the first-mate role is a plugin any
   Claude Code session can take on.
3. **Safe by construction.** Policy, not models, authorizes privileged actions; workers hold no
   credentials; every agent runs in its org's container.
4. **Cheaper per merged PR.** A lean pipeline (deterministic tests, one cross-family review, fixes in
   the warm worker) and deterministic-first supervision.
5. **Sessions that never die with a viewer**, and remote control from the start.
6. **One install.** `lucy setup` installs and checks everything lucy needs; `lucy doctor` keeps it
   healthy.

Non-goals are in the [README](README.md#non-goals) (LUCY-REPO-006).

## 4. What lucy replaces and keeps

| Today | lucy |
|---|---|
| firstmate (contract, skills, scripts) | Rewritten: mechanics as a typed Go state machine, judgment as bundled, versioned prompts |
| treehouse (worktree pool) | Built-in pool with leases in SQLite and a path guard on every delete |
| no-mistakes (validation daemon) | lucy's own lean pipeline; the no-mistakes skill stays an optional import |
| herdr lifecycle | lucy owns sessions; herdr stays one of five viewers |
| gh-axi, chrome-devtools-axi, lavish-axi | lucy has its own GitHub client; the tools stay optional worker tools |
| tasks-axi, quota-axi | Absorbed: backlog and quota tables in lucy's database |
| Per-org second-mate homes | Orgs are rows plus a config file; each org supervisor is a session |
| Memory files | Typed, org-scoped knowledge with provenance and expiry |

## 5. Incidents designed in

| Incident | What lucy does |
|---|---|
| A worker's `rm_rf('/')` wiped `~/.config` | Every agent, validation included, runs in its org's container; synthetic HOME; delete guards; doctor checks snapshots |
| Duplicate supervisor sessions after a restart | Session leases with generation and heartbeat; reattach before spawn; fence, then replace |
| Trust and onboarding prompts after config loss | Account state re-asserted before every spawn; a task needing a human is held, not wedged |
| Skill links lost with `~/.agents` | Bundled skills with content hashes, re-linked and verified at every spawn |

Smaller lessons: lucy lands through one typed call (never a shell chain), checks closing references
against the task's intent, lints PR text for `@` mentions and secrets, and gives each task its own
TMPDIR.

## 6. Measures

- Cost per merged PR at most firstmate's in m1, then lower (LUCY-RUN-011).
- Supervisor tokens per org-day, tracked as a KPI (LUCY-RUN-010).
- Zero lost work and zero duplicate sessions across cutover.
- Corpus green per org before cutover.

## 7. Plan

Build order, not dates; costs are processed tokens. Details in [docs/milestones](docs/milestones/README.md);
requirements in [docs/prd](docs/prd/README.md) and the [index](docs/requirements.md).

| Order | Milestone | PRs | Tokens |
|---|---|---|---|
| m0 | Foundation: API, event store, policy engine, spec model, simulation, corpus harness, signed releases, security baseline | 37-57 | 0.9-3.1B |
| m1 | ar4mirez end to end, shadow first, then takeover with rollback | 145-215 | 3.6-11.8B |
| m2 | Every org on lucy | 48-78 | 1.2-4.3B |
| m3 | Captain surface; firstmate retired | 29-46 | 0.7-2.5B |
| m4 | Breadth, priced per item | 5-15 each | 0.1-0.8B each |

Total m0-m3: 259-396 PRs, about 6.5-21.8B processed tokens (about 7.6-25.7B with supervision).
