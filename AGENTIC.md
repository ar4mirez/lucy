---
id: agentic
title: "lucy: agent model and documentation rules"
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-POL-001, LUCY-POL-002, LUCY-POL-004, LUCY-POL-005, LUCY-VIEW-004, LUCY-REPO-002, LUCY-REPO-003, LUCY-REPO-004, LUCY-REPO-005]
---

# lucy: agent model and documentation rules

How agents work under lucy, and the rules every doc in this repository follows. Working *on* this
repository as a coding agent: [AGENTS.md](AGENTS.md).

## 1. Principles

1. **LLM proposes, policy disposes.** Models request; deterministic Go policy decides every privileged
   action (LUCY-POL-001, LUCY-POL-002).
2. **Outside text is data.** Repo content, issues, PRs, emails and tool output are tagged untrusted
   with their source and never raise privileges (LUCY-POL-004).
3. **Structured protocols first.** Harnesses run over their structured interfaces; trust, permissions
   and limits are API fields, not screens. A PTY is a viewing path.
4. **Deterministic first.** Rules handle routine events; a small model handles the next tier; only
   judgment reaches a large model.
5. **Build inside, glue only when necessary**, but keep the core small ([D4](docs/decisions/D4-lean-validation.md),
   [R1](docs/decisions/R1-adversarial-review.md) P2-18).

## 2. Roles

Roles are ai-sdlc `AgentRole` resources with lucy's bundled, versioned instructions.

| Role | Does | Never |
|---|---|---|
| Captain (Angel) | Decides, grants postures, answers decisions, steers | - |
| Org supervisor | Turns captain intent into tasks, supervises, escalates | Writes to a project; addresses anyone but the captain |
| Ship worker | Changes one project in an isolated worktree and lands a PR | Merges; answers its own findings; holds credentials |
| Scout | Researches and reports; ships nothing | Writes to a project |
| Reviewer | Reviews a diff independently, from another model family | Reviews its own family's work without recording it |

## 3. Tasks and readiness

Two task shapes: **scout** (knowledge only) and **ship** (a change that lands). Dispatch is refused
until a task has a goal, a delivery path, an autonomy level and an acceptance test (readiness check,
LUCY-SPEC-012). Every task runs in its own worktree, TMPDIR and synthetic HOME inside its org's
sandbox.

## 4. Autonomy and approvals

Merge postures are typed data per org and project (LUCY-POL-005):

| Posture | Merges |
|---|---|
| `captain` (default) | Only on the captain's explicit word |
| `green` | Green PRs, except anything on the fixed list below |
| `client` | Only after the client confirms and the captain relays it through the API |

**The fixed list always needs the captain:** destructive or irreversible changes, security-sensitive
changes, production, billing or live money, credentials, data migrations, cross-org access and real
trade-offs. In every posture: never merge red, never with a required check unreported, never with
closing references the task did not intend. A timeout never approves anything. Promotion is proposed
by lucy and granted only by the captain.

## 5. Agent-facing surfaces

- **MCP** (`lucy mcp`): about nine tools in under 2k tokens of schema (LUCY-VIEW-004):
  `lucy_status`, `lucy_next`, `lucy_dispatch`, `lucy_steer`, `lucy_decide`, `lucy_merge`,
  `lucy_report`, `lucy_doctor`, `lucy_peek`. Each is a thin mapping onto the one API.
- **CLI**: AXI conventions: compact structured output, definitive empty states, structured errors with
  a next step, stable exit codes.
- **Hooks** (`lucy hook <event>`): harness events become typed events in lucy's log.

## 6. Safety rules carried over

Each is a daemon property, not prose: the supervisor never writes to a project; nothing merges outside
the posture; unlanded work is never torn down; steering is addressed by (org, task, session
generation) and refused when ambiguous or stale; acks are generation-bound; only the captain's words
close a decision; workers have no captain channel; one live session per role, fenced before replaced.

## 7. Documentation rules

Enforced by `go test ./internal/doccheck` in CI ([D9](docs/decisions/D9-doc-structure.md)).

### 7.1 Front matter on every doc

Every `*.md` file except `CLAUDE.md` (and `.github/`) starts with front matter (LUCY-REPO-002):

```yaml
---
id: m0                       # unique across the repository
title: "m0: foundation"      # quote values that contain ": "
status: active               # draft | active | accepted | superseded | deprecated
version: 1                   # positive integer, bumped on every substantive change
owner: ar4mirez              # GitHub login, without "@"
requirements: [LUCY-REPO-001]  # IDs this doc defines, schedules or traces; [] if none
---
```

The format is a restricted YAML subset so lucy reads it without a YAML library: only `key: value`
and one-line `key: [a, b]` lists; no nesting; quote a value that contains `: ` or ` #` or starts
with a YAML symbol. Extra keys (`decided`, `supersedes`, `generated`) are allowed.

### 7.2 Stable requirement IDs

- Format `LUCY-<AREA>-<NNN>`, defined once in a PRD table under [docs/prd](docs/prd/README.md),
  never reused or renumbered (LUCY-REPO-003).
- A PRD file's `requirements` list is exactly the IDs it defines.
- Each ID is scheduled in exactly one milestone brief, which must match the table's Milestone cell.
- Any doc may mention an ID; an undefined ID fails the test.
- [docs/requirements.md](docs/requirements.md) is generated (`mise run docs-index`) and must be current.

### 7.3 Size cap and split by area

No Markdown file over **400 lines or 24 KB** (LUCY-REPO-004). Near the cap, split by area into a
directory or sibling files and link them from an index; keep requirement IDs unchanged.

### 7.4 Links and changes

Relative links must resolve inside the repository. CHANGES.md starts with `## Unreleased` and is
rotated per version into [docs/changes](docs/changes/README.md) (LUCY-REPO-005).

### 7.5 Where things go

| Kind | Place |
|---|---|
| What and why | PRODUCT.md; requirements in docs/prd |
| How | TECH.md; extension seams in EXTENSIONS.md |
| A decision | docs/decisions, one ADR each |
| A milestone | docs/milestones, with scope, IDs and exit tests |
| An operation | docs/runbooks |
| Threats and controls | SECURITY.md and docs/security |

Small projects get a **lite profile** with fewer files under the same front matter, ID and size
rules. Its exact shape is defined when `lucy init` learns to scaffold both profiles (LUCY-BRD-004).
