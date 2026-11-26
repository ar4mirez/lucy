---
id: requirements-index
title: Requirements index
status: active
version: 1
owner: ar4mirez
requirements: []
generated: true
---

# Requirements index

Generated from the requirement tables in [docs/prd](prd/README.md). Do not edit by hand:
run `mise run docs-index` after changing a PRD table. CI fails when this file is stale.

136 requirements (m0: 53, m1: 56, m2: 13, m3: 8, m4: 6). Milestone plans: [docs/milestones](milestones/README.md).

## [foundation](prd/foundation.md)

| ID | Milestone | Requirement |
|---|---|---|
| LUCY-API-001 | m0 | One typed API on a unix socket serves every surface; surfaces hold no logic |
| LUCY-API-002 | m0 | The API offers hello, query, subscribe from a sequence, attach, input and command |
| LUCY-API-003 | m0 | The socket is 0600 in the user's runtime dir and checks the peer's credentials |
| LUCY-API-004 | m0 | Every call checks the caller's scope and the object's org and task through the policy engine |
| LUCY-API-005 | m0 | Every command becomes an audit entry naming the actor and device |
| LUCY-API-006 | m0 | Requests are strict typed JSON: unknown fields rejected, sizes bounded, no native deserialization |
| LUCY-API-007 | m0 | The API is versioned (major version in the path, negotiated by hello) and its schema is generated from one method table and committed |
| LUCY-API-008 | m0 | API errors are typed: one fixed message per code, details only about the caller's input, internal error text never sent |
| LUCY-EVT-001 | m0 | State lives in one pure-Go SQLite database (WAL) under the XDG data dir, never in a repo |
| LUCY-EVT-002 | m0 | Typed events are appended in the same transaction as the state change they describe |
| LUCY-EVT-003 | m0 | Consumers read past a cursor; an ack carries its generation and is refused if the generation changed |
| LUCY-EVT-004 | m0 | Schema changes ship as versioned forward migrations, tested on a copy of the previous schema |
| LUCY-EVT-005 | m0 | Events never carry secrets: secret-looking field names are refused, payloads are size-capped and SQL is parameterized |
| LUCY-EVT-006 | m0 | The append-only log is the source of truth: per-stream optimistic concurrency, and state tables are projections rebuilt by replay |
| LUCY-POL-001 | m0 | Deterministic Go policy evaluates every privileged action (merge, destructive op, credential use, cross-org access); default deny |
| LUCY-POL-002 | m0 | Models can only request; only decision records made through the API authorize anything |
| LUCY-POL-003 | m0 | Every allowed action records the rule, actor and decision that allowed it |
| LUCY-POL-004 | m0 | Outside text is tagged untrusted with its source and can never raise privileges |
| LUCY-POL-005 | m0 | Merge postures (owner, green with exceptions, client-confirm) are typed data per org and project |
| LUCY-POL-006 | m0 | The engine is pure and fail-safe: a decision is cleared, denied or holding with its rule and reason, any evaluation error denies, and the versioned rule set's hash is recorded with every decision |
| LUCY-POL-007 | m0 | The destructive-op rule uses a path guard that refuses `/`, `$HOME` and anything outside the declared project and scratch roots after symlink resolution |
| LUCY-REL-001 | m0 | goreleaser builds static binaries (CGO off) for linux and darwin, amd64 and arm64, with checksums |
| LUCY-REL-002 | m0 | A keyless cosign signature bundle covers checksums.txt |
| LUCY-REL-003 | m0 | Each archive carries a GitHub artifact attestation (SLSA build provenance) |
| LUCY-REL-004 | m0 | `mise use -g github:ar4mirez/lucy` installs a release and mise verifies its attestation |
| LUCY-REL-005 | m0 | Merges to main cut releases from conventional commits; tags trigger goreleaser |
| LUCY-REL-006 | m0 | `lucy self-update` verifies the new binary and `--to <version>` rolls back |
| LUCY-REL-007 | m3 | A Homebrew cask ships in ar4mirez/homebrew-tap |
| LUCY-REL-008 | m0 | Each release archive ships an SPDX SBOM, covered by the signed checksums.txt |
| LUCY-REPO-001 | m0 | The repository is Apache-2.0, with a NOTICE file and README acknowledgements |
| LUCY-REPO-002 | m0 | Every doc carries front matter: id, title, status, version, owner, requirements |
| LUCY-REPO-003 | m0 | Requirement IDs are stable, defined once, scheduled in one milestone and indexed in docs/requirements.md |
| LUCY-REPO-004 | m0 | No doc exceeds 400 lines or 24 KB; docs split by area instead of growing |
| LUCY-REPO-005 | m0 | CHANGES.md keeps an Unreleased section, rotated into docs/changes/ per version |
| LUCY-REPO-006 | m0 | The README states what lucy will never do (non-goals) |
| LUCY-REPO-007 | m0 | CI on Linux and macOS runs go vet, a gofmt check, race tests and the doc rules |
| LUCY-REPO-008 | m0 | CI actions are pinned by commit SHA, run with read-only permissions and no secrets; Dependabot watches them |

## [operations](prd/operations.md)

| ID | Milestone | Requirement |
|---|---|---|
| LUCY-BRD-001 | m4 | More harnesses (Codex, ACP harnesses) behind the same adapter contract |
| LUCY-BRD-002 | m4 | An Agent Substrate runner on the runner protocol |
| LUCY-BRD-003 | m4 | ai-memory as an optional, per-org memory provider |
| LUCY-BRD-004 | m4 | `lucy init` scaffolds this doc structure (full or lite profile) in a project |
| LUCY-OPS-001 | m1 | `lucy preflight` plans first, then installs every external tool from an embedded, pinned manifest through mise in lucy's own space |
| LUCY-OPS-002 | m1 | `lucy preflight --check` checks every inventory row, the incident classes and the security invariants, reports conditions and repairs with `--repair` |
| LUCY-OPS-003 | m1 | lucy takes a daily online database backup into the state dir; `lucy backup --to` copies it off the machine |
| LUCY-OPS-004 | m1 | OpenTelemetry traces per task, a flight recorder per session and `lucy replay` |
| LUCY-OPS-005 | m1 | The daemon restarts and upgrades without stopping runners or their sessions |
| LUCY-OPS-006 | m3 | Per-org feature flags, versioned prompts with rollback and restore drills |
| LUCY-ORG-001 | m2 | Multi-org supervisors with per-org credentials; a cross-org read is refused and audited |
| LUCY-ORG-002 | m2 | Seat pools, quota-aware placement, twin config dirs, failover and return, hard per-org budget stops |
| LUCY-ORG-003 | m2 | A resource-aware scheduler: memory caps per runner, per-org concurrency, priorities, preemption |
| LUCY-ORG-004 | m2 | Typed decisions, preferences and learnings per org, with provenance and expiry, recalled per task |
| LUCY-ORG-005 | m2 | `lucy import` carries history, decisions and postures over from the existing supervisor, with a dry run first |
| LUCY-ORG-006 | m2 | The client-confirm posture needs the owner's relayed decision through the API |
| LUCY-ORG-007 | m2 | An org cuts over only after 20 consecutive tasks on lucy with no lost work |

## [quality](prd/quality.md)

| ID | Milestone | Requirement |
|---|---|---|
| LUCY-CORP-001 | m0 | A corpus harness runs executable scenarios against lucy in the simulation |
| LUCY-CORP-002 | m0 | Corpus v1 encodes every safety rule and recorded incident of the existing supervisor; red is expected in m0 |
| LUCY-CORP-003 | m1 | The corpus covers ar4mirez's flows and is green before ar4mirez moves |
| LUCY-CORP-004 | m2 | The corpus covers each client org's rules and is green before that org moves |
| LUCY-SIM-001 | m0 | A fake harness, fake forge and fake clock drive the supervisor state machine deterministically |
| LUCY-SIM-002 | m0 | The simulation suite shows the policy engine refusing every privileged action no rule allows |
| LUCY-SIM-003 | m1 | A chaos suite kills the daemon or runner mid-merge, fills the disk, skews the clock, upgrades the harness, tries rm -rf and exhausts quota; kill-chain scenarios included |
| LUCY-VAL-001 | m1 | Build, test and lint run as plain processes with failures-only output (`lucy check`); no agent drives them |
| LUCY-VAL-002 | m1 | Fixes run in the implementing worker's warm session, at most two rounds, then a Decision |
| LUCY-VAL-003 | m1 | Review is diff-scoped from a context pack, by a different model family than the implementer, or recorded as same-family |
| LUCY-VAL-004 | m1 | The docs check runs only when the diff touches a documented surface, on a cheap model |
| LUCY-VAL-005 | m1 | Push happens only after every hard gate passed on the exact tree; CI is watched with zero tokens |
| LUCY-VAL-006 | m1 | Stage results are cached by tree hash and stage config hash |
| LUCY-VAL-007 | m1 | Each PR gets a sigstore-signed proof bundle, verified before merge; only the proof's head commit merges |
| LUCY-VAL-008 | m1 | Before replacing anything, the pipeline replays past PRs and matches no-mistakes' blocking catches |
| LUCY-VAL-009 | m1 | One merge function: never red, never with a required check unreported, closing references checked |
| LUCY-VAL-010 | m1 | Orgs may import the no-mistakes skill as an optional, pinned, hash-verified skill |

## [runtime](prd/runtime.md)

| ID | Milestone | Requirement |
|---|---|---|
| LUCY-CRED-001 | m1 | A per-org gateway process outside the agents' container adds tokens only to allowlisted hosts and paths, never on redirects |
| LUCY-CRED-002 | m1 | Credentials are encrypted at rest with a key per org |
| LUCY-CRED-003 | m1 | Capability tokens are scoped to (org, task, action, expiry) |
| LUCY-CRED-004 | m1 | The gateway denies private, link-local and metadata addresses, fails closed and logs every request to the audit chain |
| LUCY-CRED-005 | m1 | No token is ever inside a container; `lucy preflight --check` scans environments and mounts to prove it |
| LUCY-CRED-006 | m2 | Offboarding an org provably erases its data and rotates its credentials |
| LUCY-RUN-001 | m1 | A runner protocol (lease task, stream events, heartbeat, fencing token); this machine is the first runner |
| LUCY-RUN-002 | m1 | Claude Code runs over its structured protocol; a PTY is only a viewing path or a named, quarantined adapter |
| LUCY-RUN-003 | m1 | Harness adapters are pinned; nightly contract tests run against the latest harness; failing upgrades are held |
| LUCY-RUN-004 | m1 | Sessions hold a lease with generation and heartbeat; reattach before spawn; fence, then replace; never two live sessions per role |
| LUCY-RUN-005 | m1 | Every session survives all its viewers closing and the daemon restarting |
| LUCY-RUN-006 | m1 | Every task gets its own TMPDIR; every org's sessions get a synthetic HOME |
| LUCY-RUN-007 | m1 | Built-in worktree pool: slots leased in SQLite, reset on acquire, released only with a landed-work proof |
| LUCY-RUN-008 | m1 | Account state (onboarding, trust, hooks) is re-asserted before every spawn; a task needing a human is held, not wedged |
| LUCY-RUN-009 | m1 | Bundled skills carry content hashes and are re-linked and verified at every spawn |
| LUCY-RUN-010 | m1 | Supervision routes events through rules, then a small model, then a large one; supervisor tokens per org-day are tracked |
| LUCY-RUN-011 | m1 | Token cost is recorded per task and per step |
| LUCY-RUN-012 | m1 | Shadow mode, then takeover by task class; `lucy handback` returns a class to the existing supervisor in one command |
| LUCY-SBX-001 | m1 | Every worker and validation agent runs in its org's berth container through berth's API; idle orgs are stopped |
| LUCY-SBX-002 | m1 | The runtime is a provider seam, so a cloud runner can be added without changing the core |
| LUCY-SBX-003 | m1 | lucy never restarts or recreates an org container with live sessions without the owner's confirmation |

## [security](prd/security.md)

| ID | Milestone | Requirement |
|---|---|---|
| LUCY-SEC-001 | m0 | lucy runs as the user, never root; state files are 0600 and dirs 0700 |
| LUCY-SEC-002 | m0 | CI runs vulnerability and dependency scanning on lucy's own code and blocks on findings |
| LUCY-SEC-003 | m0 | The STRIDE threat model is kept as an ADR and re-checked at every milestone exit |
| LUCY-SEC-004 | m0 | Vulnerabilities are reported through GitHub private advisories, as SECURITY.md says |
| LUCY-SEC-005 | m1 | Every delete lucy makes refuses `/`, `$HOME` and anything outside lucy's managed roots |
| LUCY-SEC-006 | m1 | API rate limits per device and pairing-attempt limits |
| LUCY-SEC-007 | m1 | `lucy preflight --check` checks a host baseline: disk encryption, firewall, `/home` snapshots, OS updates |
| LUCY-SEC-008 | m1 | Review applies a security lens to security-sensitive diffs; project SAST and dependency scans are hard gates |
| LUCY-SEC-009 | m2 | Deterministic detection rules over events: token patterns, unexpected egress, repeated denials, cross-org attempts, new devices |
| LUCY-SEC-010 | m2 | `lucy incident` freezes an org, exports evidence and turns each lesson into a corpus scenario |
| LUCY-SEC-011 | m2 | Key rotation and encrypted per-org backups |
| LUCY-SEC-012 | m3 | Token-reuse anomaly checks for remote sessions |
| LUCY-SEC-013 | m4 | DAST per project, and Kubernetes RBAC with workload identity for the Agent Substrate runner |

## [spec](prd/spec.md)

| ID | Milestone | Requirement |
|---|---|---|
| LUCY-SPEC-001 | m0 | Config uses the resource envelope (apiVersion, kind, metadata, spec, status); core kinds stay `ai-sdlc.io/v1alpha1` |
| LUCY-SPEC-002 | m0 | `metadata.namespace` is required and names the org |
| LUCY-SPEC-003 | m0 | Config files hold spec; the database holds status and never writes back into config |
| LUCY-SPEC-004 | m0 | The spec's JSON Schemas are vendored at the pinned commit; config is validated on load (2020-12, unknown fields rejected) |
| LUCY-SPEC-005 | m0 | Spec resources map through an internal model, so a spec bump never breaks an org |
| LUCY-SPEC-006 | m0 | lucy kinds live under lucy's own apiVersion (`lucy/v1alpha1`) with schemas: Airspace, Runway, Flight and Pilot first, Account, Session and the client-confirm route later |
| LUCY-SPEC-007 | m0 | Status is reported as conditions (True, False, Unknown with a reason) on every surface |
| LUCY-SPEC-008 | m1 | Converge loops are level-triggered, idempotent reconcilers with backoff, woken by the event log |
| LUCY-SPEC-009 | m0 | The audit log is a hash chain (RFC 8785 + SHA-256) that passes the spec's cross-SDK fixture |
| LUCY-SPEC-010 | m1 | QualityGate and AutonomyPolicy are modelled and validated; the owner is the only override role |
| LUCY-SPEC-011 | m0 | The vendored schemas validate the spec's own examples in CI |
| LUCY-SPEC-012 | m1 | Dispatch is refused until a task has a goal, delivery path, autonomy and acceptance test (readiness check) |
| LUCY-SPEC-013 | m1 | Decisions waiting on the owner are Decision resources projected from the event log; only the owner's words close them |
| LUCY-SPEC-014 | m1 | Research and ship pipelines use FailurePolicy, CredentialPolicy, ApprovalPolicy and PullRequest config; a timeout never approves a merge |
| LUCY-SPEC-015 | m1 | AgentRole covers supervisor, ship worker, researcher and reviewer; blockedPaths feed the delete guard |
| LUCY-SPEC-016 | m1 | AdapterBinding is the provider seam: berth is Sandbox, the gateway is SecretStore, lucy's pipeline is CodeAnalysis |
| LUCY-SPEC-017 | m1 | lucy passes the spec's Core conformance level; worktree pool and backlog use the spec's schemas |
| LUCY-SPEC-018 | m2 | AgentRole handoffs, demotion triggers and Adapter conformance |
| LUCY-SPEC-019 | m3 | lucy proposes autonomy promotions from metrics; only the owner grants one; spec fixes go upstream |
| LUCY-SPEC-020 | m4 | Deferred concepts (Workflow document, complexity routing, priority policy, agentCard, session hierarchy) are priced one by one |

## [surfaces](prd/surfaces.md)

| ID | Milestone | Requirement |
|---|---|---|
| LUCY-OWN-001 | m3 | A Claude Code plugin (MCP server, one skill, a `/lucy` command) lets the owner work from any Claude Code session with no folder |
| LUCY-OWN-002 | m3 | The full dashboard has structured decision cards and push notifications |
| LUCY-OWN-003 | m3 | Privileged remote actions (merge, credentials, destructive) need a passkey step-up |
| LUCY-OWN-004 | m3 | The existing supervisor's homes are archived read-only once every org has moved |
| LUCY-VIEW-001 | m1 | `lucy attach` shows a live session in any terminal, with a detach key |
| LUCY-VIEW-002 | m1 | A bubbletea TUI shows orgs, tasks, sessions, decisions and events, and hands off to `lucy attach` |
| LUCY-VIEW-003 | m1 | herdr panes run `lucy attach`; a herdr crash is only a viewer crash |
| LUCY-VIEW-004 | m1 | The MCP server for Claude chat exposes about nine tools in under 2k tokens |
| LUCY-VIEW-005 | m1 | A phone-first web page shows status and decisions, and steers and attaches |
| LUCY-VIEW-006 | m1 | Remote use binds loopback and tailnet only, over TLS; devices pair with one-time codes and hashed, revocable, scoped credentials |
| LUCY-VIEW-007 | m1 | Dashboard sessions use CSP, output encoding, CSRF tokens and HttpOnly, Secure, SameSite=Strict cookies |
