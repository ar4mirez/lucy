---
id: runbook-daemon
title: The daemon and its API
status: active
version: 28
owner: ar4mirez
requirements: [LUCY-RUN-001, LUCY-RUN-005, LUCY-OPS-005, LUCY-API-001, LUCY-API-003, LUCY-API-005, LUCY-API-006, LUCY-API-007, LUCY-API-008, LUCY-POL-002, LUCY-POL-003, LUCY-SPEC-003, LUCY-SPEC-007, LUCY-SPEC-009]
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
lucy runner drain --org acme                 # each acme runner finishes its runs, leases nothing more and exits
lucy runner --org acme --id spare --ttl 30 --root /path/to/scratch -- ./do-the-work.sh
lucy runner --org acme --harness claude-code # drive Claude Code (`claude` on PATH) over its stream-json protocol
lucy runner host-org beta                    # the owner: the host serves beta from now on (refused while a host run is live)
lucy events --after 0             # run.queued, run.leased, run.event, run.renewed, run.resumed, run.answered, run.finished, run.expired, run.replaced
```

With `--harness claude-code` a run fails without starting unless `claude --version` reports the
pinned version (`lucy runner --help` names it); install that version rather than the newest. The
nightly `harness-check` workflow says whether a newer release is ready for the pin to move.

Every runner needs `--org` (it refuses to start without one; the daemon refuses such a lease with
`runner_org`) and is leased only that org's runs.
Every process on the host runs as one uid and can reach the others' sockets, so the daemon serves
one host org: the first run leased on the host sets it, and a runner on the host for another org
is refused (`host_org`) and exits. The owner switches the host org with
`lucy runner host-org <org>` once no host run is live. `--placement container` is refused until
berth can prove a runner's placement (berth#216). `--id` defaults to the org, the host name and
the pid, so two runners never share an id. Each run gets a fresh directory under `--root` (default
`$XDG_DATA_HOME/lucy/runner/<org>`, created 0700; the runner refuses a root, symlinks resolved,
too long for a run's gateway socket and names the longest it takes),
deleted through the path guard when the command exits; the command sees only PATH, HOME, TMPDIR
and the `LUCY_RUN_*` variables. A run of a project whose Runway names a `repository` works in its
own slot of the daemon's worktree pool (`worktrees/` in lucy's data directory), checked out on its
own `lucy/<run>-<hash>` branch at the Runway's `defaultBranch`, rather than in the empty scratch
directory, which stays its TMPDIR. The daemon reserves the slot with the lease and prepares it in
the background (a first clone of a large repository can take minutes; up to 30); the runner holds
the run, heartbeating, and starts the command once the slot is ready, or fails the run with the
reason if the preparation fails. When the run ends the slot is given back if its work landed
(pushed, merged, or nothing beyond the base); otherwise it is quarantined with its work kept, and
the next run gets a new slot. `lucy worktree ls` shows every slot and why a quarantined one is
held; push or merge its branch and the next run of the repository frees it, or remove it with
`lucy worktree discard <org>/<repo>/<slot> --confirm <org>/<repo>/<slot>` (its branch stays in the
clone, `worktrees/<org>/<repo>/base.git`, until git gc, so `git -C` there can still recover its
commits). Its HOME is the org's synthetic home, `home/` under `--root`
(0700, kept across runs and shared by them), never yours, so a Pilot reads nothing of your home
directory; whatever a harness needs there, such as its own login, is set up in that home. A root
is bound to its org on first use (`.org` in it), and `lucy runner` for another org refuses it
before it dials the daemon (exit 2), so give each org's runner its own `--root`. The command runs
in its own process group, killed when the run ends
or stops, so nothing it started outlives the run. To restart or upgrade a runner without losing
work, drain it: SIGTERM, or `lucy runner drain` (which waits until it has exited), makes it lease
nothing more and exit once its runs end; then start the new one. Ctrl-C, a second SIGTERM or
closing its terminal (SIGHUP) stops it at once, and its runs are reported lost for the Tower to
replace once; start it under `nohup` to keep it running past the terminal. A runner that goes silent for longer than its lease (`--ttl`,
60 s by default) loses the run: the daemon records `run.expired` within 5 s and refuses anything
the runner sends under that fence afterwards (`fenced`). A runner that gets `fenced` drops the run
and stops its work; that is expected after a long pause, not an error to retry.

Restarting the daemon does not stop runners or their runs: a runner keeps its work going and
retries while the daemon is down. On start, before it expires anything, the daemon takes the time
of the newest event in the log as when it stopped and moves every lease still live then later by
the outage, leaving it at least 60 s (`run.resumed`; the log line "runner leases resumed after a
restart" counts them and names the outage), and a runner that heartbeats in time carries on under
its fence. A lease that had already run out before the daemon stopped is not moved. Until its
runner renews the lease, the run's capability token is refused. A moved lease is not moved again
by another restart, so a runner that is really gone is expired when its moved lease ends. A run
whose flight queues a newer Pilot generation is fenced at once (`run.replaced`).

## Questions

A Pilot's question reaches you only as the Tower's record of it:

```sh
lucy questions                       # every flight that is asking, oldest first
lucy answer f1 use the staging account   # relayed to the Pilot by the Tower; the log keeps a digest
```

The daemon hosts the Tower and steps the flights in its log whenever the log changes, and at
least every minute while one is active, but it dispatches no Flight unless started with
`lucy daemon --dispatch`. With it, at start it dispatches every Flight in the config directory
whose `Ready` condition is True and that it does not fly yet, by the Flight's name, starting its
Pilot through the runner protocol. A Flight that is not Ready is never dispatched.

A flight whose step fails backs off on its own, 1 s doubling to at most 5 minutes, and is not
stepped again before then; the other flights keep stepping. The daemon logs each failure
(`flight step failed`), and `flights.list` shows the flight's `step_error` and `retry_at`. The
backoff is kept in memory only: a restarted daemon retries every flight at once.

`lucy config status` shows each Ready Flight's `Dispatched` condition:

| Status / reason | Meaning |
|---|---|
| True / `Dispatched` | this start dispatched it |
| True / `InFlight` | the Tower is flying it from its log, dispatched by an earlier start, with or without `--dispatch` now |
| False / `DispatchDisabled` | nothing flies it and the daemon was started without `--dispatch` |
| False / `TakeoverGate` | its Runway names a repository whose remote, symlinks resolved, is not a `file://` URL or absolute path under lucy's data directory or the system temp directory; real repositories are refused until runs have an OS boundary of their own (#36) and the owner's go/no-go |
| False / `NameInUse` | a flight of another org or runway holds its name; rename the Flight |
| False / `Finished` | the flight with its name is finished (grounded, takeoff or merged outside); a new Flight needs a new name |
| False / `PipelineInvalid` | the org's Pipeline for the Flight's shape (`ship` or `research` in its namespace) is invalid: the spec loader rejected it, or it has a field lucy refuses; the message names it and why. lucy never falls back to its built-in Pipeline; fix the org's Pipeline and restart |
| False / `AutonomyInvalid` | the Flight's posture cannot be set: its Runway names an AutonomyPolicy or level that is not admitted, a `requireApproval` other than `all` or `security-critical-only`, or a field lucy refuses; or the Flight's `autonomy` is looser than its Runway's |
| False / `WorktreePoolInvalid` | the org's `WorktreePool` cannot be accepted: the spec loader rejected it, it has a field lucy refuses (`rootDir` among them), or the org declares two; the message names it and the field |
| False / `DispatchFailed` | the Tower refused it; the message says why |

The Tower's forge is a stub that reads no pull request and merges nothing until the validation
slice. The gate is checked again whenever a run is leased: a run whose Runway names a real remote
gets the gate as its `worktree_error`, with nothing reserved or cloned, and fails without starting.
The gateway refuses every git push (see [gateway](../tech/gateway.md)).

`lucy flights` lists every Flight the daemon loaded with its readiness, its `Dispatched` condition
and the Tower's state of it (the API's `flights.list` adds a failing flight's step error);
`lucy flights --json` writes each as the spec's `BacklogTask` document, one per line, for a tool
that reads the spec's backlog.

## Decisions

Everything that waits on you is a decision, projected from the event log into the spec's Decision
resource (RFC-0035) and closed only by your recorded word:

```sh
lucy decisions                 # what waits on you, oldest first, with the command that settles each
lucy decisions --all --json    # every decision, answered, superseded and archived ones too, as spec resources
lucy approve fix-login         # approve, as you, the merge a flight holds for
```

A Pilot's question is settled with `lucy answer`, a merge that holds for your approval with
`lucy approve`, and a container operation that holds for your confirmation with
`lucy sandbox confirm`; a merge whose retries kept failing holds for your approval too. Nothing
expires a decision into an answer and no agent can settle one: new facts that end the wait (a newer
question or request, a new head, a clearance that no longer holds) supersede it, and a flight that
ends archives its open decisions.

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
pass show acme/github | lucy vault set --org acme github   # from stdin only; asks for the vault passphrase on the terminal
lucy gateway --org acme --passphrase-file /path/outside/lucy/vault-passphrase  # headless: a 0400 file outside lucy's data directory
lucy gateway --org acme --passphrase-file ${CREDENTIALS_DIRECTORY}/lucy-vault  # under systemd, with LoadCredential=lucy-vault:<path> in the unit
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
([capabilities](../tech/capability.md)). Each org's key is wrapped under the vault passphrase
([D14](../decisions/D14-vault-passphrase-kek.md)): `lucy vault set` and `lucy gateway` need it,
from `--passphrase-file` or the terminal, and nothing opens
without it. The first `set` on a vault from before keys were wrapped sets the passphrase and wraps
its keys. A lost passphrase cannot be recovered: keep it in your password manager.

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
