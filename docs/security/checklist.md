---
id: security-checklist
title: Security checklist mapped to milestones
status: active
version: 5
owner: ar4mirez
requirements: [LUCY-EVT-005, LUCY-API-003, LUCY-API-004, LUCY-API-005, LUCY-API-006, LUCY-API-008, LUCY-VIEW-006, LUCY-SEC-001, LUCY-SEC-002, LUCY-SEC-003, LUCY-SEC-004, LUCY-SEC-005, LUCY-SEC-006, LUCY-SEC-007, LUCY-SEC-008, LUCY-SEC-009, LUCY-SEC-010, LUCY-SEC-011, LUCY-SEC-012, LUCY-SEC-013]
---

# Security checklist mapped to milestones

The captain's security checklist has 38 items in five areas. Angel: "not all at once, but...
accounted for whenever it makes sense." Each item has an owner in lucy and a milestone.
**Totals: 10 in m0, 22 in m1, 4 in m2, 1 in m3, 1 later** (SSO: lucy serves one captain).

lucy applies the checklist to the work it runs as well: the readiness check asks the relevant
questions for security-touching tasks, review applies it as a lens, policy treats security-sensitive
changes as needing a decision, and every "later" becomes a backlog record.

| # | Item | lucy's answer | Lands | Requirements |
|---|---|---|---|---|
| 1.1 | Networking (TCP, UDP, HTTP, TLS, DNS) | TLS on remote calls; loopback and tailnet only; egress limited by berth and the gateway | m1 | LUCY-VIEW-006, LUCY-CRED-004 |
| 1.2 | OS internals and permissions | Runs as the user; files 0600, dirs 0700; doctor checks modes | m0 | LUCY-SEC-001 |
| 1.3 | Processes and privilege boundaries | Control plane, runner and gateway are separate processes with separate credentials | m1 | LUCY-RUN-001, LUCY-CRED-001 |
| 1.4 | Crypto basics | Hash-chained audit, sigstore signatures, encrypted vault, TLS | m0 | LUCY-SPEC-009, LUCY-REL-002 |
| 1.5 | Threat modeling and trust boundaries | STRIDE model, re-checked at every milestone exit | m0 | LUCY-SEC-003 |
| 1.6 | Secure design | Policy engine, scoped tokens, containers, gateway, delete guards | m0 | LUCY-POL-001 |
| 1.7 | CIA, STRIDE, kill chain | Kill-chain scenarios in the corpus and chaos suite | m1 | LUCY-SIM-003 |
| 2.1 | OWASP risks (XSS, SQLi, CSRF, SSRF, IDOR) | CSP and encoding, CSRF tokens, parameterised SQL, SSRF denylist, object-level checks | m1 | LUCY-VIEW-007, LUCY-CRED-004 |
| 2.2 | Input validation and output encoding | Strict schemas rejecting unknown fields; untrusted-text tags | m0 | LUCY-SPEC-004, LUCY-POL-004 |
| 2.3 | Sessions and cookie flags | Short-lived, HttpOnly, Secure, SameSite=Strict | m1 | LUCY-VIEW-007 |
| 2.4 | API auth | Hashed device credentials; runner and session capability tokens | m1 | LUCY-VIEW-006 |
| 2.5 | Broken access control, object-level authorization | Every call checks scope and the object's org and task | m0 | LUCY-API-004 |
| 2.6 | Rate limits and abuse prevention | Per-device limits, pairing-attempt limits, token budgets | m1 | LUCY-SEC-006 |
| 2.7 | File uploads and deserialization | Strict typed JSON only; attachments inert and size-limited | m0 | LUCY-API-006 |
| 3.1 | MFA design | Device pairing, passkeys, step-up for privileged remote actions | m3 | LUCY-CAP-003 |
| 3.2 | Credential hygiene, stuffing | No passwords; hashed device secrets; one-time codes; stray-token scans | m1 | LUCY-CRED-005 |
| 3.3 | SSO flows | None: one captain (a non-goal); revisit only for a team | later | - |
| 3.4 | RBAC and ABAC | Device scopes plus policy attributes (org, task, action, risk, posture) | m0 | LUCY-POL-001 |
| 3.5 | Workload identity, machine-to-machine auth | Runner and session capability tokens; SPIFFE-style with Agent Substrate | m1 | LUCY-CRED-003 |
| 3.6 | Least privilege, time-bound access | Stage CredentialPolicy; expiring capability tokens | m1 | LUCY-SPEC-014, LUCY-CRED-003 |
| 3.7 | Secrets management and rotation | Encrypted per-org vault in m1; rotation in m2 | m1 | LUCY-CRED-002, LUCY-SEC-011 |
| 3.8 | Delegation and session hijacking | Tokens bound to device and org; short remote sessions; anomaly checks in m3 | m1 | LUCY-VIEW-006, LUCY-SEC-012 |
| 4.1 | Segmentation, private connectivity | Container per org; tailnet-only API; nothing public | m1 | LUCY-SBX-001 |
| 4.2 | Firewalls, basic WAF | berth's default-deny egress; loopback and tailnet binds; no WAF while nothing is public | m1 | LUCY-SBX-001 |
| 4.3 | Shared-storage security | Per-org data dirs, encrypted per-org backups, offboard and erase | m2 | LUCY-SEC-011, LUCY-CRED-006 |
| 4.4 | Container and Kubernetes security | Hardening is berth's; lucy checks image version and the no-secret rule | m1 | LUCY-CRED-005, LUCY-SEC-013 |
| 4.5 | IaC and policy as code | Spec resources; AutonomyPolicy and QualityGate as validated policy | m0 | LUCY-SPEC-010 |
| 4.6 | Vulnerability management, patching | Vulnerability and dependency scans in CI; Dependabot; pinned toolchain; release SBOMs | m0 | LUCY-SEC-002, LUCY-REPO-008, LUCY-REL-008 |
| 4.7 | Endpoint hardening | Doctor host baseline | m1 | LUCY-SEC-007 |
| 4.8 | Secure configuration baselines | Setup applies and doctor checks a baseline profile | m1 | LUCY-OPS-001, LUCY-OPS-002 |
| 5.1 | Key log sources | Event store, audit chain, gateway egress log, container logs, forge events | m1 | LUCY-CRED-004, LUCY-OPS-004 |
| 5.2 | Detection engineering | Deterministic rules over events | m2 | LUCY-SEC-009 |
| 5.3 | Incident response to lessons learned | `lucy incident`; lessons become corpus scenarios; private advisories | m2 | LUCY-SEC-010, LUCY-SEC-004 |
| 5.4 | Alert triage and escalation | Detections become Decisions with severity and an owner | m2 | LUCY-SEC-009 |
| 5.5 | Forensics and evidence | Flight recorder, `lucy replay`, audit chain, evidence export | m1 | LUCY-OPS-004 |
| 5.6 | Secure coding and security review | A security lens in review, by another model family | m1 | LUCY-SEC-008 |
| 5.7 | Security testing in CI | Project SAST and dependency scans as hard gates; DAST later | m1 | LUCY-SEC-008, LUCY-SEC-013 |
| 5.8 | Automation to cut toil | lucy itself: deterministic first | m1 | LUCY-RUN-010 |

## Applied in m0 slice 1 (this repository's skeleton)

- **4.6:** Dependabot for Go modules and GitHub Actions; every action pinned by full commit SHA.
- **1.6, least privilege in CI:** top-level `permissions: contents: read`, no secrets used, checkout
  with `persist-credentials: false`.
- **5.3:** SECURITY.md routes reports to GitHub private advisories (LUCY-SEC-004).
- **Supply chain:** the skeleton has no third-party Go dependencies; the doc checker parses its own
  front matter subset instead of pulling in a YAML library.

## Applied in m0 slice 3 (the event store)

- **1.2:** the database file and its pre-migration snapshots are 0600 in 0700 dirs owned by the
  user; lucy refuses to open the database as root, with a looser mode or another owner, through a
  symlink, or inside a git work tree (LUCY-SEC-001).
- **2.1, SQL injection:** every SQL statement in `internal/eventstore` binds its values as
  parameters; `TestHostileInputIsData` appends, reads, claims and acks with stream, actor, consumer
  and payload strings built to break out of SQL (quotes, `'; DROP TABLE events; --`, backslashes,
  unicode), checks they round-trip exactly and that the schema and event count are unchanged
  (LUCY-EVT-005).
- **2.7 and STRIDE 2-D:** event payloads are strict typed JSON, capped at 64 KiB, at most 256 events
  per append; decoding rejects unknown fields.
- **3.7, secrets:** events never carry secrets; secret-looking field names are refused at
  registration and at append, map keys included (LUCY-EVT-005,
  [database runbook](../runbooks/database.md)).

## Applied in m0 slice 4 (the typed API and `lucy daemon`)

- **1.1 and 4.2, nothing on the network:** the daemon's only listener is its unix socket; no network
  listener exists, by default or by flag. Remote HTTPS with device pairing, scoped credentials and
  TLS lands in m1 (LUCY-VIEW-006).
- **1.2:** the socket is 0600 in a 0700 runtime dir that must be a real directory owned by the user;
  a dir planted in a shared `/tmp` is refused, not used; only a stale socket is ever removed, never
  another file (LUCY-SEC-001).
- **2.4 and STRIDE 2-S, local authentication:** each connection's kernel peer credentials
  (SO_PEERCRED on Linux, LOCAL_PEERCRED on macOS) must name the daemon's uid, or it is closed before
  a byte is read; `TestPeerCredRefusesOtherUsers` drives a real refusal (LUCY-API-003).
- **2.5 and 3.4, access control seam:** the caller rides on every request's context; an authorizer
  checks the method's scope before params are decoded. Only the local owner holds scopes until the
  policy engine (slice 5) adds org and task checks (LUCY-API-004).
- **2.2 and 2.7, input:** strict JSON only: no unknown, duplicate or case-folded members, bounds on
  every field, 64 KiB bodies, 32 levels of nesting, `application/json` only; `FuzzDecodeRequest`
  fuzzes the decoder (LUCY-API-006).
- **2.6 and STRIDE 2-D, resource limits:** header, read, write, idle and per-call timeouts; at most 64
  open connections. Per-device rate limits come with remote devices (LUCY-SEC-006, m1).
- **Information disclosure:** errors carry a fixed message per code; internal errors carry only a
  correlation id, and a test checks handler errors and panics do not leak (LUCY-API-008).
- **STRIDE 1-R, repudiation:** every mutating call is appended as an `api.command` event naming the
  actor, pid and device before it runs, and does not run if the append fails (LUCY-API-005); the
  hash chain over it is slice 6.
- **Deferred, remote authentication:** no network listener exists in m0. Remote HTTPS with device
  pairing, scoped credentials (hashed, revocable) and TLS on loopback and tailnet binds lands in m1
  with remote control (LUCY-VIEW-006, checklist 2.4); the passkey step-up for privileged remote actions in m3 (LUCY-CAP-003). Until then
  the API cannot be reached from another machine.
