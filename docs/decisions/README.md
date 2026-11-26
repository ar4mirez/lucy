---
id: decisions
title: Decisions
status: active
version: 1
owner: ar4mirez
requirements: []
---

# Decisions

Architecture decision records (ADRs). Each records one decision Angel made, why, and what it changes.
Decisions are never edited away: a changed decision gets a new version with a "Revisions" entry, or a
new ADR whose front matter says `supersedes`.

| ID | Decision | Status |
|---|---|---|
| [D1](D1-language-go.md) | Go, headless, Linux and macOS | accepted |
| [D2](D2-first-milestone.md) | m1 runs one org (ar4mirez) end to end (revised) | accepted |
| [D3a](D3a-berth-sandbox.md) | berth is the org sandbox, through its API | accepted |
| [D3b](D3b-ai-sdlc-spec.md) | Adopt as much of the ai-sdlc spec as possible | accepted |
| [D3c](D3c-credential-free-workers.md) | Credential-free workers; a runtime seam for cloud orgs | accepted |
| [D4](D4-lean-validation.md) | lucy's own lean validation pipeline | accepted |
| [D5](D5-session-ownership.md) | lucy owns every session; five viewers; remote control | accepted |
| [D6](D6-licence-and-name.md) | Apache-2.0, the name lucy, credit in README and NOTICE | accepted |
| [D7](D7-distribution-mise.md) | Distribute through mise and GitHub Releases | accepted |
| [D8](D8-built-in-vs-external.md) | Built-in vs external inventory; full setup | accepted |
| [D9](D9-doc-structure.md) | Repository doc structure and doc rules | accepted |
| [R1](R1-adversarial-review.md) | Adversarial architecture review, adopted in full | accepted |

## Format

Front matter: `id`, `title`, `status` (`accepted`, `superseded` or `draft`), `version`, `owner`,
`requirements` (the IDs this decision drives), `decided` (date), and `supersedes` or
`superseded_by` when they apply. Body: **Context**, **Decision**, **Consequences**, and
**Revisions** when the decision changed. All decisions here were made by Angel on 2026-11-26.
