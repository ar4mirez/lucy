---
id: D8
title: "D8: built-in vs external inventory; full setup"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-OPS-001, LUCY-OPS-002]
decided: 2026-11-26
---

# D8: built-in vs external inventory; full setup

## Context

Review what is built into lucy vs external tools, and account for each external as a managed
risk. Installing lucy must provide the full installation of lucy plus everything it needs.

## Decision

- Every external tool is a managed risk with a pin, a health check and a mitigation.
- **Hard external dependencies (5):** git, a container runtime (Docker, Podman or Apple `container`),
  berth, Claude Code (at least one harness), mise.
- **Optional (11):** another model family's harness, herdr, tmux, Tailscale, Node, lavish-axi, gh-axi,
  chrome-devtools-axi, quota-axi, the no-mistakes skill, ai-memory. Agent Substrate is future.
- **Built in:** GitHub access (lucy's own API client), quota and usage data, SQLite, sigstore, the
  policy engine, gateway, pipeline, TUI, dashboard and MCP server.
- `lucy setup` installs everything through mise from a pinned manifest, plan first (LUCY-OPS-001).
  OS prerequisites are checked, never installed; setup never runs `sudo`.
- `lucy doctor` checks every row and repairs what it can (LUCY-OPS-002).

## Consequences

- The full inventory with pins, risks and mitigations lives in [TECH.md](../../TECH.md).
- lucy writes its pinned toolchain under its own data dir, leaving the user's global mise config alone.
