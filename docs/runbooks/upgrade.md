---
id: runbook-upgrade
title: Upgrading lucy in place
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-OPS-005]
---

# Upgrading lucy in place

**When:** a new lucy release is out and runs are live. **Needs:** lucy installed through mise, the
daemon, its runners and gateways running as your user. **Result:** every process on the new
version, and no run or session lost (LUCY-OPS-005).

A daemon restart stops no runner and no session: runners keep their work going and retry while
the daemon is down, and the new daemon, before it expires anything, moves each lease that was live
when the old one stopped later by the outage, so each runner carries on under its fence
([daemon](daemon.md)). Runners and gateways keep running the binary they started with until they
are restarted, so the daemon goes first and they follow.

## Steps

1. **Back up** lucy's data directory (`~/.local/share/lucy` by default) while lucy runs, for
   example with `sqlite3 ~/.local/share/lucy/lucy.db '.backup /path/outside/lucy/lucy.db.bak'`.
   Only a backup taken before the upgrade lets you go back (see Rollback).
2. **Install** the new version: `lucy self-update` (or `lucy self-update --to <version>`). Nothing
   running changes yet.
3. **Restart the daemon.** Under a service manager, restart its unit; by hand, `lucy daemon stop`
   then `lucy daemon`. It migrates the database if the release needs it (taking a snapshot first,
   [database](database.md)) and rebuilds its projections from the log. Leave the runners alone:
   they reconnect within a second of the daemon answering.
4. **Check the daemon:** `lucy status` shows the new version; its log names how many leases it
   moved ("runner leases resumed after a restart", with the outage); `lucy events` shows a
   `run.resumed` for each; `lucy audit verify` reports the chain verified.
5. **Move the runners** to the new binary without losing work: `lucy runner drain --org <org>`
   waits until each runner has finished its runs and exited (it leases nothing more meanwhile);
   then start each again (`lucy runner --org <org> ...`, or let the service manager restart it).
   Queued runs wait for the new runners. Upgrading from 0.6.0, do this step as in "From 0.6.0"
   instead.
6. **Restart each gateway** (`lucy gateway --org <org> --passphrase-file <file>`); it unlocks the
   vault again at start.

## From 0.6.0

A 0.6.0 runner predates the drain: it never registers in `runners/`, so `lucy runner drain` does
not find it ("no live runner matches"), and SIGTERM or a service-manager restart stops it at once
and reports its runs lost. 0.6.0's runners also name no org, and runners now must: after the daemon
is upgraded, a 0.6.0 runner keeps every run it holds, renewing and finishing it as before, but its
next lease is refused (`runner_org`), so it leases nothing new.

So, for an upgrade from 0.6.0, step 5 is instead: leave each 0.6.0 runner running until every run
it holds has ended (in `lucy events`, each `run.leased` naming that runner is followed by its run's
`run.finished`, `run.expired` or `run.replaced`), then stop it and start a new runner with `--org`
(`lucy runner --org <org> ...`). From then on step 5 applies as written.

## Rollback

`lucy self-update --to <previous>` installs the older version, but going back is safe only to the
backup from step 1: the new daemon may have migrated the database and recorded events the older
one does not know. Stop lucy, move the data directory's database files aside (do not delete them),
restore the backup, install the older version and start it. Runs recorded after the backup are
lost to the older daemon; their runners are then fenced and the Tower replaces their runs once.

## Checked by

`TestUpgradeFromV060` (internal/daemon) starts this daemon on a store that v0.6.0's own code wrote
with a run leased (`testdata/upgrade/v0.6.0.sql`): the lease is moved, the runner's heartbeat and
finish under its old fence are accepted, a lease without an org is refused, and the audit chain
verifies across both versions.
