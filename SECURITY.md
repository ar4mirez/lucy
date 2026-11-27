---
id: security
title: "lucy: security policy and model"
status: active
version: 4
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
lucy is pre-release (m0): only the `main` branch is supported, and fixes land there.

## Model

lucy runs untrusted agents on behalf of one owner across several isolated orgs. Its design follows
from a STRIDE threat model over eight trust boundaries, recorded as decision
[D12](docs/decisions/D12-threat-model.md):

1. Owner surfaces to the daemon (CLI, TUI, dashboard, MCP, paired devices)
2. The daemon (API, policy engine, event store, vault)
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
  token is ever inside a container (LUCY-CRED-005).
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

The owner's security checklist (38 items in five areas) is mapped to milestones and requirements in the
[checklist map](docs/security/checklist.md). The threat model is re-checked at every milestone exit and each
check is recorded in D12 (LUCY-SEC-003).
