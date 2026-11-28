---
id: tech-shadow
title: Shadow mode and its import format
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-RUN-012]
---

# Shadow mode and its import format

Shadow mode (LUCY-RUN-012, first half) compares what lucy would decide on an org's tasks with what
the existing supervisor did, per task class, before lucy takes a class over. lucy learns what the
existing supervisor did only from a file the owner gives it, `lucy shadow import <file>`: it reads
no live state, no forge, no harness, no container and no other tool's files, and it never builds or
runs an export for another tool. The code is `internal/shadow`.

## The file, version 1

JSON Lines, UTF-8, at most 8 MiB, each line at most 4 KiB and one JSON object; blank lines are
skipped. Every line carries `"v": 1` and a `type`. A task line comes before its actions.

```jsonl
{"v":1,"type":"task","org":"acme","task":"t-1","class":"bugfix","goal":"fix the login page","repository":"web","posture":"green"}
{"v":1,"type":"action","task":"t-1","at":"2026-11-20T10:00:00Z","action":"dispatched"}
{"v":1,"type":"action","task":"t-1","at":"2026-11-20T10:05:00Z","action":"asked","text":"which region?"}
{"v":1,"type":"action","task":"t-1","at":"2026-11-20T10:06:00Z","action":"answered","by":"owner"}
{"v":1,"type":"action","task":"t-1","at":"2026-11-20T10:30:00Z","action":"pr_opened","checks":"passing"}
{"v":1,"type":"action","task":"t-1","at":"2026-11-20T10:40:00Z","action":"merged"}
```

| Field | Line | Value |
|---|---|---|
| `org` | task | the org's name; its Airspace must be admitted in the daemon's config |
| `task` | both | the task's id: `[a-z0-9][a-z0-9._-]{0,63}`, once per file |
| `class` | task | the task class compared and taken over by: `[a-z0-9][a-z0-9-]{0,31}` |
| `goal` | task | 1 to 512 bytes |
| `repository` | task | the repository's name, `[A-Za-z0-9._-]{1,100}` |
| `posture` | task | `green`, `owner` or `client` |
| `at` | action | RFC 3339; a task's actions are in time order |
| `action` | action | one of the actions below |
| `text` | action | `asked` only: the question, 1 to 512 bytes |
| `by` | action | `answered` only: `supervisor` or `owner`; the answer itself is never imported |
| `checks` | action | `pr_opened` only: `passing`, `failing` or `pending` |

Actions: `dispatched` (always first), `asked`, `answered`, `pr_opened`, `review_requested`,
`merged`, `closed` (without merging) and `handed_to_owner`; nothing follows `merged` or `closed`.
A task has 1 to 40 actions, and a file at most 2000 tasks.

## What is refused

The file is read through an allowlist. Any other field (a transcript, a diff, file contents, an
answer's text) is refused, and so is any value out of bounds and any goal or question that looks
like a credential: a known token prefix, a private key block, a JWT, a cloud access key id, each
at the start of the text or after a character that is not a letter or digit (so a word such as
`task-runner-integration-tests` is not taken for a token). One refused line refuses the whole
file, naming the line, and nothing is imported. The daemon checks every task again
(`shadow.import`), whatever the client checked, and refuses, task by task, a task of an org with no
admitted Airspace or a task id imported before with other contents: the import stops at that task,
and the tasks recorded before it stay recorded.

## Import

`lucy shadow import` records each task as `shadow.task_imported` on the org's `shadow/<org>`
stream, by the caller, with the task's digest. The same task again records nothing (`unchanged`);
a task id imported before with other contents is refused, so a record never changes under the
comparison. `lucy shadow ls` lists the imported tasks by org, class and task.

TODO(LUCY-RUN-012): replay each imported task through lucy's Tower in simulation (no Pilot, no forge,
no network) and `lucy shadow report`: each class matching, stricter, looser or different, where
looser blocks takeover of that class.
