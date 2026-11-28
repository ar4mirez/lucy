---
id: security
title: "lucy: security policy and model"
status: active
version: 18
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

- **Where the keys are.** `keys/<org>.wkey` in the vault directory (0700), each 0600, made once
  from the system's random source and sealed with AES-256-GCM under a key-encryption key derived
  from the owner's vault passphrase with PBKDF2-HMAC-SHA256 (600,000 iterations, a random salt;
  [D14](docs/decisions/D14-vault-passphrase-kek.md)). The passphrase and the derived key are never
  stored; `keys/kek` holds only the salt, the iteration count and a check value. The sealed
  credentials are under `orgs/`, apart from the keys. A vault from before keys were wrapped is
  migrated on its first unlock, its plain keys removed; after that a plain key is refused.
- **The passphrase.** `lucy gateway` and `lucy vault set` read it from `--passphrase-file` (a
  plain file only its owner can read, outside the vault and lucy's data directory), else ask once
  on the terminal with echo off; never from an argument or the environment. It is 12 to 1024 bytes. A lost passphrase cannot be recovered.
- **Who reads them.** Only the org's gateway process, for its own org; no agent, runner or
  container ever has a key or a credential, and the daemon holds neither.
- **What this protects against.** A copy of the vault, keys included, without the passphrase;
  and one org's gateway reaching another's credentials through the vault. It does not protect
  against someone who can run code as the owner's user while a gateway is running and read the
  derived key from its memory, or read a passphrase file: that is the host's account security
  (`lucy preflight --check`, m1 slice 15). An OS keystore or a hardware-backed key stays a later
  option.
- **Rotation and erasure.** Replacing a credential is `lucy vault set`; deleting an org's key
  erases all its credentials at once, which offboarding will use (m2). A crash while a key is being
  published can leave `keys/<org>.wkey.tmp-<hex>`, a second copy of the wrapped key, so erasing an
  org deletes `keys/<org>.wkey` and every `keys/<org>.wkey.tmp-*` beside it; the first unlock
  removes any plain `keys/<org>.key.tmp-*` from before keys were wrapped. Changing the passphrase
  is not built yet.
- **Tests** use keys, passphrases and credentials they generate; nothing reads a real credential.

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
forwards it. A Pilot reaches the gateway through a socket the runner opens in its scratch
directory, which adds the token on the Pilot's behalf, so the runner never puts a token in a
Pilot's process, environment or files ([D13](docs/decisions/D13-run-gateway-socket.md)). That is
not a boundary against the Pilot itself: a Pilot on the host runs as the daemon's uid, which the
daemon grants every scope, so it can reach the daemon API with full authority for its own org,
`runner.lease` and the token it returns included, until runs get an OS boundary of their own
([#36](https://github.com/ar4mirez/lucy/issues/36), LUCY-RUN-006, berth#216). No Pilot runs real work on the host before then. What
the code enforces until #36 and the owner's go/no-go: the daemon dispatches no Flight unless
started with `--dispatch`, and even then none whose Runway names a repository that is not a local
fixture under lucy's data directory or the system temp directory (`daemon.ErrTakeoverGate`); it
gives no run a checkout of such a repository, whenever the run is leased, so a flight already in
the log whose Runway later names a real remote gets the gate as its worktree error and nothing is
reserved or cloned; and the gateway carries no git push (`git-receive-pack`). It does not stop a
host Pilot from using the daemon user's own git or SSH credentials: that is #36. Any process of the run can
still act with its token, through that socket, for the run's life and within its org, routes and
lease. Isolation between orgs holds across containers or uids, not within one: the socket's modes
keep out other users, but every process of the same uid can reach every other's socket. So the
daemon serves one host org: the first run leased to a runner on the host sets it, recorded as
`runner.host_org_set`, and a host lease for any other org is refused (`host_org`). Only the owner
changes it (`lucy runner host-org <org>`, recorded), and not while any host run is live. A
placement a runner declares is never trusted: container placement is refused (`placement_proof`)
until the Sandbox provider can issue a placement proof (berth#216), and no daemon has a verifier
for one yet. Every runner names its org (`--org`, required for every placement; a runner without
one refuses to start), and its root defaults to that org's own, so no runner root spans orgs. One
org's runs are therefore the only runs on the host at any time.

## Deletes

Every delete lucy makes refuses `/`, `$HOME` and anything outside lucy's managed roots
(LUCY-SEC-005, incident 1). A recursive delete goes through the path guard's executor
(`pathguard.Guard.RemoveAll`): the path as asked for, and its parent resolved through symlinks
with the last component kept, must both be strictly inside a declared root and never a root
itself, and it is deleted through an `os.Root` on that root, so a symlink, the requested one or one
inside, is removed rather than followed, and none swapped in after the check takes the delete out
of the root. lucy makes two recursive deletes. The first is a run's scratch directory, a
`run-*` directory directly in the runner's root, never the org's home beside it. The second is a
quarantined or broken slot of the worktree pool that the owner discards (`lucy worktree discard`,
the admin-scoped `worktree.discard`, refused unless the confirmation repeats the slot exactly and
for a free or leased slot): the discard is recorded first (`worktree.discarded`: by whom, the slot,
its run, branch and head), then the slot's directory is removed through the guard with the pool's
root as the one root, and the run's branch stays in the clone. A guard given a role's blocked paths
(`Guard.WithBlocked`, LUCY-SPEC-015) also refuses, before removing anything, a target that matches
one, ignoring case, or a directory holding an entry that does. Every other delete removes one file
lucy made and names: a stale socket of its own (checked to be a socket first), a runner's registry
entry, a temporary file it created, or a vault file through the vault's own `os.Root`. Otherwise
the worktree pool deletes nothing itself: resetting a free slot, whose work has landed or never
started, lets git remove that slot's changes and untracked files, and `git worktree prune` removes
only base.git's stale registrations.

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
