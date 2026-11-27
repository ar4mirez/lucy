---
id: sast-baseline
title: "SAST baseline: reviewed gosec suppressions"
status: active
version: 7
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
other 11 are below. m1 slice 1 added one, the runner's process executor (G204); slice 2 added its
harness version check (G204) and the fake harness's fixture settings and MCP readers (G304).

| File | Rule | Why it is safe |
|---|---|---|
| `internal/config/config.go` | G304 | Reads the owner's own settings file, from the path the owner's environment names |
| `internal/daemon/runtime.go` | G304 | Opens a constant lock-file name inside the runtime dir, already checked to be 0700 and the user's |
| `internal/doccheck/doccheck.go` | G304 | A test helper reading a constant path under the repository root it is given |
| `internal/eventstore/migrate.go` | G304 | Syncs lucy's own pre-migration snapshot and its state dir, both paths lucy built |
| `internal/eventstore/store.go` | G304 | Opens the database path the owner chose; its directory is checked first and the file's owner and mode right after |
| `internal/runner/registry.go` | G304 | A runner creates its own entry, `<runner id>.pid`, 0600 in the runner registry, a 0700 directory checked, with its parent, to be this user's own and not a symlink; the id is a checked runner id, so the name cannot leave the directory |
| `internal/runner/registry.go` | G304 | Reads an entry listed in that registry (refused unless owned by this user), named by a checked runner id, only to see whether a live runner holds its lock and to read its pid and org |
| `internal/spec/load.go` | G304 | Reading the owner's config tree is the loader's job; the walk refuses symlinks and caps file and tree sizes |
| `internal/runner/process.go` | G204 | Runs the program the runner's operator configured (`lucy runner -- <command>`), never text from a task; no shell, and an environment of only PATH, HOME, TMPDIR and `LUCY_RUN_*` |
| `internal/runner/process.go` | G204 | The harness version check runs the same configured program with the adapter's fixed version arguments, the run's environment and a 10 s timeout; no shell |
| `internal/harness/harnesstest/replay.go` | G304 | A test helper: the fake harness reads the settings file of the fixture project its own transcript names |
| `internal/harness/harnesstest/replay.go` | G304 | A test helper: the fake harness reads the `.mcp.json` of the fixture project its own transcript names |
| `internal/selfupdate/selfupdate.go` | G204 | Runs mise from PATH with fixed arguments and a version that matched `versionRe`; no shell |
| `internal/policy/policy.go` | G101 | `KindCredentialUse` is an action kind's name, `credential_use`, not a credential |
| `internal/policy/rules.go` | G101 | A map of stale-approval reasons keyed by action kind; it holds no credential |
| `internal/sim/world.go` | G404 | The simulation is deterministic from a test seed by design; this generator never makes secrets or tokens |
| `internal/tower/tower.go` | G602 | `Tx.Append` returns exactly one event per payload, so `payloads[i]` is in range |
| `cmd/lucy/passphrase.go` | G703 | Lstats the vault passphrase file the owner named; this call is the check that it is a plain file, not a symlink, and only its owner (this user or root) can read it |
| `cmd/lucy/passphrase.go` | G703 | Opens that passphrase file after those checks and after checking it is outside the vault and lucy's data directory |
| `cmd/lucy/passphrase.go` | G304 | The same open: the path is the passphrase file the owner named with `--passphrase-file`, checked as above |
| `internal/vault/vault.go` | G115 | Writes the PBKDF2 iteration count, a small positive constant (600,000; lowered only in tests), as a uint32 in the vault's key-encryption parameters |
