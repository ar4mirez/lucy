---
id: changes
title: Changes
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- Repository skeleton: Go module `github.com/ar4mirez/lucy`, `lucy --version` and `lucy --help`.
- Documentation structure: PRODUCT, TECH, AGENTIC, EXTENSIONS, SECURITY, PRD by area with stable
  requirement IDs, milestone briefs m0-m4, decision records, runbooks, threat model.
- Doc rules enforced by `go test ./internal/doccheck`: front matter, size cap, requirement IDs,
  generated index, links.
- CI on Linux and macOS (vet, gofmt, race tests, doc rules) with pinned actions, and Dependabot.
