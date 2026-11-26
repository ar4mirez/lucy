---
id: D1
title: "D1: Go, headless, Linux and macOS"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-REL-001, LUCY-EVT-001]
decided: 2026-11-26
---

# D1: Go, headless, Linux and macOS

## Context

lucy's workload is processes, sockets, SQLite, git and HTTP. Its integration partners (berth,
treehouse, no-mistakes, Agent Substrate, the ai-sdlc Go SDK) are Go. Rust's advantages (terminal
emulation, the lowest memory floor, a stricter type system for the state machine) matter less because
lucy does not emulate a terminal and its memory is small next to the agents it runs.

## Decision

- lucy is written in Go and ships as one binary whose modes are the daemon, CLI, MCP server, hook
  entrypoint, runner and gateway.
- lucy stays headless: herdr, tmux, lucy's own TUI or a dashboard display sessions.
- Platforms: Linux and macOS. Built and maintained by the captain plus agents.

## Consequences

- Pure-Go SQLite keeps `CGO_ENABLED=0` cross-builds simple (LUCY-REL-001, LUCY-EVT-001).
- Data races are a risk the type system does not catch: CI runs `go test -race` on both platforms.
- The toolchain is pinned in `go.mod` and `mise.toml`.
