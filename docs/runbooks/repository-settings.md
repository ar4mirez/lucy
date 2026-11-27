---
id: runbook-repository-settings
title: GitHub repository settings
status: active
version: 2
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
| Branch protection on `main`: PRs required, the checks below required, no force-push | Settings, Branches or Rulesets | Nothing lands untested; a red scan stops a merge (LUCY-SEC-002) |
| Dependency graph: on | Settings, Code security | The `dependency review` check reads it |
| Code scanning: available (it is for every public repository) | Settings, Code security | Scorecard uploads its results there |
| Actions default token: read-only | Settings, Actions | Matches the workflows' `permissions: contents: read` (LUCY-REPO-008) |
| Actions: require full-SHA pinning where offered | Settings, Actions | Matches the pinning rule |

## Required checks

`check (ubuntu-latest)`, `check (macos-latest)`, `release snapshot`, `govulncheck (linux)`,
`govulncheck (darwin)`, `gosec`, `gitleaks` and `dependency review`. Scorecard runs only on `main`
and on a schedule, so it is a report, not a required check
([security scans](security-scans.md)).

## Check

```sh
gh api repos/ar4mirez/lucy/private-vulnerability-reporting   # want {"enabled":true}
gh api repos/ar4mirez/lucy/branches/main/protection           # want the required checks above
gh api repos/ar4mirez/lucy/actions/permissions/workflow       # want "default_workflow_permissions":"read"
```
