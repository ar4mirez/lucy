---
id: prd-surfaces
title: "PRD: viewers, remote control and the owner surface"
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-VIEW-001, LUCY-VIEW-002, LUCY-VIEW-003, LUCY-VIEW-004, LUCY-VIEW-005, LUCY-VIEW-006, LUCY-VIEW-007, LUCY-OWN-001, LUCY-OWN-002, LUCY-OWN-003, LUCY-OWN-004]
---

# PRD: viewers, remote control and the owner surface

Areas `VIEW` and `OWN`. Background: [D5](../decisions/D5-session-ownership.md). Every surface is a thin
client of the one API (LUCY-API-001).

## VIEW: viewers and remote control

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-VIEW-001 | `lucy attach` shows a live session in any terminal, with a detach key | m1 | D5 |
| LUCY-VIEW-002 | A bubbletea TUI shows orgs, tasks, sessions, decisions and events, and hands off to `lucy attach` | m1 | D5 |
| LUCY-VIEW-003 | herdr panes run `lucy attach`; a herdr crash is only a viewer crash | m1 | D5 |
| LUCY-VIEW-004 | The MCP server for Claude chat exposes about nine tools in under 2k tokens | m1 | Rationale: a small schema keeps every chat turn cheap |
| LUCY-VIEW-005 | A phone-first web page shows status and decisions, and steers and attaches | m1 | D5 |
| LUCY-VIEW-006 | Remote use binds loopback and tailnet only, over TLS; devices pair with one-time codes and hashed, revocable, scoped credentials | m1 | D5, checklist 2.4 |
| LUCY-VIEW-007 | Dashboard sessions use CSP, output encoding, CSRF tokens and HttpOnly, Secure, SameSite=Strict cookies | m1 | checklist 2.1, 2.3 |

## OWN: owner surface

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-OWN-001 | A Claude Code plugin (MCP server, one skill, a `/lucy` command) lets the owner work from any Claude Code session with no folder | m3 | Rationale: the owner needs no checkout to work from chat |
| LUCY-OWN-002 | The full dashboard has structured decision cards and push notifications | m3 | D5 |
| LUCY-OWN-003 | Privileged remote actions (merge, credentials, destructive) need a passkey step-up | m3 | checklist 3.1 |
| LUCY-OWN-004 | The existing supervisor's homes are archived read-only once every org has moved | m3 | D2 revised |
