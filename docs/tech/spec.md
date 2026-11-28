---
id: tech-spec
title: "lucy: the spec model and audit log"
status: active
version: 3
owner: ar4mirez
requirements: [LUCY-SPEC-001, LUCY-SPEC-002, LUCY-SPEC-003, LUCY-SPEC-004, LUCY-SPEC-005, LUCY-SPEC-007, LUCY-SPEC-009, LUCY-SPEC-017]
---

# lucy: the spec model and audit log

Section 10 of the [technical specification](../../TECH.md), split out by area to keep TECH.md under
the size cap.

Built in `internal/spec`, `internal/jcs` and `internal/audit` ([D3b](../decisions/D3b-ai-sdlc-spec.md),
[D11](../decisions/D11-spec-loading.md)); operations in the [daemon runbook](../runbooks/daemon.md).

- **Resources.** Config is ai-sdlc resources: `apiVersion`, `kind`, `metadata`, `spec`
  (LUCY-SPEC-001). The core kinds (`Pipeline`, `AgentRole`, `QualityGate`, `AutonomyPolicy`,
  `AdapterBinding`) are `ai-sdlc.io/v1alpha1`, as is `WorktreePool`, an org's worktree pool
  ([worktree](worktree.md)); lucy's (`Airspace`, `Runway`, `Flight`, `Pilot`,
  schema stubs so far) are `lucy/v1alpha1`. `metadata.namespace` is required and names the
  airspace (LUCY-SPEC-002).
- **Schemas.** The spec's schemas at commit `b56ceca` are vendored verbatim under
  `internal/spec/schemas/ai-sdlc`, each with its SHA-256 in `PROVENANCE`, which a test checks;
  lucy's own are under `internal/spec/schemas/lucy`. All are embedded and compiled at first use;
  a `$ref` to anything not embedded fails, and nothing is fetched. Validation is draft 2020-12 with
  formats asserted and unknown fields refused; defaults are applied to admitted resources
  (LUCY-SPEC-004).
- **Loading.** `spec.LoadDir` walks a directory for `.yaml`, `.yml` and `.json` files (hidden ones
  skipped, symlinks refused), decodes each through a YAML node walker that refuses anchors,
  aliases, merge keys and custom tags, and admits each document through `spec.Admit`: envelope,
  known kind, no `status`, namespace present, schema, then defaults. Limits: 1 MiB, 64 documents and
  100,000 nodes per file; 1,024 files, 16 MiB and 8 levels per tree. `lucy config validate` and the
  daemon use the same path.
- **Internal model.** An admitted document becomes a `spec.Resource` keyed by namespace, kind and
  name, with its canonical spec digest. Only the pinned apiVersion is admitted; another needs an
  explicit migration into this model first (LUCY-SPEC-005).
- **Status.** Never read from or written to a file (LUCY-SPEC-003). The daemon records a `Valid`
  condition per resource as `resource.status` events (stream `config`), `resource.removed` when a
  resource leaves config and a `config.loaded` summary; the `resource_status` projection holds
  current conditions, stamping `lastTransitionTime` from event time, and the latest
  `config.loaded` event's problems (refusals with no identity), which `resources.list` returns
  (LUCY-SPEC-007).
- **Audit chain.** The `audit` projector turns each event into a schemaVersion 2 entry, hashed as
  SHA-256 over its RFC 8785 form (`internal/jcs`) and linked to the previous entry's hash, in the
  event's own transaction; the table refuses UPDATE and DELETE. `audit.VerifyStore` rechecks
  every hash and link and that every entry is exactly what its event maps to, up to the log's head
  when it starts; `audit.VerifyJSONL` verifies the spec's cross-SDK sample chains (LUCY-SPEC-009).
- **Core conformance** (LUCY-SPEC-017, spec section 11). The suite's Core fixtures are vendored
  under `internal/spec/testdata/conformance` and `TestCoreConformance` loads each: every valid one
  is admitted once written as an org's config (a namespace, no `status`), every invalid one is
  refused on the schema. lucy refuses a spec-valid document only under its own two rules:
  `MissingNamespace` (LUCY-SPEC-002) and `StatusInConfig` (LUCY-SPEC-003).
