---
id: runbook-daemon
title: The daemon and its API
status: active
version: 4
owner: ar4mirez
requirements: [LUCY-API-001, LUCY-API-003, LUCY-API-005, LUCY-API-006, LUCY-API-007, LUCY-API-008, LUCY-POL-002, LUCY-POL-003]
---

# The daemon and its API

**When:** running or stopping `lucy daemon`, diagnosing why a client cannot reach it, or adding an
API method. **Needs:** lucy installed; for changes, the repository and `mise install`. The code is
`internal/daemon`, `internal/server`, `internal/api` and `internal/api/client`; the design is
[TECH.md](../../TECH.md) section 4 and [D10](../decisions/D10-api-json-rpc.md).

## Where it lives

| Item | Value |
|---|---|
| Socket | `$XDG_RUNTIME_DIR/lucy/lucy.sock`, else `$TMPDIR/lucy-<uid>/lucy.sock` (`/tmp/lucy-<uid>/` on Linux without a runtime dir) |
| Lock | `lucy.lock` beside the socket; held while a daemon runs |
| Modes | Runtime dir 0700, owned by the user, not a symlink; socket 0600 |
| Database | As in the [database runbook](database.md) |
| Network | None. The daemon listens only on the socket; remote HTTPS with device pairing, scoped credentials and TLS lands in m1 (LUCY-VIEW-006) |

## Run, check, stop

```sh
lucy daemon                 # foreground; logs to stderr; Ctrl-C or SIGTERM stops it
lucy status                 # version, API version, uptime, event head, refused connections, you:
lucy events --after 0       # the log, oldest first; --limit up to 500
lucy daemon stop            # graceful stop through the API (recorded as an api.command event)
```

Every start and stop is in the log as `daemon.started` and `daemon.stopped` (reason `signal`, `api`
or `error`); every mutating call as `api.command`, appended before the call runs.

## Policy decisions

The daemon decides privileged actions through its policy engine and records every decision as a
`policy.decision` event in the `policy` stream before returning it. `policy.evaluate` asks for a
decision as the caller (on the socket, the owner); `policy.decisions` pages the recorded ones,
each naming its outcome (`cleared`, `denied`, or `holding` when it waits on the owner), rule, reason and rule set version and
SHA-256. Facts (posture, checks, approvals, declared roots) come only from the daemon; until the spec
model and the forge pollers supply them, merges and deletes are denied or holding.

A decision with rule `engine.error` means the engine failed safe: the facts could not be gathered or
a rule failed. The daemon's log has the cause. A changed rule set is a new `ruleset_version`; compare
`ruleset_sha256` across decisions to see which rules decided.

## When something is wrong

| Symptom | Cause and fix |
|---|---|
| `lucy daemon is not running` | No socket, or nobody listening on it. Start `lucy daemon`; check `--socket` and `XDG_RUNTIME_DIR` match between the two shells |
| `another lucy daemon is running` | The lock is held. `lucy status` shows its pid; `lucy daemon stop` stops it |
| `insecure runtime directory ... want 0700` | The dir is readable by others, a symlink or another user's. Fix the mode as printed, or remove a dir someone else planted in `/tmp` |
| `... exists and is not a socket` | A file sits at the socket path. lucy never removes anything but a stale socket; move the file away |
| `socket path too long` | The path passes the OS limit (107 bytes on Linux, 103 on macOS). Set `XDG_RUNTIME_DIR` or `--socket` to a shorter path |
| A client hangs up with no answer, and `refused_connections` grows | A process of another user tried the socket; the peer check refused it. The daemon logs each refusal with the peer's uid and pid |
| `internal error` with a `request_id` | The daemon's stderr log has the details under that id; the client never sees them |

## Add an API method

1. Declare it in `internal/api/methods.go`: name (dotted lower-case), summary, group (`hello`,
   `query` or `command`), scope, `Mutating`, and params and result types. Put bounds in `lucy` tags
   (`min`, `max`, `maxlen`, `maxitems`, `itemmin`, `itemmax`, `required`) and a `doc` tag per field.
2. Implement it in `internal/server/handlers.go` and bind it in `bindAll`; `server.New` refuses a
   table and handlers that disagree.
3. Add the wrapper to `internal/api/client` and the CLI command, if any.
4. Run `mise run api-schema` and commit `internal/api/openrpc.json`.
5. Only additive changes inside v1: a new method, or a new optional result field. Removing or
   changing a field, or a new required param, is v2 ([D10](../decisions/D10-api-json-rpc.md)).
6. Return `*api.Error` (built with `api.NewError` or `api.FieldError`) for anything about the
   caller's input; any other error becomes an internal error with a correlation id.
