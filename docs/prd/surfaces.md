---
id: prd-surfaces
title: "PRD: viewers, remote control and the captain surface"
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-VIEW-001, LUCY-VIEW-002, LUCY-VIEW-003, LUCY-VIEW-004, LUCY-VIEW-005, LUCY-VIEW-006, LUCY-VIEW-007, LUCY-CAP-001, LUCY-CAP-002, LUCY-CAP-003, LUCY-CAP-004]
---

# PRD: viewers, remote control and the captain surface

Areas `VIEW` and `CAP`. Background: [D5](../decisions/D5-session-ownership.md). Every surface is a thin
client of the one API (LUCY-API-001).

## VIEW: viewers and remote control

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-VIEW-001 | `lucy attach` shows a live session in any terminal, with a detach key | m1 | D5 |
| LUCY-VIEW-002 | A bubbletea TUI shows orgs, tasks, sessions, decisions and events, and hands off to `lucy attach` | m1 | D5 |
| LUCY-VIEW-003 | herdr panes run `lucy attach`; a herdr crash is only a viewer crash | m1 | D5 |
| LUCY-VIEW-004 | The MCP server for Claude chat exposes about nine tools in under 2k tokens | m1 | report 4.7 |
| LUCY-VIEW-005 | A phone-first web page shows status and decisions, and steers and attaches | m1 | D5 |
| LUCY-VIEW-006 | Remote use binds loopback and tailnet only, over TLS; devices pair with one-time codes and hashed, revocable, scoped credentials | m1 | D5, checklist 2.4 |
| LUCY-VIEW-007 | Dashboard sessions use CSP, output encoding, CSRF tokens and HttpOnly, Secure, SameSite=Strict cookies | m1 | checklist 2.1, 2.3 |

## CAP: captain surface

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-CAP-001 | A Claude Code plugin (MCP server, one skill, a `/lucy` command) makes the first-mate role folder-free | m3 | report 4.7 |
| LUCY-CAP-002 | The full dashboard has structured decision cards and push notifications | m3 | D5 |
| LUCY-CAP-003 | Privileged remote actions (merge, credentials, destructive) need a passkey step-up | m3 | checklist 3.1 |
| LUCY-CAP-004 | firstmate homes are archived read-only once every org has moved | m3 | D2 revised |
