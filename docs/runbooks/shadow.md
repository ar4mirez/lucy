---
id: runbook-shadow
title: Shadow mode
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-RUN-012]
---

# Shadow mode

**When:** giving lucy the existing supervisor's tasks to compare its own decisions with, before it
takes a task class over.

**Needs:** a running daemon whose config admits the org's Airspace, and an import file in the
[format](../tech/shadow.md). Producing the file is outside lucy.

1. Check the file and import it: `lucy shadow import tasks.jsonl`. It prints how many tasks were
   imported and how many were unchanged. A refused file names the line and imports nothing. A task
   the daemon refuses (an org with no admitted Airspace, or a task id reused with other contents)
   stops the import there: it names the task, and the tasks before it stay recorded. Fix the file
   and import it again; a task already imported is left as it is.
2. List what is imported: `lucy shadow ls [--org <org>]`.

**Check:** `lucy events` shows one `shadow.task_imported` per new task on `shadow/<org>`.
