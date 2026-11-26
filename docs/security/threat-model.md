---
id: threat-model
title: STRIDE threat model
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-SEC-003, LUCY-API-003, LUCY-API-005, LUCY-POL-001, LUCY-POL-002, LUCY-POL-004, LUCY-SPEC-009, LUCY-CRED-001, LUCY-CRED-003, LUCY-CRED-004, LUCY-VIEW-006, LUCY-VIEW-007, LUCY-VAL-007, LUCY-SEC-006]
---

# STRIDE threat model

Seven trust boundaries, six STRIDE rows each. "Lands" is the milestone whose exit checks the row; the
whole model is re-checked at every milestone exit (LUCY-SEC-003). The credential gateway runs as a
per-org sidecar outside the agents' container, so agents never share memory or files with tokens.
TODO(LUCY-SEC-003): record this model as an ADR in m0 slice 9.

## 1. Captain surfaces to the daemon

CLI, TUI, web dashboard, Claude chat (MCP), paired devices.

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | Stolen device credential or forged pairing | One-time, short-lived, attempt-limited codes; hashed, revocable device credentials; passkey step-up for privileged actions | m1; step-up m3 |
| T | Tampering in transit | TLS on every remote connection; loopback and tailnet binds only (LUCY-VIEW-006) | m1 |
| R | "I never approved that merge" | Every command is an audit entry naming the device; the captain's exact words are stored (LUCY-API-005) | m0-m1 |
| I | Dashboard XSS; chat transcripts leaking state | CSP and output encoding (LUCY-VIEW-007); MCP returns scoped summaries; secrets never cross the API | m1 |
| D | A compromised device floods the API | Per-device rate limits (LUCY-SEC-006); revoke the device | m1 |
| E | A read-only device decides or merges; CSRF | Scopes checked server-side on every call; SameSite=Strict and CSRF tokens; object-level checks | m0-m1 |

## 2. The daemon (control plane)

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | A local process impersonates the CLI on the socket | Socket 0600 in the user's runtime dir; peer-credential check (LUCY-API-003) | m0 |
| T | Editing the database or audit log on disk | Hash-chained audit verified by `lucy doctor` (LUCY-SPEC-009); files 0600; signed backups | m0-m2 |
| R | Privileged actions without attribution | Every allowed action records actor, rule and decision (LUCY-POL-001) | m0 |
| I | One process holding every org's secrets | Per-org keys; the gateway is a separate process per org; secrets never enter a model's context | m1 |
| D | Event storms, a full disk, runaway reconcilers | Rate-limited reconcilers with backoff; disk checks; chaos suite | m0-m1 |
| E | A model talks the supervisor into a privileged action | LLM proposes, policy disposes; outside text never raises privileges (LUCY-POL-004) | m0 |

## 3. Runners and berth containers

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | A rogue process poses as a runner | Runner capability tokens bound to org and lease; fencing tokens | m1 |
| T | An agent tampers with the runner, instructions or image | Agents run as a separate user; instructions read-only; skills hashed; images pinned | m1 |
| R | Not knowing which agent did what | Per-session event log and flight recorder | m1 |
| I | Cross-org leakage through shared paths | A container per org, no shared mounts, data dirs 0700 | m1 |
| D | One org's runaway agent starves the machine | Memory caps per runner, concurrency limits, preemption | m2 |
| E | Container escape | berth's container and host guard; no engine socket inside; microVMs with Agent Substrate later | m1; m4 |

## 4. Credential gateway

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | An agent forges another task's identity to get its token | Requests authenticated with the runner capability; tokens scoped to (org, task, action, expiry) (LUCY-CRED-003) | m1 |
| T | A request redirected after the token is added | Tokens only for allowlisted hosts and paths; no redirects with credentials (LUCY-CRED-001) | m1 |
| R | Unknown token use | Per-request egress log into the audit chain (LUCY-CRED-004) | m1 |
| I | Token exfiltration, for example in a PR comment | The token never enters the container; echoed tokens scrubbed; a detection rule for token patterns | m1; rule m2 |
| D | The gateway is down | Health checks and restart; it fails closed (no direct egress) | m1 |
| E | SSRF to internal services or the metadata address | Private, link-local and metadata ranges denied (LUCY-CRED-004) | m1 |

## 5. Agents (untrusted executors)

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | An agent claims the captain approved something | Only decision records made through the API count (LUCY-POL-002) | m0 |
| T | Writes outside the worktree; edits to its own hooks | Container and worktree scope; read-only instructions; hashed skills; delete guards | m1 |
| R | Disputed output | Transcripts and tool calls in the flight recorder, hashed into the PR's proof (LUCY-VAL-007) | m1 |
| I | Repo or client data sent to other hosts | Per-org egress allowlist; model provider per org policy | m1 |
| D | Runaway token spend or loops | Per-task budgets with hard stops; bounded fix rounds | m1-m2 |
| E | A prompt-injected agent tries to merge, push or use credentials | Policy engine; agents hold no credentials; pushes go through the gateway under branch rules | m0-m1 |

## 6. Forge (GitHub)

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | Forged webhook, or a fork PR posing as trusted | Webhook signatures; allowlisted repos and actors; the authenticated API is the source of truth | m1 |
| T | Force-push replaces the reviewed head before merge | Merge only the head commit named in the signed proof (LUCY-VAL-007) | m1 |
| R | Forge actions without attribution | Audit chain plus a provenance section in each PR | m1 |
| I | Secrets or client details posted publicly | PR text lint for secrets and `@` mentions; a public-repo check before posting | m1 |
| D | Outages or rate limits | Backoff and conditional requests; merges wait, never bypass checks | m1 |
| E | An over-privileged token bypasses branch protection | Fine-grained per-org tokens without admin; never an admin merge; rotation | m1; rotation m2 |

## 7. Client content

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | An issue or email claims "the client confirmed, merge" | Client-confirm needs the captain's relayed decision through the API | m1-m2 |
| T | Content poisons tests or build scripts | Cross-family review, deterministic tests in the container, delete guards, the sandbox | m1 |
| R | No record of which content influenced a decision | Untrusted-text tags record their source in events | m1 |
| I | One client's content reaching another org | Org isolation; org-scoped memory; no cross-org recall | m1; memory m2 |
| D | Huge or adversarial inputs | Input size limits; budgets | m1 |
| E | Prompt injection leading to privileged actions | Outside text never raises privileges; agents hold no credentials | m0-m1 |
