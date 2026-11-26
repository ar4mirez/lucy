---
id: D6
title: "D6: Apache-2.0 and the name lucy"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-REPO-001, LUCY-REL-007]
decided: 2026-11-26
---

# D6: Apache-2.0 and the name lucy

## Context

lucy is a from-scratch rewrite informed by MIT projects (firstmate, treehouse, no-mistakes, by Kun
Chen) and adopts text and schemas from the Apache-2.0 ai-sdlc spec. Ideas and workflows are free to
reuse; copied code or substantial text keeps its notice.

## Decision

- Licence: Apache-2.0 (explicit patent grant; matches berth, ai-sdlc and Agent Substrate).
- Name: lucy, at `github.com/ar4mirez/lucy` (repository and Go module), later a Homebrew cask in
  `ar4mirez/homebrew-tap` (LUCY-REL-007).
- Credit: README acknowledgements for firstmate, treehouse and no-mistakes (Kun Chen) and the ai-sdlc
  spec, plus NOTICE entries for adopted spec text (LUCY-REPO-001).

## Consequences

- Re-express rather than copy. Any copied MIT code keeps its copyright and permission notice in NOTICE.
- The vendored ai-sdlc schemas keep their licence and get a NOTICE entry when they land.
- The firstmate name and branding are not used; "captain" and "first mate" as plain words are fine.
