---
id: tech-policy
title: "lucy: the policy engine"
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-POL-001, LUCY-POL-002, LUCY-POL-003, LUCY-POL-004, LUCY-POL-006, LUCY-POL-007]
---

# lucy: the policy engine

Section 5 of the [technical specification](../../TECH.md), split out by area to keep TECH.md under
the size cap.

"LLM proposes, policy disposes" (LUCY-POL-001): models and agents only request; deterministic Go in
`internal/policy` decides every privileged action. No policy language is embedded: the rules are a
small Go table, and the spec model's `AutonomyPolicy` and `QualityGate` resources (m1,
LUCY-SPEC-010) feed them data, not code.

- **Request.** Actor (id, role, org: set by the daemon from the authenticated caller), kind (`read`,
  `write`, `merge`, `delete`, `force_push`, `branch_delete`, `history_rewrite`, `credential_use`,
  `exec`, `fetch`; no rule allows the last two yet),
  target (org, project, resource) and inputs, each with its source (`owner`, `config`, `daemon`,
  `task` are trusted; `agent`, `tool_output`, `repo_content`, `issue`, `pr_body`, `pr_comment`,
  `web`, `email` are not). The requester never sets trust (LUCY-POL-004).
- **Facts.** What the daemon knows from trusted sources: posture (owner by default), check state,
  unreported required checks, fixed-list categories, a PR's head SHA, base ref and base SHA, the
  credential's scope, default branch, home, declared roots, the symlink-resolved delete target and
  owner approvals. Approvals come only from decision records and name two digests: the action's
  (`action_sha256`, over actor, kind, target), so one cannot be reused for another action, and the
  bound facts' (`facts_sha256`, the SHA-256 of `{"kind":..., "facts":{...}}` holding a merge's head
  SHA, base ref and base SHA, a delete's resolved path, a credential use's scope, or the default
  branch for force-push, branch delete and history rewrite), so an approval lapses when those
  change: a matching approval made under other facts is a deny, not an escalation (LUCY-POL-002,
  rule set version 2; version 3 renamed the outcomes, roles and postures to the glossary's terms;
  version 4 only annotated two names for the static analyser).
- **Rules, in order; the first with an opinion decides:** `request.valid`, `facts.valid`,
  `org.isolation` (an org-bound role acts only in its org), `taint.trusted-basis` (a privileged
  request needs a trusted input), `read.in-org`, `write.worker-only`, `credential.gateway-only`,
  `merge.role`, `merge.head-known` (deny when the PR's head SHA is unknown), `merge.checks-green` (never red, pending, unknown or with a required check
  unreported), `merge.closing-refs`, `merge.fixed-list` (needs the owner's approval, then lets
  the posture decide, so a client-posture fixed-list merge needs both the owner's approval and
  the relayed client confirmation), `merge.posture`, `destructive.role`, `delete.path-guard`,
  `delete.scratch` (cleared), `delete.project` (owner), `git.default-branch` (denied; the target
  must be a plain branch name by git check-ref-format rules and is compared case-insensitively),
  `git.owner`, and `default.deny`.
- **Decision** (a clearance). `cleared`, `denied` or `holding` (waiting on the owner), with the
  rule, a reason, every approval it rested on (`approvals_used`, in the order the rules used them;
  empty unless cleared), whether any input was untrusted, and the rule set's version and SHA-256 (LUCY-POL-006). The hash
  covers every non-test source file of `internal/policy` (embedded, in sorted order) and the path
  guard's version;
  `internal/policy/testdata/ruleset.sum` records each version's hash and a test refuses a changed
  rule set under an old version. `Evaluate` is pure; a panic, an unknown outcome or facts the
  daemon could not gather are denied under `engine.error`.
- **Recording.** `policy.evaluate` appends a `policy.decision` event (stream `policy`) before it
  returns the decision; an unrecorded decision is never returned (LUCY-POL-003).
  `policy.decisions` pages them. TODO(LUCY-POL-001): the privileged executors of m1 (merge, delete,
  push, the gateway) call the engine and act only on a recorded clearance.
- **Path guard** (`internal/pathguard`, LUCY-POL-007): refuses `/`, `$HOME` and its ancestors, the
  roots themselves, relative paths, `.`, `..` and empty components, control, invisible and
  lookalike characters, and anything not strictly inside a declared root, checking both the
  requested spelling and the symlink-resolved path; roots that are `/`, home or contain home are
  refused when declared. A delete inside the task's scratch root is cleared; inside a project it
  needs the owner. TODO(LUCY-SEC-005): every delete lucy makes goes through it, deleting the
  resolved path without following links (m1).
