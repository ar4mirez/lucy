---
id: runbook-daemon
title: The daemon and its API
status: active
version: 14
owner: ar4mirez
requirements: [LUCY-RUN-001, LUCY-API-001, LUCY-API-003, LUCY-API-005, LUCY-API-006, LUCY-API-007, LUCY-API-008, LUCY-POL-002, LUCY-POL-003, LUCY-SPEC-003, LUCY-SPEC-007, LUCY-SPEC-009]
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

## Config and its status

At start the daemon loads the spec resources in `$XDG_CONFIG_HOME/lucy/resources` (or
`--resources <dir>`; a missing directory means none) exactly as `lucy config validate` does, then
records the outcome in the `config` stream: a `resource.status` event for each resource whose status
changed, `resource.removed` for one no longer in config, and a `config.loaded` summary. It never
writes a config file. Refused resources do not stop the daemon; they are recorded with
`Valid=False` and are not admitted. A directory it cannot read at all, or one over the limits
(1,024 files, 16 MiB, 8 levels), does stop it.

```sh
lucy config validate ~/.config/lucy/resources   # offline: each resource's Valid condition and every violation
lucy config status                              # the daemon's view: resources and their conditions
```

A document that names no resource (it does not parse, lacks a valid kind, name or namespace, or
duplicates another) has no status of its own; `lucy config status` lists the latest load's under
`refused`, with its file and document.

Change config by editing the files and restarting the daemon. Recorded violation messages carry
only the paths; `lucy config validate` shows the full messages.

## The audit chain

Every event is also an audit entry in the ai-sdlc wire format, written in the same transaction and
chained by SHA-256 over RFC 8785 canonical JSON ([D11](../decisions/D11-spec-loading.md)).

```sh
lucy audit verify                       # the daemon's chain, checked against its event log
```

`audit: broken` names the first event whose entry fails and why: the event or its entry was changed
outside lucy, or one of them is missing. Stop the daemon, keep the database file and its
pre-migration snapshots for inspection, and compare against a backup; lucy never repairs a chain.

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

## Runners

A runner leases runs from the daemon over the runner protocol ([TECH.md](../../TECH.md) section 6).
Nothing queues runs through the API yet; the Tower does, through `runner.Runtime`, once the daemon
flies flights.

```sh
lucy runner --org acme -- ./do-the-work.sh   # acme's runner, its id unique to the process; Ctrl-C stops it and its runs
lucy runner --org acme --id spare --ttl 30 --root /path/to/scratch -- ./do-the-work.sh
lucy runner --org acme --harness claude-code # drive Claude Code (`claude` on PATH) over its stream-json protocol
lucy runner host-org beta                    # the owner: the host serves beta from now on (refused while a host run is live)
lucy events --after 0             # run.queued, run.leased, run.event, run.renewed, run.answered, run.finished, run.expired
```

With `--harness claude-code` a run fails without starting unless `claude --version` reports the
pinned version (`lucy runner --help` names it); install that version rather than the newest. The
nightly `harness-check` workflow says whether a newer release is ready for the pin to move.

A runner on the host needs `--org` (`runner_org` without one) and is leased only that org's runs.
Every process on the host runs as one uid and can reach the others' sockets, so the daemon serves
one host org: the first run leased on the host sets it, and a runner on the host for another org
is refused (`host_org`) and exits. Run other orgs with container placement, or switch the host org
with `lucy runner host-org <org>` once no host run is live. `--id` defaults to the org, the host
name and the pid, so two runners never share an id. Each run gets a fresh directory under `--root` (default
`$XDG_DATA_HOME/lucy/runner/<org>`, created 0700; the runner refuses a root, symlinks resolved,
too long for a run's gateway socket and names the longest it takes),
deleted through the path guard when the command exits; the command sees only PATH, HOME, TMPDIR
and the `LUCY_RUN_*` variables. The command runs in its own process group, killed when the run ends
or stops, so nothing it started outlives the run. Closing the runner's terminal (SIGHUP) stops it
like Ctrl-C; start it under `nohup` to keep it running past the terminal. A runner that goes silent for longer than its lease (`--ttl`,
60 s by default) loses the run: the daemon records `run.expired` within 5 s and refuses anything
the runner sends under that fence afterwards (`fenced`). A runner that gets `fenced` drops the run
and stops its work; that is expected after a long pause, not an error to retry.

## Questions

A Pilot's question reaches you only as the Tower's record of it:

```sh
lucy questions                       # every flight that is asking, oldest first
lucy answer f1 use the staging account   # relayed to the Pilot by the Tower; the log keeps a digest
```

The daemon does not host the Tower yet (m1 slice 7a): until it does, `lucy questions` lists
nothing and `lucy answer` is refused as `unavailable`.

## Container operations

Starting, restarting or stopping an org's container stops every session in it, so while a session
is live lucy holds the operation for you:

```sh
lucy sandbox held                       # what waits on you: org, op, decision, the sessions it stops
lucy sandbox confirm acme restart 41    # confirm stopping exactly those sessions, once
```

A confirmation lapses if a session starts or ends before lucy asks again, and a newer decision
replaces the one shown; run `lucy sandbox held` again. The daemon does not open an org's Sandbox
binding yet (m1 slice 3): until it does, nothing is held and a confirmation is refused as
`unavailable`.

## An org's credential gateway

An org's gateway is configured by an `AdapterBinding` in the resources directory
([gateway](../tech/gateway.md)) and runs as its own process, next to the daemon:

```sh
pass show acme/github | lucy vault set --org acme github   # from stdin only; recorded by name in the log
lucy vault ls --org acme                # names and when each was set; never a value
lucy gateway --org acme                 # routes from the daemon; socket gateway/acme.sock, 0600
lucy events                             # each request: gateway.forwarded / gateway.denied / gateway.completed
lucy capability ls                      # the runs' capability tokens, by id; never a token
lucy capability revoke 3f2a9c0d1e4b5a67 lost   # the gateway refuses it from then on
```

A binding the daemon cannot read is logged at start (`gateway binding refused`) and that org's
gateway gets no routes: `lucy gateway` says the daemon has no gateway binding for the org. A route
whose credential the vault does not hold is refused, and the refusal recorded
([vault](../tech/vault.md)). `lucy vault set` refuses to read a value from a terminal: pipe it in. A request without the
run's capability token, or with one revoked, expired or for another run, is refused and recorded
([capabilities](../tech/capability.md)). Do not put a real credential in the vault until the org
keys are wrapped at rest (#32).

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
| `spec: cannot load config` at start | The resources path is not a readable directory, or is over the limits. Fix it, or point `--resources` elsewhere |
| `config loaded ... refused=N` in the log | N documents were refused; `lucy config validate` shows why |
| `runner dropped a run ... why=fenced` in a runner's log | Its lease expired or was replaced while it was cut off. The Tower has already replaced the Pilot; nothing to do |
| `runner kept a run's scratch directory` | The path guard refused the delete (the directory left `--root`, or `--root` is `/`, the home directory or one of its parents). Check `--root` and remove the directory by hand |
| `runner could not kill a run's leftover processes` | Killing the run's process group failed after the command exited. Check with `ps -o pid,pgid,cmd` for processes in that group and stop them by hand |

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
