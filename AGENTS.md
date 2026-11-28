---
id: agents
title: Instructions for coding agents
status: active
version: 9
owner: ar4mirez
requirements: [LUCY-REPO-002, LUCY-REPO-004, LUCY-REPO-007]
---

# Instructions for coding agents

lucy is a Go project. CLAUDE.md imports this file; do not duplicate it there.

## Read first

- [AGENTIC.md](AGENTIC.md): working rules and the documentation rules.
- The milestone plan you are working in, under [docs/milestones](docs/milestones/README.md), and the
  requirement IDs it lists ([index](docs/requirements.md)).
- [TECH.md](TECH.md) for architecture and layout; [SECURITY.md](SECURITY.md) before touching
  credentials, the API, policy, deletes or CI.

## Commands

Toolchain: `mise install` (Go is pinned in `mise.toml` and `go.mod`). On shared machines keep
`GOFLAGS=-p=2` (mise sets it) and run one heavy build or test job at a time.

| Task | Command |
|---|---|
| Everything CI runs | `mise run check` |
| Tests only | `go test -race ./...` |
| Doc rules only | `go test ./internal/doccheck` |
| Regenerate the requirements index | `mise run docs-index` |
| Build | `mise run build` (binary in `bin/lucy`) |
| Format | `gofmt -w .` |

## Layout

- `cmd/lucy/`: the lucy binary (version, help, self-update, `daemon`, `status`, `events`, `config`,
  `audit`, `runner`, `questions`, `answer`, `decisions`, `flights`, `approve`, `sandbox`, `gateway`, `vault`,
  `capability` and `worktree` so far).
- `internal/doccheck/`: the doc-rules checker and its test.
- `docs/`: PRD, milestones, decisions, runbooks, security, changes, and the generated index.
- `.github/`: CI and Dependabot.

## Rules

- Work on a branch; never push to `main`; never merge your own PR.
- Every change keeps `mise run check` green on Linux and macOS.
- Every new or changed doc follows the doc rules in AGENTIC.md: front matter, size cap, stable IDs.
  Changing a PRD table means running `mise run docs-index` and committing the result.
- Record user-visible changes under `## Unreleased` in [CHANGES.md](CHANGES.md).
- Pin every GitHub Action by full commit SHA; keep workflow permissions read-only unless a job needs
  more, and say why in the workflow.
- Add a dependency only with a reason in the PR; prefer the standard library.
- Never delete outside a temp dir you created; never commit secrets, `.env` files or local harness
  state.
- Write `@` handles in PR text inside backticks unless you mean to notify someone.
