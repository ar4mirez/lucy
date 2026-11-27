---
id: changes
title: Changes
status: active
version: 4
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- `runner.permit`: a Pilot's prompted file tool (read, edit or write) on a path inside its run's
  directory is decided by the daemon's policy engine, with the Pilot as a ship worker of the run's
  org, and the decision is recorded before the tool runs; a path outside the directory, also
  through a symlink, a path or glob that could be expanded or climb out (`~`, a variable
  reference, a brace group, `..`) and a write to the harness's own `.claude` configuration are
  refused without asking, and shell or fetch prompts are still refused (OpenRPC revision 4).
- Conformance scenario trust-04 runs: a Pilot's tool request is tagged by the channel it entered
  through, the run's task and the Pilot's own output, whatever its text claims, and its decision
  is marked tainted. The corpus now has 59 executable scenarios.

### Changed

- A Claude Code session loads no user, project or local settings, only lucy's own: nothing is
  pre-allowed, and hooks, skills and slash commands are off. A repository's committed `.claude`
  settings, hooks or MCP config can no longer widen what a Pilot may do, and every tool prompt
  still reaches lucy. A contract test runs a session in a fixture repository whose settings
  pre-allow Bash, add a hook and enable its `.mcp.json` server, and checks that none takes effect.
  So a Pilot still follows a repository's agent instructions, lucy reads the run directory's
  `CLAUDE.md` (or `AGENTS.md` when there is none) and the files it imports with `@path`, and
  appends them to the session's system prompt, framed as repository text that grants no
  permission. Imports are followed one level deep and only inside the run directory (no `..`,
  absolute path or symlink out); a file over 64 KiB, one that is not UTF-8 text without NUL bytes,
  or one that would take the total over 96 KiB,
  is dropped whole, so the prompt fits in one command-line argument. The run directory is an empty
  scratch directory until m1 slice 6 checks the repository out into it, so for now nothing is
  injected.
- The Pilot's file-tool guard allows `$`-prefixed route files such as `app/routes/$postId.tsx`:
  only `${`, `$(` and a path element starting with an environment-style name (`$HOME`, `$TMPDIR-1`,
  `$HOME.bak`) count as a variable reference.
