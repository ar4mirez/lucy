---
id: runbook-release
title: Cutting a release
status: active
version: 2
owner: ar4mirez
requirements: [LUCY-REL-001, LUCY-REL-002, LUCY-REL-003, LUCY-REL-004, LUCY-REL-005, LUCY-REL-006, LUCY-REL-008, LUCY-REPO-005]
---

# Cutting a release

**When:** `main` holds changes worth shipping and CI is green on it. **Needs:** push rights for tags
on `ar4mirez/lucy`. Nothing else: there are no signing keys or release secrets
([D7](../decisions/D7-distribution-mise.md)).

## What a release contains

The tag-triggered [release workflow](../../.github/workflows/release.yml) runs goreleaser
([`.goreleaser.yaml`](../../.goreleaser.yaml)) and publishes to GitHub Releases:

| Asset | What it is | Requirement |
|---|---|---|
| `lucy_<version>_<os>_<arch>.tar.gz` | Static binary (`CGO_ENABLED=0`, `-trimpath`) for linux and darwin, amd64 and arm64, with README, LICENSE and NOTICE | LUCY-REL-001 |
| `lucy_<version>_<os>_<arch>.tar.gz.spdx.json` | SPDX SBOM of that archive, made by syft | LUCY-REL-008 |
| `checksums.txt` | SHA-256 of every archive and SBOM | LUCY-REL-001 |
| `checksums.txt.sigstore.json` | Keyless cosign signature over `checksums.txt` (Sigstore bundle) | LUCY-REL-002 |

Each archive, SBOM and `checksums.txt` also gets a GitHub build-provenance attestation (SLSA)
(LUCY-REL-003). `<version>` has no leading `v` (`lucy_0.1.0_linux_amd64.tar.gz`), and each archive
has the binary at its top level: that is what mise's `github:` backend matches (LUCY-REL-004).

## Steps

1. Rotate CHANGES.md: move `## Unreleased` to `docs/changes/<version>.md` and start a fresh
   `## Unreleased` ([rotation](../changes/README.md)), in a PR that merges to `main`.
2. Tag the merge commit on `main` and push the tag:
   ```sh
   git fetch origin && git tag -s v<version> origin/main -m "lucy v<version>"
   git push origin v<version>
   ```
   Only `v*` tags start the workflow, and it refuses a tag whose commit is not on `main`.
   TODO(LUCY-REL-005): a release-PR tool (release-please) that proposes the version and tag from
   conventional commits. It needs a token that can trigger workflows, which is a decision for later.
3. The workflow then, in one job:
   1. builds the archives, SBOMs and `checksums.txt`, signs `checksums.txt` with cosign through
      GitHub's OIDC token, and uploads everything to a **draft** release;
   2. verifies the signature and checksums as a user would, and checks that a tampered
      `checksums.txt` fails (`scripts/verify-dist.sh`);
   3. writes the build-provenance attestations;
   4. publishes the draft. Until this step nothing is visible, so mise never sees an unverified release.

Only the release job has write scopes (`contents`, `id-token`, `attestations`); every other job is
read-only. Tool versions are pinned in the workflow (`GORELEASER_VERSION`, `COSIGN_VERSION`,
`SYFT_VERSION`) and actions by commit SHA; `internal/workflowcheck` fails CI if that slips.

## Check the release

1. The workflow run is green and the release is published (not a draft).
2. Verify it as a user would: [verify-release.md](verify-release.md).
3. On Linux and on macOS: `mise use -g github:ar4mirez/lucy@<version>`, then `lucy --version` prints
   `lucy <version> (<commit>, <date>)`.

## Before a tag: prove the config

Every pull request runs `goreleaser check` and a snapshot build (`--snapshot --clean --skip=sign`,
nothing published) and checks its assets with `scripts/verify-dist.sh` (ci.yml's `release-snapshot`
job). Locally, with the same versions:

```sh
mise x goreleaser@2.20.3 syft@1.57.2 -- goreleaser release --snapshot --clean --skip=sign
scripts/verify-dist.sh dist "$(jq -r .version dist/metadata.json)"
```

## When it fails

- **Before "Publish the release":** nothing is public. Fix the cause on `main`, delete the draft
  release and the tag (`git push origin :refs/tags/v<version>`), and tag again.
- **After publishing:** do not replace assets in place; cut the next patch version.

## Roll back

A user goes back with `lucy self-update --to <previous>`, which runs
`mise use -g github:ar4mirez/lucy@<previous>`. A bad release stays published but is marked as such in
its notes, and the next patch release supersedes it.
