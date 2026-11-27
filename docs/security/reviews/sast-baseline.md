---
id: sast-baseline
title: "SAST baseline: reviewed gosec suppressions"
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-SEC-002]
---

# SAST baseline: reviewed gosec suppressions

CI's `gosec` job runs every rule at every severity and confidence over lucy's non-test code, for
Linux and macOS, with no exclusion flags ([security scans runbook](../../runbooks/security-scans.md)).
A finding is fixed, or, when the code is right as it is, suppressed on its line with
`// #nosec G<nnn> -- <reason>`: one rule and a reason each, never a bare or multi-rule tag and never
`nolint:gosec`. This table is the reviewed list of those suppressions. `internal/seccheck` fails
when the code and the table disagree, so a new suppression is always a visible change to this file
in its pull request.

Reviewed in m0 slice 9 against gosec v2.30.0. Of the first scan's 23 findings, 12 were fixed in
the code: ten unchecked `Close`, `Rollback` and `Write` calls on error paths now discard their
error explicitly (G104), and the doc checker reads through an `os.Root` (G122 and a G304). The
other 11 are below. m1 slice 1 added one, the runner's process executor (G204).

| File | Rule | Why it is safe |
|---|---|---|
| `internal/config/config.go` | G304 | Reads the owner's own settings file, from the path the owner's environment names |
| `internal/daemon/runtime.go` | G304 | Opens a constant lock-file name inside the runtime dir, already checked to be 0700 and the user's |
| `internal/doccheck/doccheck.go` | G304 | A test helper reading a constant path under the repository root it is given |
| `internal/eventstore/migrate.go` | G304 | Syncs lucy's own pre-migration snapshot and its state dir, both paths lucy built |
| `internal/eventstore/store.go` | G304 | Opens the database path the owner chose; its directory is checked first and the file's owner and mode right after |
| `internal/spec/load.go` | G304 | Reading the owner's config tree is the loader's job; the walk refuses symlinks and caps file and tree sizes |
| `internal/runner/process.go` | G204 | Runs the program the runner's operator configured (`lucy runner -- <command>`), never text from a task; no shell, and an environment of only PATH, HOME, TMPDIR and `LUCY_RUN_*` |
| `internal/selfupdate/selfupdate.go` | G204 | Runs mise from PATH with fixed arguments and a version that matched `versionRe`; no shell |
| `internal/policy/policy.go` | G101 | `KindCredentialUse` is an action kind's name, `credential_use`, not a credential |
| `internal/policy/rules.go` | G101 | A map of stale-approval reasons keyed by action kind; it holds no credential |
| `internal/sim/world.go` | G404 | The simulation is deterministic from a test seed by design; this generator never makes secrets or tokens |
| `internal/tower/tower.go` | G602 | `Tx.Append` returns exactly one event per payload, so `payloads[i]` is in range |
