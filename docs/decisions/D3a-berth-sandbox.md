---
id: D3a
title: "D3a: berth is the org sandbox, through its API"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-SBX-001, LUCY-SBX-003]
decided: 2026-11-26
---

# D3a: berth is the org sandbox, through its API

## Context

A worker's code ran `rm_rf('/')` on the host and wiped most of `~/.config`. A prompt-level guard could
not have stopped it: the delete was inside code the agent ran. Only an OS-level boundary does. berth,
Angel's own Apache-2.0 product, already gives each org a container with its own account, git key,
default-deny egress firewall and repo allowlist, plus an HTTP API.

## Decision

- Every worker runs in an isolated container per org (a berth), from m1.
- berth stays its own product; lucy talks to it only through its API, not by importing its code.
- Only orgs with active work have a running container; idle orgs are stopped.

## Consequences

- lucy's validation agents run in the org's container too, so none touches the host (LUCY-SBX-001).
- lucy never restarts or recreates an org container with live sessions without the captain
  (LUCY-SBX-003).
- berth's packages are all `internal/`; a small exported API client in berth would help (a berth change).
- berth, git and a container runtime become hard dependencies ([D8](D8-built-in-vs-external.md)).
