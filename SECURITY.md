---
id: security
title: "lucy: security policy and model"
status: active
version: 11
owner: ar4mirez
requirements: [LUCY-SEC-004, LUCY-SEC-003, LUCY-SEC-002, LUCY-REPO-008, LUCY-POL-001, LUCY-CRED-005]
---

# lucy: security policy and model

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's private vulnerability reporting: open the
repository's **Security** tab and choose **Report a vulnerability**
(https://github.com/ar4mirez/lucy/security/advisories/new). Do not open a public issue, PR or
discussion for a suspected vulnerability.

Include what you found, how to reproduce it, the version or commit, and the impact you expect. You
will get an acknowledgement, a fix or mitigation plan, and credit in the advisory if you want it.
lucy is pre-release (0.x): only the `main` branch is supported, and fixes land there.

## Model

lucy runs untrusted agents on behalf of one owner across several isolated orgs. Its design follows
from a STRIDE threat model over eight trust boundaries, recorded as decision
[D12](docs/decisions/D12-threat-model.md):

1. Owner surfaces to the daemon (CLI, TUI, dashboard, MCP, paired devices)
2. The daemon (API, policy engine, event store)
3. Runners and berth containers
4. The per-org credential gateway
5. Agents (harness sessions)
6. The forge (GitHub)
7. Client content (repos, issues, PR comments, emails, dependencies)
8. lucy's own build and release pipeline (repository, CI, dependencies, releases)

The controls that answer most of those threats:

- **LLM proposes, policy disposes.** Deterministic policy authorizes every privileged action; agent
  text never does (LUCY-POL-001).
- **Credential-free agents.** Tokens live in a per-org gateway outside the agents' container; no
  token is ever inside a container (LUCY-CRED-005). The gateway adds a token only to an allowlisted
  method and path on an `https://` upstream, refuses private, link-local and metadata addresses,
  never follows a redirect, scrubs an echoed token from the answer (asking only for unencoded,
  whole answers and refusing an encoded one) and fails closed. It runs as its own process and
  records every request through the daemon, which decides each credential use from its own routes
  ([gateway](docs/tech/gateway.md)).
- **A container per org**, default-deny egress, delete guards, a synthetic HOME per org.
- **Tamper-evident audit:** a hash chain (RFC 8785 + SHA-256) of every privileged action, naming the
  rule, actor and decision.
- **Nothing public by default:** loopback and tailnet only, TLS, paired devices with hashed, scoped,
  revocable credentials.
- **Supply chain:** signed releases (keyless cosign) with build provenance verified by mise and an
  SBOM per archive, pinned tools, CI actions pinned by SHA with a read-only token (LUCY-REPO-008);
  [verifying a release](docs/runbooks/verify-release.md).
- **lucy's own scans:** govulncheck, gosec with a reviewed baseline, gitleaks and dependency review
  fail CI on a finding; OpenSSF Scorecard reports to code scanning (LUCY-SEC-002,
  [security scans](docs/runbooks/security-scans.md)).

## Credential keys at rest

An org's credentials are kept in lucy's vault ([vault](docs/tech/vault.md)), each sealed with
AES-256-GCM under that org's own 32-byte key and bound to its org and name, so one org's key opens
nothing of another's and a sealed file moved or changed does not open (LUCY-CRED-002).

- **Where the keys are.** `keys/<org>.key` in the vault directory (0700), each 0600, written once
  from the system's random source; the sealed credentials are under `orgs/`, apart from them. A
  copy of `orgs/` alone, a backup without `keys/` (lucy's backups, m2, leave it out), reveals
  nothing.
- **Who reads them.** Only the org's gateway process, for its own org; no agent, runner or
  container ever has a key or a credential, and the daemon holds neither.
- **What this protects against.** A copy of the vault's data without its keys, and one org's
  gateway reaching another's credentials through the vault. It does not protect against someone
  who can already read the owner's files, who can read the keys too: that is the host's account
  security (`lucy preflight --check`, m1 slice 15). Keeping the keys in an OS keystore instead is a
  later choice for the owner.
- **Rotation and erasure.** Replacing a credential is `lucy vault set`; deleting an org's key
  erases all its credentials at once, which offboarding will use (m2). A crash while a key is being
  published can leave `keys/<org>.key.tmp-<hex>`, a second copy of the live key, so erasing an org
  deletes `keys/<org>.key` and every `keys/<org>.key.tmp-*` beside it.
- **Tests** use keys and credentials they generate; nothing reads a real credential.

## Capability tokens

A run proves to its org's gateway that it may use a route with a capability token
([capabilities](docs/tech/capability.md), LUCY-CRED-003): 32 random bytes, issued by the daemon on
the run's lease and given only to its runner, scoped to the org, the run under that lease, the
org's routes, and five minutes. Each heartbeat of the live lease renews it with a fresh token and
revokes the one before, never past four hours from the lease's start; finishing or stopping the
run, or its lease running out, revokes its tokens. The daemon stores only the token's SHA-256 and
compares it in constant time; a token is refused once revoked, past its expiry, for another org or
route, or once its run's lease has ended or been replaced. The daemon checks it before the gateway
reads any credential or resolves any upstream, and again when deciding. The gateway never
forwards it. Until the owner
decides how a Pilot's tools get it, no token is in a Pilot's environment.

## Audit and log data

Decision records and the audit trail hold names and references, never secrets or input text. A
Pilot's shell command, URL or search (an `exec` or `fetch` request) is untrusted text that can
carry secrets, so the policy engine decides on it as written, in memory, and the record and
`policy.decisions` hold only `policy.RecordedResource`: the text with Authorization and Cookie
values, URL credentials, credential-named query parameters and `NAME=value` assignments, known
token prefixes (GitHub, OpenAI, Slack, AWS, GitLab) and long high-entropy strings replaced by
`<redacted>`, capped at 512 bytes, then `sha256:` and the digest of the original, so one command
can be matched across records without being stored. `runner.permit`'s `api.command` record holds
only a digest of its parameters. The gateway's records (`gateway.forwarded`, `gateway.denied`,
`gateway.completed`) hold the route, method, redacted path, upstream host, status, the policy
decision, and the run and capability id, never a credential, a capability token, a query string or
a body. A vault change (`vault.changed`) is recorded before and after it is made, by org, name and
operation only; none is made unrecorded.

The owner's security checklist (38 items in five areas) is mapped to milestones and requirements in the
[checklist map](docs/security/checklist.md). The threat model is re-checked at every milestone exit and each
check is recorded in D12 (LUCY-SEC-003).
