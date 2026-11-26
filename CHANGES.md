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
- Release pipeline: goreleaser builds static lucy binaries for linux and darwin (amd64, arm64) with
  `checksums.txt` and an SPDX SBOM per archive; a `v*` tag publishes them to GitHub Releases with a
  keyless cosign signature over the checksums and GitHub build-provenance attestations. CI runs
  `goreleaser check` and a snapshot build on every pull request. No release is published yet.
- Install with `mise use -g github:ar4mirez/lucy@latest`.
- `lucy self-update` updates lucy through mise (attestation checks forced on); `--to <version>` rolls
  back and `--check` reports whether a newer release exists.
- `lucy --version` also prints the commit and its date in release builds.
- Runbooks for cutting a release and for verifying one (`cosign verify-blob`, `gh attestation verify`).
