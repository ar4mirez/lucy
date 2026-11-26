---
id: prd
title: Product requirements
status: active
version: 1
owner: ar4mirez
requirements: []
---

# Product requirements

The PRD is split by area. Each file owns one or more area prefixes and defines its requirements in
tables. [PRODUCT.md](../../PRODUCT.md) says what lucy is and why; these files say what it must do.
The generated [requirements index](../requirements.md) lists every ID in one place.

| File | Areas | Covers |
|---|---|---|
| [foundation.md](foundation.md) | `REPO`, `REL`, `EVT`, `API`, `POL` | Repository, releases, event store, the one API, policy engine |
| [spec.md](spec.md) | `SPEC` | The ai-sdlc spec model |
| [quality.md](quality.md) | `SIM`, `CORP`, `VAL` | Simulation, conformance corpus, lean validation |
| [runtime.md](runtime.md) | `RUN`, `SBX`, `CRED` | Runners, sessions, berth sandbox, credential gateway |
| [surfaces.md](surfaces.md) | `VIEW`, `CAP` | Viewers, remote control, captain surface |
| [operations.md](operations.md) | `OPS`, `ORG`, `BRD` | Setup, doctor, operations, multi-org, breadth |
| [security.md](security.md) | `SEC` | Security items not owned by another area |

## Requirement rules

- **ID format:** `LUCY-<AREA>-<NNN>`, for example `LUCY-API-001`. An ID is never reused or renumbered;
  a dropped requirement keeps its row with the text `Withdrawn: <reason>`.
- **One row per requirement:** `| ID | Requirement | Milestone | Source |`. The text is one sentence
  with no `|`. Milestone is `m0` to `m4`. Source names the decision, review item, checklist item or
  report section it comes from.
- **Defined once:** an ID is defined in exactly one table, and the file's front matter `requirements`
  lists exactly the IDs it defines.
- **Scheduled once:** every ID appears in the front matter of exactly one milestone brief in
  [docs/milestones](../milestones/README.md), and that brief matches the Milestone cell.
- **Referenced freely:** any doc may mention an ID; the doc-rules test fails on an ID nobody defines.
- **Grow by area:** a file near the size cap splits by area (for example `runtime.md` into
  `runners.md` and `credentials.md`), keeping the IDs.

`go test ./internal/doccheck` enforces these rules; [AGENTIC.md](../../AGENTIC.md) has the full list.
