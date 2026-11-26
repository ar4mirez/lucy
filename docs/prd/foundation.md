---
id: prd-foundation
title: "PRD: repository, releases, event store, API and policy"
status: active
version: 4
owner: ar4mirez
requirements: [LUCY-REPO-001, LUCY-REPO-002, LUCY-REPO-003, LUCY-REPO-004, LUCY-REPO-005, LUCY-REPO-006, LUCY-REPO-007, LUCY-REPO-008, LUCY-REL-001, LUCY-REL-002, LUCY-REL-003, LUCY-REL-004, LUCY-REL-005, LUCY-REL-006, LUCY-REL-007, LUCY-REL-008, LUCY-EVT-001, LUCY-EVT-002, LUCY-EVT-003, LUCY-EVT-004, LUCY-EVT-005, LUCY-EVT-006, LUCY-API-001, LUCY-API-002, LUCY-API-003, LUCY-API-004, LUCY-API-005, LUCY-API-006, LUCY-API-007, LUCY-API-008, LUCY-POL-001, LUCY-POL-002, LUCY-POL-003, LUCY-POL-004, LUCY-POL-005]
---

# PRD: repository, releases, event store, API and policy

Areas `REPO`, `REL`, `EVT`, `API` and `POL`. Format and ID rules: [PRD README](README.md).
Design background: [TECH.md](../../TECH.md) and the decisions in [docs/decisions](../decisions/README.md).

## REPO: repository and documentation

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-REPO-001 | The repository is Apache-2.0, with a NOTICE file and README acknowledgements | m0 | D6 |
| LUCY-REPO-002 | Every doc carries front matter: id, title, status, version, owner, requirements | m0 | D9 |
| LUCY-REPO-003 | Requirement IDs are stable, defined once, scheduled in one milestone and indexed in docs/requirements.md | m0 | D9 |
| LUCY-REPO-004 | No doc exceeds 400 lines or 24 KB; docs split by area instead of growing | m0 | D9 |
| LUCY-REPO-005 | CHANGES.md keeps an Unreleased section, rotated into docs/changes/ per version | m0 | D9 |
| LUCY-REPO-006 | The README states what lucy will never do (non-goals) | m0 | P2-18 |
| LUCY-REPO-007 | CI on Linux and macOS runs go vet, a gofmt check, race tests and the doc rules | m0 | report 9.1a |
| LUCY-REPO-008 | CI actions are pinned by commit SHA, run with read-only permissions and no secrets; Dependabot watches them | m0 | checklist 4.6 |

## REL: releases and distribution

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-REL-001 | goreleaser builds static binaries (CGO off) for linux and darwin, amd64 and arm64, with checksums | m0 | D7 |
| LUCY-REL-002 | A keyless cosign signature bundle covers checksums.txt | m0 | D7 |
| LUCY-REL-003 | Each archive carries a GitHub artifact attestation (SLSA build provenance) | m0 | D7 |
| LUCY-REL-004 | `mise use -g github:ar4mirez/lucy` installs a release and mise verifies its attestation | m0 | D7 |
| LUCY-REL-005 | Merges to main cut releases from conventional commits; tags trigger goreleaser | m0 | D7 |
| LUCY-REL-006 | `lucy self-update` verifies the new binary and `--to <version>` rolls back | m0 | D7 |
| LUCY-REL-007 | A Homebrew cask ships in ar4mirez/homebrew-tap | m3 | D6, D7 |
| LUCY-REL-008 | Each release archive ships an SPDX SBOM, covered by the signed checksums.txt | m0 | checklist 4.6 |

## EVT: state and event store

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-EVT-001 | State lives in one pure-Go SQLite database (WAL) under the XDG data dir, never in a repo | m0 | report 4.2 |
| LUCY-EVT-002 | Typed events are appended in the same transaction as the state change they describe | m0 | report 4.3 |
| LUCY-EVT-003 | Consumers read past a cursor; an ack carries its generation and is refused if the generation changed | m0 | report 4.3 |
| LUCY-EVT-004 | Schema changes ship as versioned forward migrations, tested on a copy of the previous schema | m0 | P2-14 |
| LUCY-EVT-005 | Events never carry secrets: secret-looking field names are refused, payloads are size-capped and SQL is parameterized | m0 | STRIDE 2-I, checklist 2.1 |
| LUCY-EVT-006 | The append-only log is the source of truth: per-stream optimistic concurrency, and state tables are projections rebuilt by replay | m0 | report 4.2 |

## API: one typed API

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-API-001 | One typed API on a unix socket serves every surface; surfaces hold no logic | m0 | P1-8 |
| LUCY-API-002 | The API offers hello, query, subscribe from a sequence, attach, input and command | m0 | report 9d.2 |
| LUCY-API-003 | The socket is 0600 in the user's runtime dir and checks the peer's credentials | m0 | STRIDE 2-S |
| LUCY-API-004 | Every call checks the caller's scope and the object's org and task through the policy engine | m0 | checklist 2.5 |
| LUCY-API-005 | Every command becomes an audit entry naming the actor and device | m0 | STRIDE 1-R |
| LUCY-API-006 | Requests are strict typed JSON: unknown fields rejected, sizes bounded, no native deserialization | m0 | checklist 2.7 |
| LUCY-API-007 | The API is versioned (major version in the path, negotiated by hello) and its schema is generated from one method table and committed | m0 | P1-8, D10 |
| LUCY-API-008 | API errors are typed: one fixed message per code, details only about the caller's input, internal error text never sent | m0 | checklist 2.2, D10 |

## POL: policy engine

| ID | Requirement | Milestone | Source |
|---|---|---|---|
| LUCY-POL-001 | Deterministic Go policy evaluates every privileged action (merge, destructive op, credential use, cross-org access); default deny | m0 | P0-2 |
| LUCY-POL-002 | Models can only request; only decision records made through the API authorize anything | m0 | P0-2 |
| LUCY-POL-003 | Every allowed action records the rule, actor and decision that allowed it | m0 | P0-2 |
| LUCY-POL-004 | Outside text is tagged untrusted with its source and can never raise privileges | m0 | P0-2 |
| LUCY-POL-005 | Merge postures (captain, green with exceptions, client-confirm) are typed data per org and project | m0 | report 3 |
