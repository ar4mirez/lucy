---
id: tech
title: "lucy: technical specification"
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-EVT-001, LUCY-EVT-002, LUCY-EVT-003, LUCY-API-001, LUCY-API-002, LUCY-RUN-001, LUCY-CRED-001, LUCY-OPS-001, LUCY-REPO-007]
---

# lucy: technical specification

Status: m0. Only the skeleton exists; everything below is the design the m0-m3 slices build.

## 1. Shape

One Go binary ([D1](docs/decisions/D1-language-go.md)); every box is a mode of it:

| Mode | Role |
|---|---|
| `lucy daemon` | Control plane: API, policy engine, event store, scheduler, supervisor loop, forge pollers, validation orchestrator, vault |
| `lucy runner` | Owns agent processes on one machine or inside one org container; speaks the runner protocol |
| `lucy gateway` | Per-org egress credential gateway, a separate process outside the agents' container |
| `lucy mcp` | MCP server for Claude chat; `--remote` forwards to a daemon over the tailnet |
| `lucy hook <event>` | Harness hook entrypoint; posts typed events |
| CLI, TUI, `lucy attach` | Thin clients of the API |

```
 surfaces: CLI · TUI · web · Claude chat (MCP) · paired devices      (thin, one API)
        │ unix socket (local) · HTTPS/WebSocket on loopback or tailnet (remote)
 ┌──────▼──────────────────── lucy daemon (control plane) ───────────────────────┐
 │ API · policy engine · event store + hash-chained audit · scheduler · vault    │
 │ supervisor (rules → small model → large model) · validation orchestrator     │
 └──────┬────────────────────────────────────────────────────────────────────────┘
        │ runner protocol: lease, events, heartbeat, fencing token
 ┌──────▼──── runner (this machine) ──────┐     ┌─ gateway (per org, sidecar) ─┐
 │ berth container per active org:         │────▶ adds tokens to allowlisted   │──▶ forge, model API
 │ sessions, worktrees, validation agents  │     │ requests; fails closed       │
 └─────────────────────────────────────────┘     └──────────────────────────────┘
```

## 2. State

| Kind | Location (Linux; macOS uses `~/Library` equivalents) | Contents |
|---|---|---|
| Config | `$XDG_CONFIG_HOME/lucy/` | `config.toml`, per-org spec resources, instruction overlays |
| Data | `$XDG_DATA_HOME/lucy/` | `lucy.db`, pools, materialized skills, pinned toolchain |
| State | `$XDG_STATE_HOME/lucy/` | logs, daily database backups |
| Runtime | `$XDG_RUNTIME_DIR/lucy/` | daemon and runner sockets |

- One SQLite database (pure Go, WAL), never a repo (LUCY-EVT-001). Files 0600, dirs 0700.
- Important state stays out of `~/.config`; everything lucy writes into other tools' config is
  recorded and re-derivable (`lucy doctor --repair`).
- Bundled instructions are plain Markdown embedded in the binary, viewable and overridable through
  the config overlay dir.

## 3. Event log

- Typed events (`status`, `pr.checks`, `pr.merged`, `validation.finding`, `session.exited`, ...) are
  appended in the same transaction as the state change (LUCY-EVT-002).
- Consumers read past a cursor; `ack(consumer, through_seq, generation)` is refused if the
  generation changed, so an interruption before ack leaves work durable (LUCY-EVT-003).
- A harness Stop hook asks the daemon for actionable events, so an idle session costs no tokens.

## 4. API

One typed API (JSON-RPC or Connect; chosen in m0 slice 4) on a 0600 unix socket, the same over HTTPS
and WebSocket for remote surfaces (LUCY-API-001). Messages: `hello`/`welcome`, `query`,
`subscribe(from_seq, filter)`, `attach(session, view|input, from_seq)`, `input`, and
`command(steer | decide | dispatch | stop | merge)` (LUCY-API-002). Every call passes the policy
engine; every command is an audit entry.

## 5. Orgs, runners and sessions

- **Isolation per org:** harness config dir, GitHub token, git identity and key, repo allowlist,
  memory rows, merge rules, container. The daemon refuses any cross-org read or write.
- **Runner protocol** (LUCY-RUN-001): lease a task, stream events, heartbeat, fencing token. This
  machine is the first runner; a cloud runner later uses the same protocol.
- **Sessions:** structured protocols first (Claude Agent SDK or `claude -p --output-format
  stream-json`; Codex app-server; ACP). Leases with generation and heartbeat; reattach before spawn;
  fence, then replace.
- **Worktree pool:** `git worktree` slots per org and repo, leased in SQLite, reset on acquire,
  released only with a landed-work proof; per-task TMPDIR; path guard on every delete.
- **Credentials:** the per-org gateway adds tokens only to allowlisted hosts and paths
  (LUCY-CRED-001); capability tokens are scoped to (org, task, action, expiry).

## 6. External dependencies

Every external is a managed risk ([D8](docs/decisions/D8-built-in-vs-external.md)); `lucy setup`
installs through mise from a pinned manifest and `lucy doctor` checks each row (LUCY-OPS-001).

| Component | Kind | Install or check | Main risk and mitigation |
|---|---|---|---|
| git | hard | checked, minimum version | missing worktree features: doctor refuses below the minimum |
| Container runtime (Docker, Podman, Apple `container`) | hard | checked | daemon down: doctor names the engine in use |
| berth | hard | mise, exact version; driven by its API | API change: pin plus contract tests |
| Claude Code | hard (one harness) | berth image in containers; version range checked | near-daily releases: structured adapter, nightly contract tests, held upgrades |
| mise | hard | bootstrap, minimum version | behaviour change: version check, lockfile |
| Another model family's harness | optional | mise | without it review is recorded as same-family |
| herdr, tmux | optional | checked | viewers only; never own a session |
| Tailscale | optional | checked | loopback still works |
| Node and the axi tools, lavish-axi | optional | mise, exact versions | supply chain: pinned, off unless enabled |
| no-mistakes skill | optional, per org | pinned import with hash | drift: hash verified at spawn |
| ai-memory | optional | mise, exact version and hash | off by default |

Built in: GitHub API client, quota and usage data, SQLite, sigstore verification, policy engine,
gateway, pipeline, TUI, dashboard and MCP server. lucy's own Go dependencies are kept minimal; the
skeleton has none.

## 7. Repository layout

| Path | Contents |
|---|---|
| `cmd/lucy/` | The binary's entrypoint (version, help and self-update so far) |
| `internal/` | All packages; nothing is a public Go API yet |
| `internal/doccheck/` | Doc-rules checker and test |
| `internal/selfupdate/` | `lucy self-update`, which updates lucy through mise |
| `internal/workflowcheck/` | Test enforcing the GitHub Actions rules: SHA pins, permissions, release trigger |
| `scripts/` | `verify-dist.sh`: checks a goreleaser `dist/` (assets, SBOMs, checksums) |
| `.goreleaser.yaml` | The release build ([release runbook](docs/runbooks/release.md)) |
| `docs/` | PRD, milestones, decisions, runbooks, security, changes, requirements index |
| `.github/` | CI, the tag-triggered release workflow, Dependabot |

## 8. Build and CI

- Go is pinned in `go.mod` and `mise.toml`; release builds are `CGO_ENABLED=0` for linux and
  darwin, amd64 and arm64, made by goreleaser on a `v*` tag ([release runbook](docs/runbooks/release.md)).
- `mise run check` = gofmt check, `go vet`, `go test -race`, which includes the doc rules.
- CI runs the same on `ubuntu-latest` and `macos-latest` (LUCY-REPO-007) with actions pinned by SHA,
  `permissions: contents: read`, no secrets and no persisted checkout credentials.
- The version is stamped with `-ldflags "-X main.version=<v>"` (goreleaser also sets `main.commit` and
  `main.date`).
- CI also runs `goreleaser check` and a snapshot release build on every pull request; only the
  release workflow publishes, and only its job has write scopes and an OIDC token.
