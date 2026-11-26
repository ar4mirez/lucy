---
id: extensions
title: "lucy: configuration and extension specification"
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-SPEC-001, LUCY-SPEC-003, LUCY-SPEC-005, LUCY-SPEC-006, LUCY-SPEC-016, LUCY-SBX-002, LUCY-VAL-010, LUCY-BRD-001]
---

# lucy: configuration and extension specification

Status: design. Only lucy's settings file, `config.toml` (section 3), is implemented so far.

## 1. Goals

- Configure lucy with declarative, validated resources, not code.
- Extend lucy through narrow provider seams, so the core stays small
  ([R1](docs/decisions/R1-adversarial-review.md) P2-18).
- Never let an extension or a spec bump break a running org.

## 2. Configuration as spec resources

Config uses the ai-sdlc resource envelope (`apiVersion`, `kind`, `metadata`, `spec`, `status`)
(LUCY-SPEC-001). Files under `$XDG_CONFIG_HOME/lucy/` hold `spec`; lucy's database holds `status`
and never writes back (LUCY-SPEC-003). `metadata.namespace` is the org.

| Kind | apiVersion | Purpose |
|---|---|---|
| `Pipeline` | `ai-sdlc.io/v1alpha1` | Research and ship flows (tasks), failure, credential and approval policies, PR config |
| `AgentRole` | `ai-sdlc.io/v1alpha1` | Supervisor (Approach), ship worker, researcher, reviewer: instructions, tools, blocked paths |
| `QualityGate` | `ai-sdlc.io/v1alpha1` | Hard, soft and advisory gates; the owner is the only override |
| `AutonomyPolicy` | `ai-sdlc.io/v1alpha1` | Merge postures as levels; demotion triggers |
| `AdapterBinding` | `ai-sdlc.io/v1alpha1` | Provider seams (section 4) and the toolchain manifest |
| `Org`, `Project`, `Account`, `Task`, `Session` | lucy's own group | lucy's model, with their own schemas (LUCY-SPEC-006) |
| client-confirm route | lucy's own group | The client-confirm posture, which the spec schema cannot express |

Example (illustrative, validated against the vendored schema once it lands):

```yaml
apiVersion: ai-sdlc.io/v1alpha1
kind: AutonomyPolicy
metadata: { name: merge-on-green, namespace: acme }
spec:
  levels:
    - level: 2
      name: merge-on-green
      guardrails: { requireApproval: security-critical-only, blockedPaths: ["**/.env*"] }
  demotionTriggers:
    - { trigger: red-merge-attempt, action: demote-to-0, cooldown: P4W }
```

The spec is pinned to one commit and mapped through an internal model; moving to a newer spec is an
explicit migration (LUCY-SPEC-005).

## 3. lucy's settings: config.toml

lucy's own settings, as opposed to spec resources, live in `config.toml` in the config directory:
`$XDG_CONFIG_HOME/lucy/config.toml` when `XDG_CONFIG_HOME` is set, else `~/.config/lucy/` on
Linux and `~/Library/Application Support/lucy/` on macOS. A missing file means the defaults.
Package `internal/config` reads it.

| Section and key | Default | Means |
|---|---|---|
| `[you]` `title` | `"Owner"` | How lucy refers to you: the display title of the owner role ([glossary](GLOSSARY.md)) |
| `[you]` `name` | none | Optional; shown with the title, for example `Ada (Commander)` |

```toml
[you]
title = "Commander"
name = "Ada"
```

- **Rendered, never recorded.** The role is `owner` in code, the API, policy and events, and
  recorded text that names you, such as a policy decision's reason, says `{owner}`. Surfaces put
  the title in its place when they render text for you: CLI and TUI output, the dashboard, MCP
  chat replies, notifications and holding messages ("holding: waiting on the Commander: the owner
  posture merges only on the word of the Commander"). `lucy status` shows it as
  `you: Ada (Commander)`.
- **Set by `lucy preflight`** once that command lands (m1, LUCY-OPS-001); until then, edit the
  file.
- **Strict.** The file is a TOML subset: `[section]` headers, `key = "basic"` or `key = 'literal'`
  strings and `#` comments. An unknown section or key, a duplicate, an empty value, a value over 64
  bytes, or a control or invisible character is an error naming the line, never silently ignored.

## 4. Provider seams

Each seam is an ai-sdlc `AdapterBinding` interface (LUCY-SPEC-016). A provider is code inside lucy
or an external process behind a versioned contract test.

| Interface | First provider | Later |
|---|---|---|
| `Sandbox` | berth containers via berth's API | host sandbox fallback; Agent Substrate runner (LUCY-SBX-002) |
| `SecretStore` | lucy's per-org gateway and vault | - |
| `SourceControl` | lucy's GitHub API client | other forges |
| `CodeAnalysis` | lucy's lean pipeline | the no-mistakes skill as an optional import (LUCY-VAL-010) |
| `EventBus`, `AuditSink` | lucy's event log and hash-chained audit | OpenTelemetry export |
| `MemoryStore` | lucy's org-scoped knowledge tables | ai-memory |
| Harness adapter | Claude Code, structured protocol | Codex, ACP harnesses (LUCY-BRD-001) |

## 5. Extension points

- **Instruction overlays:** files in the config overlay dir override bundled prompts and skills, by
  name and version.
- **Skills:** bundled skills are hashed and linked at every spawn; third-party skills are pinned by
  version, source and content hash.
- **Quality gates:** a project declares its build, test, lint, SAST and dependency-scan commands; lucy
  runs them as deterministic gates.
- **Hooks:** harness hooks call `lucy hook <event>`; lucy owns their installation.

## 6. Rules for extensions

- Extensions hold no credentials; they request through the gateway like workers do.
- Extension output is untrusted text and can never raise privileges.
- Every external provider is pinned, health-checked by `lucy preflight --check` and covered by a contract test.
