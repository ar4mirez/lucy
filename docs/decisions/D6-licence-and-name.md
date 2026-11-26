---
id: D6
title: "D6: Apache-2.0 and the name lucy"
status: accepted
version: 2
owner: ar4mirez
requirements: [LUCY-REPO-001, LUCY-REL-007]
decided: 2026-11-26
---

# D6: Apache-2.0 and the name lucy

## Context

lucy is a from-scratch rewrite informed by MIT projects by Kun Chen (named in the README
acknowledgements) and adopts text and schemas from the Apache-2.0 ai-sdlc spec. Ideas and workflows are free to
reuse; copied code or substantial text keeps its notice.

## Decision

- Licence: Apache-2.0 (explicit patent grant; matches berth, ai-sdlc and Agent Substrate).
- Name: lucy, at `github.com/ar4mirez/lucy` (repository and Go module), later a Homebrew cask in
  `ar4mirez/homebrew-tap` (LUCY-REL-007).
- Credit: README acknowledgements for those MIT projects (Kun Chen) and the ai-sdlc spec, plus
  NOTICE entries for adopted spec text (LUCY-REPO-001).

## Consequences

- Re-express rather than copy. Any copied MIT code keeps its copyright and permission notice in NOTICE.
- The vendored ai-sdlc schemas keep their licence and get a NOTICE entry when they land.
- Those projects' names, branding and role vocabulary are not used outside the acknowledgements;
  lucy has its own terms ([GLOSSARY.md](../../GLOSSARY.md)).

## Revisions

- **v2 (2026-11-26):** lucy adopted its own vocabulary (the glossary) instead of reusing the role
  words of the projects it learns from; credit stays in the README and NOTICE.
