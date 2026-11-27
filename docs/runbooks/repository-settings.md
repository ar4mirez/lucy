---
id: runbook-repository-settings
title: GitHub repository settings
status: active
version: 5
owner: ar4mirez
requirements: [LUCY-SEC-004, LUCY-REPO-008, LUCY-SEC-002]
---

# GitHub repository settings

Settings for `ar4mirez/lucy` that code cannot enforce. Changing them is the repository owner's call;
agents only check and report.

## Required

| Setting | Where | Why |
|---|---|---|
| Private vulnerability reporting: on | Settings, Code security | SECURITY.md sends reports there (LUCY-SEC-004) |
| Dependabot alerts and security updates: on | Settings, Code security | Pairs with `.github/dependabot.yml` |
| The ruleset for `main` below | Settings, Rules, Rulesets | Nothing lands untested; a red scan stops a merge (LUCY-SEC-002) |
| Dependency graph: on, by enabling vulnerability alerts | Settings, Code security | The `dependency review` check reads it; without vulnerability alerts it fails with `Forbidden` from the dependency-graph compare API |
| Code scanning: available (it is for every public repository) | Settings, Code security | Scorecard uploads its results there |
| Actions default token: read-only | Settings, Actions | Matches the workflows' `permissions: contents: read` (LUCY-REPO-008) |
| Actions: require full-SHA pinning where offered | Settings, Actions | Matches the pinning rule |

## Ruleset for `main` (m0 exit test 5)

Applied: a repository ruleset named `main`, enforcement active, with no bypass actors, so the
rules bind the owner and every token alike.

- **Target:** the default branch, `main`.
- **Require a pull request before merging,** with 0 required approvals: nothing reaches `main`
  except through a pull request, and a pull request merges as soon as its required checks are green
  (merge-on-green), without waiting for a review.
- **Required status checks,** from GitHub Actions, not strict (a branch need not be up to date
  with `main` before it merges): `check (ubuntu-latest)`, `check (macos-latest)`,
  `release snapshot`, and, so the security scans block merges (what exit test 5 asks),
  `govulncheck (linux)`, `govulncheck (darwin)`, `gosec`, `gitleaks` and `dependency review`.
- **Block force pushes** (non-fast-forward updates).
- **Restrict deletions:** `main` cannot be deleted.

These are the only required checks. Scorecard runs only on `main` and on a schedule, so it is a
report ([security scans](security-scans.md)). `install (ubuntu-latest)` and `install (macos-latest)`
are not required either: they run only after a release is published, on demand, or when
`install.yml` itself changes.

## Check

```sh
gh api repos/ar4mirez/lucy/private-vulnerability-reporting   # want {"enabled":true}
gh api repos/ar4mirez/lucy/vulnerability-alerts              # want HTTP 204 (enabled)
gh api repos/ar4mirez/lucy/rulesets                          # want one ruleset "main", "enforcement":"active"
gh api repos/ar4mirez/lucy/rulesets/<id>                     # want the rules above and "bypass_actors":[]
gh api repos/ar4mirez/lucy/rules/branches/main               # want deletion, non_fast_forward, pull_request, required_status_checks
gh api repos/ar4mirez/lucy/actions/permissions/workflow       # want "default_workflow_permissions":"read"
```
