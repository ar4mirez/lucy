---
id: tech
title: "lucy: technical specification"
status: active
version: 6
owner: ar4mirez
requirements: [LUCY-EVT-001, LUCY-EVT-002, LUCY-EVT-003, LUCY-EVT-004, LUCY-EVT-005, LUCY-EVT-006, LUCY-API-001, LUCY-API-002, LUCY-API-003, LUCY-API-004, LUCY-API-005, LUCY-API-006, LUCY-API-007, LUCY-API-008, LUCY-POL-001, LUCY-POL-002, LUCY-POL-003, LUCY-POL-004, LUCY-POL-005, LUCY-POL-006, LUCY-POL-007, LUCY-RUN-001, LUCY-CRED-001, LUCY-OPS-001, LUCY-REPO-007]
---

# lucy: technical specification

Status: m0. The skeleton, the release pipeline, the event store (section 3), the first part of the
API with `lucy daemon` (section 4) and the policy engine (section 5) exist; the rest below is the
design the m0-m3 slices build.

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
| State | `$XDG_STATE_HOME/lucy/` | logs, pre-migration database snapshots, daily database backups |
| Runtime | `$XDG_RUNTIME_DIR/lucy/` | daemon and runner sockets |

- One SQLite database (pure Go, WAL), never a repo (LUCY-EVT-001). Files 0600, dirs 0700.
- Important state stays out of `~/.config`; everything lucy writes into other tools' config is
  recorded and re-derivable (`lucy doctor --repair`).
- Bundled instructions are plain Markdown embedded in the binary, viewable and overridable through
  the config overlay dir.

## 3. Event log

Built in `internal/eventstore`; operations in the [database runbook](docs/runbooks/database.md).

- **Source of truth.** An append-only `events` table (triggers refuse `UPDATE` and `DELETE`); every
  other table is a projection of it (LUCY-EVT-006). The driver is `modernc.org/sqlite`, pure Go, so
  releases stay `CGO_ENABLED=0`.
- **Envelope.** `seq` (global, commit order), `stream`, `stream_version` (1..n, no gaps), `type` and
  `type_version` (the payload schema), `envelope_version`, time, `actor`, JSON payload. Payload types
  are registered Go structs (`status`, `pr.checks`, `pr.merged`, `validation.finding`,
  `session.exited`, ... as later slices add them); a changed shape is a new version registered next
  to the old one; decoding rejects unknown fields.
- **Append.** Optimistic concurrency per stream: an append names the version it expects (or
  `NoStream`, `AnyVersion`) and gets a conflict error if another writer got there first. Projectors
  run inside the same transaction, so events and the state change they describe commit together
  (LUCY-EVT-002); a write that fails partway cannot commit.
- **Projections.** A projector owns its tables, resets and replays them on demand, and is rebuilt
  automatically when its version changes or it is behind the log.
- **Consumers.** Read past a cursor; `Claim` starts a new generation and
  `ack(consumer, through_seq, generation)` is refused if the generation changed, so an interruption
  before ack leaves work durable (LUCY-EVT-003). Acks can commit in the same transaction as the
  events the work produced.
- **Migrations.** Numbered, checksummed SQL files applied forward-only, each in one transaction; a
  newer or edited schema is refused; a uniquely named snapshot is written to the state dir before
  an upgrade, and never overwritten; upgrades are tested
  from every older version and from committed dumps (LUCY-EVT-004).
- **Limits and secrets.** No secrets in events: secret-looking field names are refused at
  registration and at append; payloads are capped at 64 KiB, appends at 256 events; all SQL binds
  values as parameters, and a test round-trips SQL-breaking stream, actor, consumer and payload
  strings and checks the schema and log are unchanged (LUCY-EVT-005).
- **Durability.** WAL with `synchronous=FULL` (plus `fullfsync` on macOS); one writer connection
  with immediate transactions; read-only reader connections.
- **Audit seam.** The hash-chained audit (LUCY-SPEC-009) lands in m0 slice 6 as a projector over
  these events, written in the same transaction as the events it chains.
- A harness Stop hook asks the daemon for actionable events, so an idle session costs no tokens.

## 4. API

One typed API on a 0600 unix socket, the same over HTTPS and WebSocket for remote surfaces
(LUCY-API-001). Messages: `hello`/`welcome`, `query`, `subscribe(from_seq, filter)`,
`attach(session, view|input, from_seq)`, `input`, and `command(steer | decide | dispatch | stop |
merge)` (LUCY-API-002). Every call passes the policy engine; every command is an audit entry.
Operations: [daemon runbook](docs/runbooks/daemon.md).

- **Protocol** ([D10](docs/decisions/D10-api-json-rpc.md)): restricted JSON-RPC 2.0, one request per
  HTTP/1.1 POST to `/rpc/v1`. The method table in `internal/api` declares every method's scope,
  whether it mutates, and its params and result types; `internal/api/openrpc.json` is generated
  from it, committed and served by `rpc.discover` (LUCY-API-007). Built so far: `hello`, `health`,
  `status.get`, `events.list` (pages the event log), `rpc.discover`, `daemon.shutdown`, and
  `policy.evaluate` and `policy.decisions` (section 5).
- **Transport and identity.** `lucy daemon` listens on `$XDG_RUNTIME_DIR/lucy/lucy.sock` (0600 in a
  0700 dir, one daemon per dir by `flock`). Each connection's peer credentials (SO_PEERCRED on
  Linux, LOCAL_PEERCRED on macOS) must name the daemon's own uid, or it is closed unread; the
  accepted caller (uid, gid, pid, device `local`) rides on the request context (LUCY-API-003).
- **Authorization seam.** An `Authorizer` grants scopes (read, decide, steer, attach-view,
  attach-input, admin) and checks each call's scope before its params are decoded. Today only the
  local owner holds scopes; the policy engine adds org and task checks (LUCY-API-004).
- **Recording.** A mutating call is appended as an `api.command` event (method, request id, caller,
  params digest) before it runs, and does not run if the append fails (LUCY-API-005).
- **Limits** (LUCY-API-006): 64 KiB bodies, 32 levels of nesting, 8 KiB headers; 5 s to read
  headers, 15 s per request body, 30 s to write, 60 s idle, 10 s per call; 64 connections. Params
  are decoded strictly: no unknown, duplicate or case-folded members, bounds from struct tags.
- **Errors** (LUCY-API-008): JSON-RPC codes plus lucy's (-32001 forbidden, -32002 unsupported
  version, -32003 too large, -32004 unavailable, -32005 timeout), one fixed message and `kind` per
  code; internal errors are logged with a correlation id, which is all the caller sees.
- **Remote transport (m1).** The handler is transport-neutral; HTTPS needs only a TLS listener and a
  device authenticator. No network listener exists in m0: remote HTTPS with device pairing, scoped
  credentials and TLS lands in m1 (LUCY-VIEW-006).
- **Clients.** `internal/api/client` is the Go client: strict about the envelope, tolerant of new
  result fields. The CLI's `lucy status`, `lucy events` and `lucy daemon stop` use it.

## 5. Policy engine

"LLM proposes, policy disposes" (LUCY-POL-001): models and agents only request; deterministic Go in
`internal/policy` decides every privileged action. No policy language is embedded: the rules are a
small Go table, and the spec model's `AutonomyPolicy` and `QualityGate` resources (slice 6) feed
them data, not code.

- **Request.** Actor (id, role, org: set by the daemon from the authenticated caller), kind (`read`,
  `write`, `merge`, `delete`, `force_push`, `branch_delete`, `history_rewrite`, `credential_use`),
  target (org, project, resource) and inputs, each with its source (`captain`, `config`, `daemon`,
  `task` are trusted; `agent`, `tool_output`, `repo_content`, `issue`, `pr_body`, `pr_comment`,
  `web`, `email` are not). The requester never sets trust (LUCY-POL-004).
- **Facts.** What the daemon knows from trusted sources: posture (captain by default), check state,
  unreported required checks, fixed-list categories, a PR's head SHA, base ref and base SHA, the
  credential's scope, default branch, home, declared roots, the symlink-resolved delete target and
  captain approvals. Approvals come only from decision records and name two digests: the action's
  (`action_sha256`, over actor, kind, target), so one cannot be reused for another action, and the
  bound facts' (`facts_sha256`, the SHA-256 of `{"kind":..., "facts":{...}}` holding a merge's head
  SHA, base ref and base SHA, a delete's resolved path, a credential use's scope, or the default
  branch for force-push, branch delete and history rewrite), so an approval lapses when those
  change: a matching approval made under other facts is a deny, not an escalation (LUCY-POL-002,
  rule set version 2).
- **Rules, in order; the first with an opinion decides:** `request.valid`, `facts.valid`,
  `org.isolation` (an org-bound role acts only in its org), `taint.trusted-basis` (a privileged
  request needs a trusted input), `read.in-org`, `write.worker-only`, `credential.gateway-only`,
  `merge.role`, `merge.head-known` (deny when the PR's head SHA is unknown), `merge.checks-green` (never red, pending, unknown or with a required check
  unreported), `merge.closing-refs`, `merge.fixed-list` (needs the captain's approval, then lets
  the posture decide, so a client-posture fixed-list merge needs both the captain's approval and
  the relayed client confirmation), `merge.posture`, `destructive.role`, `delete.path-guard`,
  `delete.scratch` (allow), `delete.project` (captain), `git.default-branch` (deny; the target
  must be a plain branch name by git check-ref-format rules and is compared case-insensitively),
  `git.captain`, and `default.deny`.
- **Decision.** `allow`, `deny` or `require-captain`, with the rule, a reason, every approval it
  rested on (`approvals_used`, in the order the rules used them; empty unless allowed), whether any input was untrusted, and the rule set's version and SHA-256 (LUCY-POL-006). The hash
  covers the embedded `rules.go` and `trust.go` and the path guard's version;
  `internal/policy/testdata/ruleset.sum` records each version's hash and a test refuses a changed
  rule set under an old version. `Evaluate` is pure; a panic, an unknown outcome or facts the
  daemon could not gather are a deny under `engine.error`.
- **Recording.** `policy.evaluate` appends a `policy.decision` event (stream `policy`) before it
  returns the decision; an unrecorded decision is never returned (LUCY-POL-003).
  `policy.decisions` pages them. TODO(LUCY-POL-001): the privileged executors of m1 (merge, delete,
  push, the gateway) call the engine and act only on a recorded allow.
- **Path guard** (`internal/pathguard`, LUCY-POL-007): refuses `/`, `$HOME` and its ancestors, the
  roots themselves, relative paths, `.`, `..` and empty components, control, invisible and
  lookalike characters, and anything not strictly inside a declared root, checking both the
  requested spelling and the symlink-resolved path; roots that are `/`, home or contain home are
  refused when declared. A delete inside the task's scratch root is allowed; inside a project it
  needs the captain. TODO(LUCY-SEC-005): every delete lucy makes goes through it, deleting the
  resolved path without following links (m1).

## 6. Orgs, runners and sessions

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

## 7. External dependencies

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
gateway, pipeline, TUI, dashboard and MCP server. lucy's own Go dependencies are kept minimal:

- `modernc.org/sqlite` (with its `modernc.org/libc` runtime and their small dependencies): the
  SQLite engine translated to Go, so the binary stays static and cross-compiled without cgo
  (LUCY-EVT-001, LUCY-REL-001).
- `golang.org/x/sys`, already pulled in by `modernc.org/sqlite` and direct since m0 slice 4: the
  peer-credential syscalls (`SO_PEERCRED`, `LOCAL_PEERCRED`) behind the socket's identity check
  (LUCY-API-003).
- `gopkg.in/yaml.v3`, used only by tests: `internal/workflowcheck` parses the workflows so their
  permission and pinning rules, a security baseline (LUCY-REL, checklist area 5), do not pass or fail
  on formatting.

## 8. Repository layout

| Path | Contents |
|---|---|
| `cmd/lucy/` | The binary's entrypoint: version, help, self-update, `daemon`, `status`, `events` |
| `internal/api/` | The API's method table, wire format, errors and generated OpenRPC schema (section 4) |
| `internal/api/client/` | The Go client of the API |
| `internal/server/` | The API's HTTP handler, peer-credential listener and authorizer seam |
| `internal/daemon/` | `lucy daemon`: runtime dir, lock, socket, store, graceful stop |
| `internal/` | All packages; nothing is a public Go API yet |
| `internal/doccheck/` | Doc-rules checker and test |
| `internal/eventstore/` | The SQLite event store: event log, projections, consumers, migrations (section 3) |
| `internal/pathguard/` | The path guard every delete passes (section 5) |
| `internal/policy/` | The policy engine: request model, rules, rule-set hash, decision record (section 5) |
| `internal/selfupdate/` | `lucy self-update`, which updates lucy through mise |
| `internal/workflowcheck/` | Test enforcing the GitHub Actions rules: SHA pins, permissions, release trigger |
| `scripts/` | `verify-dist.sh`: checks a goreleaser `dist/` (assets, SBOMs, checksums) |
| `.goreleaser.yaml` | The release build ([release runbook](docs/runbooks/release.md)) |
| `docs/` | PRD, milestones, decisions, runbooks, security, changes, requirements index |
| `.github/` | CI, the tag-triggered release workflow, Dependabot |

## 9. Build and CI

- Go is pinned in `go.mod` and `mise.toml`; release builds are `CGO_ENABLED=0` for linux and
  darwin, amd64 and arm64, made by goreleaser on a `v*` tag ([release runbook](docs/runbooks/release.md)).
- `mise run check` = gofmt check, `go vet`, `go test -race`, which includes the doc rules.
- CI runs the same on `ubuntu-latest` and `macos-latest` (LUCY-REPO-007) with actions pinned by SHA,
  `permissions: contents: read`, no secrets and no persisted checkout credentials.
- The version is stamped with `-ldflags "-X main.version=<v>"`; goreleaser sets it.
- CI also runs `goreleaser check` and a snapshot release build on every pull request; only the
  release workflow publishes, and only its job has write scopes and an OIDC token.
