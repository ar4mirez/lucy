---
id: runbook-repository-settings
title: GitHub repository settings
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-SEC-004, LUCY-REPO-008]
---

# GitHub repository settings

Settings for `ar4mirez/lucy` that code cannot enforce. Changing them is the repository owner's call;
agents only check and report.

## Required

| Setting | Where | Why |
|---|---|---|
| Private vulnerability reporting: on | Settings, Code security | SECURITY.md sends reports there (LUCY-SEC-004) |
| Dependabot alerts and security updates: on | Settings, Code security | Pairs with `.github/dependabot.yml` |
| Branch protection on `main`: PRs required, CI checks required, no force-push | Settings, Branches or Rulesets | Nothing lands untested |
| Actions default token: read-only | Settings, Actions | Matches the workflows' `permissions: contents: read` (LUCY-REPO-008) |
| Actions: require full-SHA pinning where offered | Settings, Actions | Matches the pinning rule |

## Check

```sh
gh api repos/ar4mirez/lucy/private-vulnerability-reporting   # want {"enabled":true}
gh api repos/ar4mirez/lucy/branches/main/protection           # want required checks
gh api repos/ar4mirez/lucy/actions/permissions/workflow       # want "default_workflow_permissions":"read"
```
