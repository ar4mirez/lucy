---
id: readme
title: lucy
status: active
version: 3
owner: ar4mirez
requirements: [LUCY-REPO-001, LUCY-REPO-006]
---

# lucy

lucy is a headless supervisor for autonomous coding agents: one Go binary that runs a per-user daemon,
keeps its state in SQLite with an append-only event log, and dispatches, supervises, validates and
lands agent work across several organisations, each hard-isolated in its own sandbox. You talk to it
through its CLI, a TUI, a web dashboard, Claude chat (MCP) or a paired phone; models propose, and
deterministic policy decides what actually happens.

## Status

**m0 (foundation) in progress.** This repository holds the skeleton, the signed release pipeline, the
event store (not yet wired to a command) and the design docs; the binary only prints its version and
help and updates itself. Nothing here runs agents yet. See the
[m0 brief](docs/milestones/m0.md) for what lands next and [PRODUCT.md](PRODUCT.md) for the plan.

## Install

lucy installs through [mise](https://mise.jdx.dev) from GitHub Releases, on Linux and macOS (amd64
and arm64). No release is published yet; the release pipeline is merged and the captain cuts the
first tag.

```sh
mise use -g github:ar4mirez/lucy@latest     # or a pinned version: github:ar4mirez/lucy@0.1.0
lucy self-update                            # update through mise; --to <version> rolls back
```

Each release has a keyless cosign signature over its `checksums.txt`, a GitHub build-provenance
attestation and an SBOM per archive. mise checks the checksum and the attestation before installing;
to verify by hand, see [verifying a release](docs/runbooks/verify-release.md).

To build from source: `mise install && mise run build`, then `./bin/lucy --version`.

## Non-goals

lucy will not:

- emulate a terminal: herdr, tmux, `lucy attach` or a browser widget display sessions;
- serve a team: it serves one captain, so there is no SSO or multi-user tenancy;
- let a model authorize anything privileged: merges, destructive operations, credential use and
  cross-org access are decided by deterministic policy;
- absorb worker tools such as gh-axi or Lavish, or the berth sandbox: they stay separate, managed
  products behind a sharp provider boundary;
- expose anything publicly by default: the API listens on loopback and the tailnet only;
- support Windows, or ship a Kubernetes provider before a cloud org needs one.

## Documentation

| Doc | What it holds |
|---|---|
| [PRODUCT.md](PRODUCT.md) | What lucy is, who it serves, goals and the milestone plan |
| [TECH.md](TECH.md) | Architecture, state layout, dependencies, repository layout |
| [AGENTIC.md](AGENTIC.md) | How agents work in and with lucy, and the documentation rules |
| [EXTENSIONS.md](EXTENSIONS.md) | Provider seams, config resources and extension points |
| [SECURITY.md](SECURITY.md) | Reporting vulnerabilities, and the security model |
| [AGENTS.md](AGENTS.md) | Instructions for coding agents working in this repository |
| [CHANGES.md](CHANGES.md) | Changes since the last release |
| [docs/](docs/requirements.md) | Requirements index, PRD, milestones, decisions, runbooks, security |

## Acknowledgements

lucy is a from-scratch rewrite that takes the learnings of tools it replaces. It is informed by:

- [firstmate](https://github.com/kunchenguid/firstmate), [treehouse](https://github.com/kunchenguid/treehouse)
  and [no-mistakes](https://github.com/kunchenguid/no-mistakes) by Kun Chen (MIT): the supervision
  contract, the worktree pool and the validation pipeline that lucy's designs learn from;
- the [AXI](https://github.com/kunchenguid/axi) agent-interface conventions by Kun Chen (MIT), which
  lucy's CLI follows;
- the [ai-sdlc](https://github.com/ai-sdlc-framework/ai-sdlc) specification (Apache-2.0), whose
  resource model, readiness check, independent review and signed proofs lucy adopts;
- [berth](https://github.com/ar4mirez/berth), the org sandbox lucy drives, and
  [Agent Substrate](https://github.com/agent-substrate/substrate), whose credential-free sandbox
  pattern lucy follows.

No code from these projects is copied today; [NOTICE](NOTICE) lists attributions and will carry the
notice for anything adopted verbatim.

## Licence

[Apache-2.0](LICENSE). Copyright 2026 the lucy authors.
