---
id: runbook-database
title: The database, its migrations and event types
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-EVT-001, LUCY-EVT-002, LUCY-EVT-003, LUCY-EVT-004, LUCY-EVT-005, LUCY-EVT-006, LUCY-SEC-001]
---

# The database, its migrations and event types

**When:** adding an event type or a schema migration, upgrading or rolling back a database, or
investigating one. **Needs:** the repository and `mise install` for changes; for operations, the
machine where lucy runs. The code is `internal/eventstore`; the design is in
[TECH.md](../../TECH.md) section 3.

## Where it lives

| Item | Value |
|---|---|
| File | `$XDG_DATA_HOME/lucy/lucy.db`, else `~/.local/share/lucy/lucy.db` (Linux) or `~/Library/Application Support/lucy/lucy.db` (macOS) |
| Snapshots | `$XDG_STATE_HOME/lucy/`, else `~/.local/state/lucy/` (Linux) or `~/Library/Application Support/lucy/state/` (macOS) |
| Modes | Files 0600, directories 0700, all owned by the user. WAL and shared-memory files (`-wal`, `-shm`) take the file's mode |
| Refused | Running as root; a looser mode or another owner; a symlinked file; a database or snapshot directory inside a git work tree |

lucy never loosens or fixes a mode on its own: it refuses to open and names the `chmod` to run
(LUCY-SEC-001).

## Durability policy

- **WAL mode, `synchronous=FULL`:** every commit syncs the WAL before it returns, so an append or an
  ack that returned survives a crash or power loss. `fullfsync` and `checkpoint_fullfsync` are on, so
  macOS flushes the drive cache too (`F_FULLFSYNC`); Linux ignores them.
- **One writer connection** with `BEGIN IMMEDIATE`: writes are serialized inside the process and
  never deadlock on a lock upgrade; other processes wait up to 10 s (`busy_timeout`).
- **Readers** use separate `query_only` connections and see the last committed state.
- Tested: a writer killed with SIGKILL mid-stream leaves an intact database holding every append that
  had returned (`TestCrashKeepsCommittedEvents`). Power loss is covered by the `synchronous` setting,
  not by a test.

## Adding an event type

1. Define a Go struct and give it `EventType()` (lower-case dotted, for example `pr.checks`) and
   `EventVersion()` (start at 1). Register it in the registry the daemon builds.
2. Never put a secret in a payload: events are kept forever and read by every consumer. A field name
   ending in `password`, `passwd`, `passphrase`, `secret`, `token`, `apikey`, `privatekey`,
   `accesskey`, `sessionkey`, `signingkey`, `credential(s)`, `cookie` or `bearer`, or named
   `authorization`, `auth`, `pat` or `otp` (case, `_` and `-` ignored), is refused at registration
   and again at append, including map keys (LUCY-EVT-005). Store a reference (`secret_ref`) instead.
   Every value reaches SQL as a bound parameter; `TestHostileInputIsData` round-trips stream,
   actor, consumer and payload strings written to break out of SQL and checks the schema and log
   are untouched.
   Name non-secret fields so they do not trip the rule: `token_count`, `fence`.
3. Keep payloads small: at most 64 KiB of JSON. Logs, diffs and transcripts go elsewhere, with a
   reference in the event.
4. To change a payload's shape, add a new struct with `EventVersion()` + 1 and register both: old
   events stay readable as the old version. Decoding is strict; unknown fields are an error.

## Adding a projection

A projector owns tables prefixed with its name, creates them in `Reset`, folds one event in `Apply`,
and uses bound parameters for every value. `Apply` runs inside the append's transaction and may read
only the event and its own tables. Bump `Version()` whenever its tables or logic change: the store
rebuilds the projection from the log when it opens.

## Adding a schema migration

Migrations are forward-only. A released migration is never edited or removed.

1. Add `internal/eventstore/migrations/NNNN_name.sql` with the next number. Plain SQL, `STRICT`
   tables; it runs in one transaction, so a failure leaves the schema untouched.
2. Run `go test ./internal/eventstore -run 'TestSchemaGolden|TestMigrationsLocked|TestUpgradeFromFixtures' -update`.
   It rewrites `testdata/schema.sql`, appends the migration's checksum to `testdata/migrations.sum`,
   and writes `testdata/upgrade/v<N>.sql`, a dump of a database at the new version, which is never
   regenerated afterwards.
3. Review the three diffs, then run `mise run check`. `TestUpgradeFromEveryVersion` upgrades a copy
   of a database at every earlier version; `TestUpgradeFromFixtures` upgrades every committed dump
   (LUCY-EVT-004).

Editing an old migration fails `TestMigrationsLocked`; on a user's machine it fails at open with
"an applied migration was changed".

## Upgrading and rolling back

Opening a database with pending migrations first writes a snapshot into the state dir,
`lucy.db.pre-v<N>.<UTC time>.seq<S>.bak` (0600), where `<N>` is the schema version before the
upgrade and `<S>` the last event's sequence number, then applies each migration in its own
transaction. The snapshot is written to a temporary file, synced to disk, linked into place and
the directory synced before any migration runs, so after a crash or power loss it is complete or
absent; every upgrade gets a new file and none is ever overwritten. If the snapshot cannot be
written, the upgrade does not run. To roll back a bad upgrade:

1. Stop lucy.
2. Move `lucy.db`, `lucy.db-wal` and `lucy.db-shm` aside (do not delete them).
3. Pick the newest `lucy.db.pre-v<N>.*.bak` for the version you are going back to (the highest
   `seq<S>` holds the most events), copy it to `lucy.db` and `chmod 600` it.
4. Install the older lucy: `lucy self-update --to <version>`.

Events written after the snapshot are not in it; they are in the files moved aside. Upgrading again
later takes a fresh snapshot of the restored database, so a second rollback loses nothing written
since the first.

Snapshots are kept: lucy deletes none of them. TODO: a retention policy (keep the newest per version,
or by age) lands with `lucy backup`; until then remove old ones by hand.

## When lucy refuses to open the database

| Message | Meaning | Action |
|---|---|---|
| `database schema is newer than this lucy` | A newer lucy migrated it | Update lucy, or roll back as above |
| `an applied migration was changed` | This build's migration differs from the one applied | A build or release bug: report it; do not edit the database |
| `migration history is not contiguous` | `schema_migrations` was edited | Restore a snapshot |
| `insecure database path` | Mode, owner, symlink or git work tree | Run the `chmod` it names, or move the file |
| `lucy does not run as root` | Started with euid 0 | Run as your user |

## Inspecting

Open the file read-only with the SQLite shell, for example
`sqlite3 -readonly ~/.local/share/lucy/lucy.db 'SELECT seq, stream, type FROM events ORDER BY seq DESC LIMIT 20'`.
The `events` table refuses `UPDATE` and `DELETE` through triggers. `lucy sql` (read-only) comes with
the CLI.
