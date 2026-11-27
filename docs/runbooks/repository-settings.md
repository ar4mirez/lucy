---
id: runbook-repository-settings
title: GitHub repository settings
status: active
version: 3
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
| Dependency graph: on | Settings, Code security | The `dependency review` check reads it |
| Code scanning: available (it is for every public repository) | Settings, Code security | Scorecard uploads its results there |
| Actions default token: read-only | Settings, Actions | Matches the workflows' `permissions: contents: read` (LUCY-REPO-008) |
| Actions: require full-SHA pinning where offered | Settings, Actions | Matches the pinning rule |

## Ruleset for `main` (m0 exit test 5)

Not applied yet: whether and when to apply it is the owner's decision. Until it is, a red check
does not stop a merge, and m0 exit test 5 stays open.

- **Target:** the default branch, `main`.
- **Require a pull request before merging.**
- **Required status checks:** `check (ubuntu-latest)`, `check (macos-latest)`, `release snapshot`,
  and, so the security scans block merges (what exit test 5 asks), `govulncheck (linux)`,
  `govulncheck (darwin)`, `gosec`, `gitleaks` and `dependency review`.
- **Block force pushes.**
- **Restrict deletions.**

These are the only required checks. Scorecard runs only on `main` and on a schedule, so it is a
report ([security scans](security-scans.md)). `install (ubuntu-latest)` and `install (macos-latest)`
are not required either: they run only after a release is published, on demand, or when
`install.yml` itself changes.

## Check

```sh
gh api repos/ar4mirez/lucy/private-vulnerability-reporting   # want {"enabled":true}
gh api repos/ar4mirez/lucy/rules/branches/main               # want the ruleset above
gh api repos/ar4mirez/lucy/actions/permissions/workflow       # want "default_workflow_permissions":"read"
```
