---
id: tech
title: "lucy: technical specification"
status: active
version: 32
owner: ar4mirez
requirements: [LUCY-EVT-001, LUCY-EVT-002, LUCY-EVT-003, LUCY-EVT-004, LUCY-EVT-005, LUCY-EVT-006, LUCY-API-001, LUCY-API-002, LUCY-API-003, LUCY-API-004, LUCY-API-005, LUCY-API-006, LUCY-API-007, LUCY-API-008, LUCY-POL-001, LUCY-POL-002, LUCY-POL-003, LUCY-POL-004, LUCY-POL-005, LUCY-POL-006, LUCY-POL-007, LUCY-SPEC-004, LUCY-SPEC-009, LUCY-SIM-001, LUCY-SIM-002, LUCY-RUN-001, LUCY-RUN-002, LUCY-RUN-003, LUCY-RUN-013, LUCY-CRED-001, LUCY-OPS-001, LUCY-REPO-007, LUCY-SEC-002]
---

# lucy: technical specification

Status: m0 done; m1 has begun with the runner protocol (section 6). The skeleton, the release pipeline, the event store (section 3), the first part of the
API with `lucy daemon` (section 4), the policy engine (section 5), lucy's settings file (section
2), the spec model with the audit chain (section 10) and the flight state machine with its
simulation harness (section 11) and lucy's own security scans (section 9) exist; the rest
below is the design the m0-m3 slices build.

## 1. Shape

One Go binary ([D1](docs/decisions/D1-language-go.md)); every box is a mode of it:

| Mode | Role |
|---|---|
| `lucy daemon` | The Tower, the control plane: API, policy engine, event store, scheduler, supervisor loop, forge pollers, validation orchestrator |
| `lucy runner` | Owns agent processes on one machine or inside one org container; speaks the runner protocol |
| `lucy gateway` | Per-org egress credential gateway, a separate process outside the agents' container; the only reader of its org's vault |
| `lucy mcp` | MCP server for Claude chat; `--remote` forwards to a daemon over the tailnet |
| `lucy hook <event>` | Hook entrypoint for harnesses with only command hooks; not built yet (Claude Code hooks arrive as stream-json host callbacks: [harness](docs/tech/harness.md)) |
| CLI, TUI, `lucy attach` | Thin clients of the API |

```
 surfaces: CLI · TUI · web · Claude chat (MCP) · paired devices      (thin, one API)
        │ unix socket (local) · HTTPS/WebSocket on loopback or tailnet (remote)
 ┌──────▼──────────────────── lucy daemon (control plane) ───────────────────────┐
 │ API · policy engine · event store + hash-chained audit · scheduler            │
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
| Config | `$XDG_CONFIG_HOME/lucy/` | `config.toml` (lucy's settings, [EXTENSIONS.md](EXTENSIONS.md) section 3), spec resources in `resources/` (section 10), instruction overlays |
| Data | `$XDG_DATA_HOME/lucy/` | `lucy.db`, pools, materialized skills, pinned toolchain, the credential vault (`vault/`) |
| State | `$XDG_STATE_HOME/lucy/` | logs, pre-migration database snapshots, daily database backups |
| Runtime | `$XDG_RUNTIME_DIR/lucy/` | daemon and runner sockets |

- One SQLite database (pure Go, WAL), never a repo (LUCY-EVT-001). Files 0600, dirs 0700.
- Important state stays out of `~/.config`; everything lucy writes into other tools' config is
  recorded and re-derivable (`lucy preflight --repair`).
- Bundled instructions are plain Markdown embedded in the binary, viewable and overridable through
  the config overlay dir.
- The owner's display title and name come from `config.toml`'s `[you]` section (`internal/config`,
  default title "Owner"). Recorded data (events, decisions, API fields) holds the role id `owner`,
  and recorded text that names the owner, such as a decision's reason, says `{owner}`; surfaces
  render the title in its place only when they render text for the owner ([glossary](GLOSSARY.md)).

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
- **Audit chain.** `internal/audit` is a projector over these events: every event's audit entry is
  written in its append transaction (section 10, LUCY-SPEC-009).
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
  from it, committed and served by `rpc.discover` (LUCY-API-007). That table is the list of
  methods built so far, with their scopes: the daemon's own, the policy engine's (section 5),
  config and audit (section 10), the runner protocol, the Pilots' questions, the sandbox and the
  credential gateway (section 6).
- **Transport and identity.** `lucy daemon` listens on `$XDG_RUNTIME_DIR/lucy/lucy.sock` (0600 in a
  0700 dir, one daemon per dir by `flock`). Each connection's peer credentials (SO_PEERCRED on
  Linux, LOCAL_PEERCRED on macOS) must name the daemon's own uid, or it is closed unread; the
  accepted caller (uid, gid, pid, device `local`) rides on the request context (LUCY-API-003).
- **Authorization seam.** An `Authorizer` grants scopes (read, decide, steer, attach-view,
  attach-input, admin, run) and checks each call's scope before its params are decoded. Today only the
  local owner holds scopes; the policy engine adds org and task checks (LUCY-API-004).
- **Recording.** A mutating call is appended as an `api.command` event (method, request id, caller,
  params digest) before it runs, and does not run if the append fails (LUCY-API-005).
- **Limits** (LUCY-API-006): 64 KiB bodies, 32 levels of nesting, 8 KiB headers; 5 s to read
  headers, 15 s per request body, 30 s to write, 60 s idle, 10 s per call; 64 connections. Params
  are decoded strictly: no unknown, duplicate or case-folded members, bounds from struct tags.
- **Errors** (LUCY-API-008): JSON-RPC codes plus lucy's, -32001 to -32010 (forbidden, unsupported
  version, too large, unavailable, timeout, fenced, sandbox busy, runner org, host org, placement
  proof), one fixed message and `kind` per code; an internal error is logged; the caller sees only its correlation id.
- **Remote transport (m1).** The handler is transport-neutral; HTTPS needs only a TLS listener and a
  device authenticator. No network listener exists in m0: remote HTTPS with device pairing, scoped
  credentials and TLS lands in m1 (LUCY-VIEW-006).
- **Clients.** `internal/api/client` is the Go client: strict about the envelope, tolerant of new
  result fields. The CLI's `lucy status`, `lucy events` and `lucy daemon stop` use it.

## 5. Policy engine

"LLM proposes, policy disposes" (LUCY-POL-001): models and agents only request; deterministic Go in
`internal/policy` decides every privileged action, with default deny, and records each decision
before returning it. The request model, facts, rule order, decision record and path guard are in
[docs/tech/policy.md](docs/tech/policy.md).

## 6. Orgs, runners and sessions

- **Isolation per org:** harness config dir, GitHub token, git identity and key, repo allowlist,
  memory rows, merge rules, container. The daemon refuses any cross-org read or write.
- **Runner protocol** (LUCY-RUN-001), built in `internal/runner`: a runner polls `runner.queued`
  (a read), leases the oldest queued run with `runner.lease`, renews it with `runner.heartbeat` at a
  third of its life (60 s by default, 5 s to 5 min), streaming the run's new events in the same
  call, and ends it with `runner.finish`. Each call is an `api.command` plus `run.*` events on
  `run/<org>/<run>`; the `runner_runs` projection is the broker's whole state, so a daemon restart
  keeps every lease. The fence is the sequence number of the event that granted the lease, so
  fences only grow; a lease not renewed in time expires (the daemon sweeps every 5 s) and the run
  is lost, and any later call under that fence is refused `fenced`. Streamed events are numbered
  per run, so a batch resent after a lost reply is recorded once. `runner.Runtime` is the Tower's
  Runtime seam over the protocol: starting a Pilot queues run `<flight>#<generation>`.
  `lucy runner -- <command>` is this machine's runner: a process executor runs the command once per
  run in a fresh scratch directory (its TMPDIR), with only PATH, an org HOME, TMPDIR and `LUCY_RUN_*` in its
  environment, in its own process group, which is killed whenever the run ends or is stopped so no
  descendant outlives it, and deletes the directory through the path guard. The same binary runs in
  an org container (`--placement container`) once berth mounts the socket and per-runner
  credentials exist (m1 slices 3, 4); a cloud runner later speaks the same protocol.
- **Org sandbox** (LUCY-SBX-001 to LUCY-SBX-003), in `internal/sandbox`: the runtime seam, with
  berth as its first provider; nothing stops a container's work without a recorded clearance:
  [docs/tech/sandbox.md](docs/tech/sandbox.md).
- **Harness adapters** (LUCY-RUN-002, LUCY-RUN-003, LUCY-RUN-013), in `internal/harness`: an
  `Adapter` drives one harness over its structured protocol, pinned to one version; `claudecode`
  runs Claude Code over stream-json. Session limits, tool prompts, questions, hooks and the
  contract: [docs/tech/harness.md](docs/tech/harness.md).
- **Sessions:** structured protocols first (Claude Code's stream-json, built; Codex app-server; ACP
  later). Leases with generation and heartbeat; reattach before spawn; fence, then replace.
- **Worktree pool:** `git worktree` slots per org and repo, leased in the store, reset on
  acquire, freed only once the work landed ([worktree](docs/tech/worktree.md)); path guard.
- **Credentials:** the per-org gateway adds tokens only to allowlisted hosts and paths
  ([gateway](docs/tech/gateway.md)), from the org's encrypted [vault](docs/tech/vault.md);
  [capability tokens](docs/tech/capability.md).

## 7. External dependencies

Every external is a managed risk ([D8](docs/decisions/D8-built-in-vs-external.md)); `lucy preflight`
installs through mise from a pinned manifest and `lucy preflight --check` checks each row
(LUCY-OPS-001).

| Component | Kind | Install or check | Main risk and mitigation |
|---|---|---|---|
| git | hard | checked, minimum version | missing worktree features: preflight refuses below the minimum |
| Container runtime (Docker, Podman, Apple `container`) | hard | checked | daemon down: preflight names the engine in use |
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
- `github.com/santhosh-tekuri/jsonschema/v6`: the draft 2020-12 validator for spec resources
  (LUCY-SPEC-004, [D11](docs/decisions/D11-spec-loading.md)); it needs `golang.org/x/text` for
  its messages.
- `go.yaml.in/yaml/v3`, the maintained continuation of `gopkg.in/yaml.v3`: the YAML parser behind
  config loading, read only as a node tree (section 10), and the workflow test
  (`internal/workflowcheck`), which parses the workflows so their permission and pinning rules, a
  security baseline (LUCY-REL, checklist area 5), do not pass or fail on formatting.

## 8. Repository layout

| Path | Contents |
|---|---|
| `cmd/lucy/` | The binary's entrypoint: version, help, self-update, `daemon`, `status`, `events`, `config`, `audit`, `runner`, `questions`, `answer`, `sandbox`, `gateway`, `vault`, `capability` |
| `internal/api/` | The API's method table, wire format, errors and generated OpenRPC schema (section 4) |
| `internal/api/client/` | The Go client of the API |
| `internal/server/` | The API's HTTP handler, peer-credential listener and authorizer seam |
| `internal/daemon/` | `lucy daemon`: runtime dir, lock, socket, store, graceful stop |
| `internal/config/` | `config.toml`: lucy's settings, and rendering the owner's display title |
| `internal/skills/` | lucy's bundled skills and their SHA-256 manifest, installed and verified in an org's home before every spawn (LUCY-RUN-009) |
| `internal/spec/` | Spec resources: vendored and lucy schemas, safe YAML decoding, loading, admission, status (section 10) |
| `internal/jcs/` | RFC 8785 canonical JSON |
| `internal/audit/` | The hash-chained audit log: entries, projector, verification (section 10) |
| `internal/` | All packages; nothing is a public Go API yet |
| `internal/doccheck/` | Doc-rules checker and test |
| `internal/eventstore/` | The SQLite event store: event log, projections, consumers, migrations (section 3) |
| `internal/pathguard/` | The path guard every delete passes (section 5) |
| `internal/pipeline/` | the ship and research Pipelines as bundled ai-sdlc Pipelines, an org's replacements, read into policy (LUCY-SPEC-014) |
| `internal/backlog/` | a Flight as the spec's BacklogTask document, checked against the vendored schema (LUCY-SPEC-017) |
| `internal/role/` | lucy's roles as bundled ai-sdlc AgentRoles, an org's additions, their blocked paths (LUCY-SPEC-015) |
| `internal/policy/` | The policy engine: request model, rules, rule-set hash, decision record (section 5) |
| `internal/runner/` | The runner protocol: broker and its projection, the Tower's Runtime over it, the runner loop, the process executor (section 6) |
| `internal/gateway/` | The credential gateway (section 6) |
| `internal/vault/`, `internal/capability/` | The vault and capability tokens (section 6) |
| `internal/sandbox/` | The runtime seam: providers, the guard, berth's client, a fake berth (section 6) |
| `internal/harness/` | The harness adapter seam; `claudecode/` is the Claude Code adapter and its recorded transcripts, `harnesstest/` replays them (section 6) |
| `internal/tower/` | The flight state machine and its seams: Pilot runtime, forge, clock (section 11) |
| `internal/sim/` | The simulation harness: virtual clock, fake forge, fake Pilot runtime, fault hooks (section 11) |
| `internal/converge/` | converge loops: level-triggered reconcilers woken by the event log, with resync and backoff (LUCY-SPEC-008) |
| `internal/corpus/` | The conformance corpus: scenario format, runner, corpus v1 (section 11) |
| `internal/selfupdate/` | `lucy self-update`, which updates lucy through mise |
| `internal/workflowcheck/` | Test enforcing the GitHub Actions rules: SHA pins, permissions, release trigger |
| `internal/seccheck/` | Test keeping the gosec suppressions and the reviewed SAST baseline in step, and the scanners pinned |
| `scripts/` | `verify-dist.sh`: checks a goreleaser `dist/` (assets, SBOMs, checksums) |
| `.goreleaser.yaml` | The release build ([release runbook](docs/runbooks/release.md)) |
| `docs/` | PRD, milestones, decisions, runbooks, security, changes, requirements index |
| `.github/` | CI, security scans, Scorecard, the tag-triggered release workflow, Dependabot |

## 9. Build and CI

- Go is pinned in `go.mod` and `mise.toml`; release builds are `CGO_ENABLED=0` for linux and
  darwin, amd64 and arm64, made by goreleaser on a `v*` tag ([release runbook](docs/runbooks/release.md)).
- `mise run check` = gofmt check, `go vet`, `go test -race`, which includes the doc rules.
- CI runs the same on `ubuntu-latest` and `macos-latest` (LUCY-REPO-007) with actions pinned by SHA,
  `permissions: contents: read`, no secrets and no persisted checkout credentials.
- The version is stamped with `-ldflags "-X main.version=<v>"`; goreleaser sets it.
- CI also runs `goreleaser check` and a snapshot release build on every pull request; only the
  release workflow publishes, and only its job has an OIDC token.
- `security.yml` runs lucy's own scans on every pull request, push to `main` and weekly:
  govulncheck (Linux and macOS), gosec with a reviewed baseline, gitleaks over the new commits and
  dependency review with a licence allowlist; each fails on a finding. `scorecard.yml` reports
  OpenSSF Scorecard to code scanning; its job and the release job are the only ones with write
  scopes. `mise run security` runs the same scanners locally (LUCY-SEC-002,
  [security scans](docs/runbooks/security-scans.md)).
- `harness-check.yml` checks the newest Claude Code release nightly against the adapter's flags,
  with no model, login or secret, and reports it ready or held without failing (LUCY-RUN-003).
- `install.yml` installs each published release through mise in a clean HOME on Linux and macOS,
  attestation checks forced on; the release workflow calls it, and it runs on demand.

## 10. Spec model and audit log

Config is ai-sdlc resources, validated against vendored schemas and admitted through one safe
loader; status lives only in the event log, and every event is chained into a hash-linked audit
log. Resources, schemas, loading, status and the audit chain are in
[docs/tech/spec.md](docs/tech/spec.md).

## 11. Flights and the simulation harness

`internal/tower` flies a flight (Pilot session, pull request, Clearance, then Takeoff, outside or
grounded) through three seams: `Runtime` (Pilot sessions), `Forge` (pull requests; a merge names
the cleared head) and `Clock`. It never sleeps; a driver calls `Step`. Each transition is a
`task.*` event on `task/<org>/<id>` and state is their projection, so a new Tower on the store
resumes. A merge's `policy.decision` and `task.clearance` events share one transaction; an
approval binds to the head and base shown. `internal/sim` fakes the seams on virtual time with one
seeded generator; a test holds a seed to one log, restarts or not (LUCY-SIM-001, LUCY-SIM-002).
`internal/corpus` runs the conformance corpus on it ([coverage](docs/security/conformance.md)).
