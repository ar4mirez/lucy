---
id: D5
title: "D5: lucy owns every session"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-RUN-004, LUCY-RUN-005, LUCY-VIEW-001, LUCY-VIEW-002, LUCY-VIEW-003, LUCY-VIEW-005, LUCY-VIEW-006]
decided: 2026-11-26
---

# D5: lucy owns every session

## Context

After a runtime restart a second supervisor session launched while a stale one still held the lock:
about 4.4 hours of a stalled org. Viewers that restore or relaunch agents on their own create such
duplicates.

## Decision

- lucy owns every agent session. Agents keep running whatever viewer closes or crashes, and are always
  reattachable (LUCY-RUN-004, LUCY-RUN-005).
- Viewers, all planned: lucy's own TUI (a bubbletea dashboard, not a terminal emulator), herdr as
  today, `lucy attach` in any terminal, a web dashboard, and Claude chat.
- Remote control (phone, other machines, Claude desktop) is very important and is designed into the
  chat connector and dashboard from the start.

## Consequences

- Viewers never own sessions; the daemon reattaches before it spawns and fences before it replaces.
- With [R1](R1-adversarial-review.md) P0-3, a session is a structured-protocol process; a PTY exists
  only where needed.
- Remote control lands in m1, the first milestone where lucy owns sessions, kept cheap by the one API
  built in m0 (LUCY-VIEW-005, LUCY-VIEW-006). The full dashboard follows in m3.
