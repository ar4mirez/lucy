---
id: D12
title: "D12: lucy's STRIDE threat model"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-SEC-003, LUCY-SEC-002, LUCY-API-003, LUCY-API-004, LUCY-API-005, LUCY-API-006, LUCY-POL-001, LUCY-POL-002, LUCY-POL-003, LUCY-POL-004, LUCY-SPEC-009, LUCY-SIM-002, LUCY-REL-002, LUCY-REL-003, LUCY-REL-004, LUCY-REL-008, LUCY-REPO-008, LUCY-CRED-001, LUCY-CRED-003, LUCY-CRED-004, LUCY-VIEW-006, LUCY-VIEW-007, LUCY-VAL-007, LUCY-SEC-006]
decided: 2026-11-26
---

# D12: lucy's STRIDE threat model

## Context

lucy runs untrusted agents for one owner across isolated orgs, so its design starts from a threat
model: STRIDE (spoofing, tampering, repudiation, information disclosure, denial of service,
elevation of privilege) over each trust boundary, every threat with a mitigation and the milestone
whose exit checks it. LUCY-SEC-003 keeps that model as an ADR and re-checks it at every milestone
exit. The model was drafted with the architecture ([R1](R1-adversarial-review.md),
[SECURITY.md](../../SECURITY.md)) as a working document under `docs/security`; recording it as a
decision was delegated to m0 slice 9, the security baseline, and the owner can revise it.

## Decision

1. **Eight trust boundaries.** The seven of the draft, plus lucy's own build and release pipeline
   (section 8): the scans m0 slice 9 adds answer threats to it that no other boundary covered.
2. **Six STRIDE rows per boundary**, each with its mitigation and the milestone that lands it
   ("Lands"). A row split across milestones names each part. The credential gateway runs as a
   per-org sidecar outside the agents' container, so agents never share memory or files with
   tokens.
3. **Re-checked at every milestone exit.** Each row the milestone lands is checked against what was
   built, with the requirement or test that shows it; the result is a new version of this record
   with a Revisions entry naming the milestone. A row that slips names its new milestone and why.
   Nothing is closed by a calendar date.

## The model

### 1. Owner surfaces to the daemon

CLI, TUI, web dashboard, Claude chat (MCP), paired devices.

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | Stolen device credential or forged pairing | One-time, short-lived, attempt-limited codes; hashed, revocable device credentials; passkey step-up for privileged actions | m1; step-up m3 |
| T | Tampering in transit | TLS on every remote connection; loopback and tailnet binds only (LUCY-VIEW-006) | m1 |
| R | "I never approved that merge" | Every command is an audit entry naming the device; the owner's exact words are stored (LUCY-API-005) | m0-m1 |
| I | Dashboard XSS; chat transcripts leaking state | CSP and output encoding (LUCY-VIEW-007); MCP returns scoped summaries; secrets never cross the API | m1 |
| D | A compromised device floods the API | Per-device rate limits (LUCY-SEC-006); revoke the device | m1 |
| E | A read-only device decides or merges; CSRF | Scopes checked server-side on every call; SameSite=Strict and CSRF tokens; object-level checks | m0-m1 |

### 2. The daemon (control plane)

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | A local process impersonates the CLI on the socket | Socket 0600 in the user's runtime dir; peer-credential check (LUCY-API-003) | m0 |
| T | Editing the database or audit log on disk | Hash-chained audit verified by `lucy preflight --check` (LUCY-SPEC-009); files 0600; signed backups | m0-m2 |
| R | Privileged actions without attribution | Every allowed action records actor, rule and decision (LUCY-POL-001) | m0 |
| I | One process holding every org's secrets | Per-org keys; the gateway is a separate process per org; secrets never enter a model's context | m1 |
| D | Event storms, a full disk, runaway reconcilers | Rate-limited reconcilers with backoff; disk checks; chaos suite | m0-m1 |
| E | A model talks the supervisor into a privileged action | LLM proposes, policy disposes; outside text never raises privileges (LUCY-POL-004) | m0 |

### 3. Runners and berth containers

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | A rogue process poses as a runner | Runner capability tokens bound to org and lease; fencing tokens | m1 |
| T | An agent tampers with the runner, instructions or image | Agents run as a separate user; instructions read-only; skills hashed; images pinned | m1 |
| R | Not knowing which agent did what | Per-session event log and flight recorder | m1 |
| I | Cross-org leakage through shared paths | A container per org, no shared mounts, data dirs 0700 | m1 |
| D | One org's runaway agent starves the machine | Memory caps per runner, concurrency limits, preemption | m2 |
| E | Container escape | berth's container and host guard; no engine socket inside; microVMs with Agent Substrate later | m1; m4 |

### 4. Credential gateway

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | An agent forges another task's identity to get its token | Requests authenticated with the runner capability; tokens scoped to (org, task, action, expiry) (LUCY-CRED-003) | m1 |
| T | A request redirected after the token is added | Tokens only for allowlisted hosts and paths; no redirects with credentials (LUCY-CRED-001) | m1 |
| R | Unknown token use | Per-request egress log into the audit chain (LUCY-CRED-004) | m1 |
| I | Token exfiltration, for example in a PR comment | The token never enters the container; echoed tokens scrubbed; a detection rule for token patterns | m1; rule m2 |
| D | The gateway is down | Health checks and restart; it fails closed (no direct egress) | m1 |
| E | SSRF to internal services or the metadata address | Private, link-local and metadata ranges denied (LUCY-CRED-004) | m1 |

### 5. Agents (untrusted executors)

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | An agent claims the owner approved something | Only decision records made through the API count (LUCY-POL-002) | m0 |
| T | Writes outside the worktree; edits to its own hooks | Container and worktree scope; read-only instructions; hashed skills; delete guards | m1 |
| R | Disputed output | Transcripts and tool calls in the flight recorder, hashed into the PR's proof (LUCY-VAL-007) | m1 |
| I | Repo or client data sent to other hosts | Per-org egress allowlist; model provider per org policy | m1 |
| D | Runaway token spend or loops | Per-task budgets with hard stops; bounded fix rounds | m1-m2 |
| E | A prompt-injected agent tries to merge, push or use credentials | Policy engine; agents hold no credentials; pushes go through the gateway under branch rules | m0-m1 |

### 6. Forge (GitHub)

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | Forged webhook, or a fork PR posing as trusted | Webhook signatures; allowlisted repos and actors; the authenticated API is the source of truth | m1 |
| T | Force-push replaces the reviewed head before merge | Merge only the head commit named in the signed proof (LUCY-VAL-007) | m1 |
| R | Forge actions without attribution | Audit chain plus a provenance section in each PR | m1 |
| I | Secrets or client details posted publicly | PR text lint for secrets and `@` mentions; a public-repo check before posting | m1 |
| D | Outages or rate limits | Backoff and conditional requests; merges wait, never bypass checks | m1 |
| E | An over-privileged token bypasses branch protection | Fine-grained per-org tokens without admin; never an admin merge; rotation | m1; rotation m2 |

### 7. Client content

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | An issue or email claims "the client confirmed, merge" | Client-confirm needs the owner's relayed decision through the API | m1-m2 |
| T | Content poisons tests or build scripts | Cross-family review, deterministic tests in the container, delete guards, the sandbox | m1 |
| R | No record of which content influenced a decision | Untrusted-text tags record their source in events | m1 |
| I | One client's content reaching another org | Org isolation; org-scoped memory; no cross-org recall | m1; memory m2 |
| D | Huge or adversarial inputs | Input size limits; budgets | m1 |
| E | Prompt injection leading to privileged actions | Outside text never raises privileges; agents hold no credentials | m0-m1 |

### 8. lucy's build and release pipeline

lucy's own repository, CI, dependencies and releases.

| | Threat | Mitigation | Lands |
|---|---|---|---|
| S | A forged or look-alike lucy release is installed | Keyless cosign signature over `checksums.txt` and build-provenance attestations, verified by mise at install (LUCY-REL-002 to LUCY-REL-004) | m0 |
| T | A compromised dependency, action or tool changes what lucy builds | `go.sum` and the Go checksum database; actions pinned by full SHA; scanners pinned and installed through the checksum database; dependency review on every pull request; Dependabot (LUCY-REPO-008, LUCY-SEC-002) | m0 |
| R | Not knowing what a release contains or how it was built | An SPDX SBOM per archive; provenance naming the workflow and commit (LUCY-REL-003, LUCY-REL-008) | m0 |
| I | A secret committed to the repository or exposed by CI | gitleaks over every change's commits and weekly over the history; GitHub secret scanning with push protection; CI holds no secrets and persists no credentials (LUCY-SEC-002) | m0 |
| D | A dependency, action or scanning service is gone or down | The Go module proxy; pinned SHAs and versions; a scan that cannot run fails its check, never passes it | m0 |
| E | A change widens a workflow's token, or ships a reachable vulnerability or unsafe code | Read-only tokens, write scopes only on the release and Scorecard jobs (`internal/workflowcheck`); govulncheck and gosec fail CI; reviewed suppressions only (LUCY-SEC-002) | m0 |

## m0 check

Each row or part of a row m0 lands, checked at m0's exit against what was built.

| Row | Built in m0 | Shown by |
|---|---|---|
| 1-R (m0 part) | Every mutating API call is an `api.command` event naming uid, pid and device, appended before it runs, and a hash-chained audit entry; storing the owner's exact words is m1 | LUCY-API-005, LUCY-SPEC-009 |
| 1-E (m0 part) | Scopes are checked server-side before params are decoded; only the local owner holds scopes. CSRF, SameSite and object-level checks come with remote surfaces in m1 | LUCY-API-004 |
| 2-S | Socket 0600 in a 0700 runtime dir; the kernel's peer credentials must name the daemon's uid | LUCY-API-003, `TestPeerCredRefusesOtherUsers` |
| 2-T (m0 part) | Hash-chained audit; `lucy audit verify` detects a changed, missing or extra entry; files 0600. The `preflight` check is m1, signed backups m2 | LUCY-SPEC-009 |
| 2-R | Every policy decision is recorded before it is returned, naming rule, actor, inputs and facts | LUCY-POL-003 |
| 2-D (m0 part) | Body, nesting, time and connection limits on the API; capped event payloads. Rate-limited reconcilers and the chaos suite are m1 | LUCY-API-006, LUCY-EVT-005 |
| 2-E | Deterministic policy with default deny; outside text never raises privileges | LUCY-POL-001, LUCY-POL-004, LUCY-SIM-002 |
| 5-S | Approvals exist only as decision records; an agent's claim is not one | LUCY-POL-002, LUCY-SIM-002, the corpus |
| 5-E (m0 part) | The policy engine refuses every privileged kind for every agent role unless a rule allows it. Credential-free agents and gateway pushes are m1 | LUCY-SIM-002 |
| 7-E (m0 part) | A privileged request backed only by outside text is denied. Tagging at the harness adapters is m1 | LUCY-POL-004 |
| 8-S, 8-R | Signed, attested releases with SBOMs, installed and verified through mise | LUCY-REL-002 to LUCY-REL-004, LUCY-REL-008 |
| 8-T, 8-I, 8-D, 8-E | Pinned actions and scanners, read-only tokens, govulncheck, gosec, gitleaks and dependency review failing CI, Scorecard reporting | LUCY-REPO-008, LUCY-SEC-002 |

Residual risks m0 accepts, each with the milestone that removes it:

- Any process running as the owner's uid that reaches the socket counts as the owner. Nothing in
  m0 acts on a decision, so this authorizes nothing yet; step-up for privileged actions is m3
  (LUCY-OWN-003).
- The audit chain lives in the same database it protects, so a full rewrite is not detected;
  anchoring its head outside the database is m1 (LUCY-VAL-007).
- CI turns red on a finding, but only the repository's branch protection can make a red check stop
  a merge. It is a repository setting ([repository settings](../runbooks/repository-settings.md)),
  not code.

## Consequences

- This record replaces `docs/security/threat-model.md`; SECURITY.md, the PRD and the milestone
  plans link here.
- SECURITY.md lists eight trust boundaries.
- Every milestone's exit re-checks this model (LUCY-SEC-003) and records the result here as a
  revision; m1 lands most rows of boundaries 1 to 7.
