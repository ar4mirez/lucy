---
id: D11
title: "D11: how lucy loads spec resources and chains its audit log"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-SPEC-001, LUCY-SPEC-002, LUCY-SPEC-003, LUCY-SPEC-004, LUCY-SPEC-005, LUCY-SPEC-006, LUCY-SPEC-007, LUCY-SPEC-009, LUCY-SPEC-011]
decided: 2026-11-26
---

# D11: how lucy loads spec resources and chains its audit log

## Context

[D3b](D3b-ai-sdlc-spec.md) adopts the ai-sdlc spec, pinned at commit
`b56cecaeef256644c3066fae2106af5c7eaa684a`, as lucy's config format. Its m0 part is the resource
envelope with a required namespace, the spec/status split, Conditions, validation on load against
vendored draft 2020-12 schemas (unknown fields refused, defaults applied), lucy's own kinds under
its own apiVersion, and the audit hash chain (RFC 8785 + SHA-256) passing the spec's cross-SDK
fixture. Config is untrusted input: loading must be size-limited, must not expand YAML aliases and
must never fetch a schema over the network. The implementation choices below were delegated to
m0 slice 6 within those rules; the owner can revise them.

## Decision

1. **Validator: `github.com/santhosh-tekuri/jsonschema/v6`.** It implements draft 2020-12 in full
   and passes the official JSON Schema test suite, is maintained, and needs only the standard
   library and `golang.org/x/text` (for its messages). Schemas are compiled from files embedded in
   the binary; its loader is replaced by one that refuses every URL, so a `$ref` lucy did not embed
   is a build error, not a fetch. Formats are asserted, not just annotated. It does not apply
   defaults, so lucy applies them itself from the compiled schema (following `properties`, `$ref`,
   `allOf`, `items` and `additionalProperties`; never into `anyOf`, `oneOf` or `if` branches, and
   never onto a required member), then validates again. Rejected: `google/jsonschema-go`, whose
   default application does not follow `$ref`, and every metadata in these schemas is a `$ref`.
2. **YAML: `go.yaml.in/yaml/v3`, read as a node tree, never into Go values.** It is the maintained
   continuation of `gopkg.in/yaml.v3` (which lucy used for tests and now drops). lucy walks the
   parsed nodes itself and refuses anchors, aliases and merge keys outright, so a document can never
   expand into more than it says; it also refuses custom and binary tags, duplicate and non-scalar
   keys, NaN and infinities, and keeps timestamps as strings. Files are capped at 1 MiB, 64
   documents and 100,000 nodes, a directory tree at 1,024 files, 16 MiB and 8 levels, and symlinks
   below the config directory are not followed (the directory itself may be one). Rejected: capping alias expansion, because no lucy resource needs aliases.
3. **lucy's kinds use the apiVersion `lucy/v1alpha1`.** The spec forbids its own `ai-sdlc.io`
   prefix for custom kinds and does not require a DNS name; a bare group claims no domain lucy does
   not own. Airspace, Runway, Flight and Pilot start as schema stubs; an Airspace's namespace is its
   own name. Like the spec's, this version has no stability guarantee before 1.0.
4. **Status never comes from a file.** A document with a `status` member is refused; the daemon
   records each resource's `Valid` condition as `resource.status` events, and the projection stamps
   `lastTransitionTime` from the event's time, so replays agree. Recorded schema-violation messages
   keep only the paths, since a violation message can quote a value from the file.
5. **The audit chain is a projector over the event log.** Every event becomes one schemaVersion 2
   entry in the same transaction: actor, type as action, `stream/<stream>` as resource, decision
   `allowed`, and in `details` the event's seq, stream, type and the payload's SHA-256, plus the
   actor type `metrics.md` asks for. A `policy.decision` names the decided action, the target
   (`org/<org>/project/<project>/<resource>`), the rule, and maps `cleared` to `allowed` and both
   `denied` and `holding` to `denied`, with lucy's outcome in `details`. Verification recomputes
   every hash and link and checks each entry is exactly what its event maps to. lucy verifies only
   schemaVersion 2: legacy chains belong to the SDKs that wrote them.
6. **Another spec version is an explicit migration.** Only `ai-sdlc.io/v1alpha1` at the pinned
   commit is admitted. A bump vendors the new files, updates `PROVENANCE`, and adds the new
   apiVersion to the kind table with a migration into the internal model in the same change.

## Consequences

- Three new direct dependencies: the validator, `golang.org/x/text` and the YAML module; the
  test-only `gopkg.in/yaml.v3` goes. All are pure Go, so releases stay `CGO_ENABLED=0`.
- The vendored files keep their Apache-2.0 licence, listed in NOTICE and in `PROVENANCE` with their
  SHA-256s; a test fails if one changes without its entry.
- The chain lives in the same database as the events it covers, so it detects edits, not a full
  rewrite by someone who can rewrite both and recompute every hash. Anchoring the head hash outside
  the database comes with signed flight logs (LUCY-VAL-007).
- A changed event-to-entry mapping is a new projector version: the chain is rebuilt from the log,
  and its hashes change with it.
