---
id: runbook-security-scans
title: lucy's own security scans
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-SEC-002, LUCY-REPO-008]
---

# lucy's own security scans

Use this when a security check is red, when adding a suppression, or when bumping a scanner. The
scans cover lucy's own code and dependencies (LUCY-SEC-002); the projects lucy runs get their own
gates in m1 (LUCY-SEC-008).

## What runs

`.github/workflows/security.yml` runs on every pull request, every push to `main` and weekly
(Monday 06:17 UTC), with a read-only token and no secrets. Every job fails the run on a finding.

| Check | Tool | Fails on |
|---|---|---|
| `govulncheck (linux)`, `govulncheck (darwin)` | govulncheck, source mode | A known vulnerability that lucy's code can reach, in a module or in the Go standard library it builds with |
| `gosec` | gosec, every rule, Linux and macOS | Any finding not listed in the [SAST baseline](../security/reviews/sast-baseline.md) |
| `gitleaks` | gitleaks, redacted output | A secret in the pull request's commits, the commits a push adds, or (weekly) the whole history |
| `dependency review` | GitHub's dependency review, pull requests only | A new or changed Go module or action with a known vulnerability at any severity, or a licence outside the allowlist |

`.github/workflows/scorecard.yml` runs OpenSSF Scorecard weekly and on every push to `main`, and
uploads its results to code scanning (Security tab, "Code scanning"). It is a report, not a gate:
it publishes nothing outside the repository and needs no extra token. Its job is the only one
besides the release job that holds a write scope (`security-events`), which
`internal/workflowcheck` enforces.

Dependabot (`.github/dependabot.yml`) opens weekly updates for Go modules and the actions' pinned
SHAs. It does not see the scanner versions below.

## Run them locally

```sh
mise run security
```

This runs govulncheck, gosec and gitleaks (over the full history) at the versions CI pins, for the
host's OS. Dependency review and Scorecard run only on GitHub.

## When a check is red

- **govulncheck:** update the module named in the report (`go get <module>@<fixed version>`, then
  `go mod tidy`), or raise Go in `go.mod` and `mise.toml` for a standard-library fix. If no fix
  exists yet, the check stays red; a decision to ship anyway is the owner's.
- **gosec:** fix the code. If the code is right as it is, add `// #nosec G<nnn> -- <reason>` on the
  line, one rule each, and a row in the [SAST baseline](../security/reviews/sast-baseline.md) in
  the same pull request; `internal/seccheck` fails until both agree. Never add exclusion flags to
  the workflow or `nolint:gosec`.
- **gitleaks:** treat the secret as leaked: revoke and rotate it first, then remove it from the
  branch. A false positive is not suppressed in the workflow; it needs a reviewed `.gitleaksignore`
  entry (the finding's fingerprint) in its own pull request.
- **dependency review:** pick another version or module. Widening `allow-licenses` is a reviewed
  change to `security.yml`.

## Bump a scanner

The versions are pinned in two places, kept equal by `internal/seccheck`: the `env` block of
`security.yml` and the `security` task in `mise.toml`. Change both, run `mise run security` and
`go test ./internal/seccheck`, and review any new findings in the same pull request.

## Check

```sh
gh run list --workflow security.yml --limit 5      # want: completed, success
gh api repos/ar4mirez/lucy/code-scanning/alerts --jq 'length'  # Scorecard results, after the first run on main
```
