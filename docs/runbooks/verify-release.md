---
id: runbook-verify-release
title: Verifying a release
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-REL-002, LUCY-REL-003, LUCY-REL-004, LUCY-REL-008]
---

# Verifying a release

**When:** before trusting a lucy download you did not install through mise, or to audit one you did.
**Needs:** [cosign](https://github.com/sigstore/cosign) 2.4 or newer and the [GitHub CLI](https://cli.github.com)
(`mise use -g cosign gh`). No keys: the signature is keyless and names the workflow that made it.

## With mise (the normal path)

`mise use -g github:ar4mirez/lucy@<version>` checks the archive against `checksums.txt` and verifies
its GitHub attestation before installing ([mise settings](https://mise.jdx.dev/configuration/settings.html):
`github.github_attestations` and `github.slsa`, both on by default). `lucy self-update` runs mise
with both forced on, and needs mise 2026.10.19 or newer: the first release whose attestation and
SLSA checks fail closed. With an older mise it refuses and installs nothing; run `mise self-update`.

## By hand

Download the archive for your platform, `checksums.txt` and `checksums.txt.sigstore.json` from the
release (`gh release download v<version> --repo ar4mirez/lucy`), then:

1. **The checksums file is signed by lucy's release workflow, from a tag:**
   ```sh
   cosign verify-blob checksums.txt \
     --bundle checksums.txt.sigstore.json \
     --certificate-identity "https://github.com/ar4mirez/lucy/.github/workflows/release.yml@refs/tags/v<version>" \
     --certificate-oidc-issuer https://token.actions.githubusercontent.com
   ```
   Expect `Verified OK`. Any other identity, issuer or a modified file fails.
2. **Your archive matches the signed checksums:**
   ```sh
   sha256sum --ignore-missing -c checksums.txt      # macOS: shasum -a 256 --ignore-missing -c checksums.txt
   ```
   Expect `lucy_<version>_<os>_<arch>.tar.gz: OK`.
3. **The archive was built by that workflow from this repository (SLSA provenance):**
   ```sh
   gh attestation verify lucy_<version>_<os>_<arch>.tar.gz \
     --repo ar4mirez/lucy \
     --signer-workflow ar4mirez/lucy/.github/workflows/release.yml
   ```
   Expect `Verification succeeded!`. The same works for `checksums.txt` and each `.spdx.json` SBOM.

## Check the result

All three commands succeed. If any fails, do not run the binary: report it privately as
[SECURITY.md](../../SECURITY.md) describes.
