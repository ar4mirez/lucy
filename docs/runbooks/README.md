---
id: runbooks
title: Runbooks
status: active
version: 6
owner: ar4mirez
requirements: []
---

# Runbooks

Step-by-step operations. Each runbook says when to use it, what it needs, the steps, and how to check
the result.

| Runbook | When |
|---|---|
| [release.md](release.md) | Cutting a lucy release |
| [verify-release.md](verify-release.md) | Verifying a release's signature, checksums and attestation |
| [repository-settings.md](repository-settings.md) | Setting up or auditing the GitHub repository's settings |
| [security-scans.md](security-scans.md) | A red security check, a new suppression, or a scanner bump |
| [daemon.md](daemon.md) | Running, checking or stopping the daemon; adding an API method |
| [database.md](database.md) | Adding an event type or migration; upgrading, rolling back or inspecting the database |
| [upgrade.md](upgrade.md) | Upgrading lucy in place while runs are live |
