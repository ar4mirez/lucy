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

- The Pilot's file-tool guard allows `$`-prefixed route files such as `app/routes/$postId.tsx`:
  only `${`, `$(` and a path element starting with an environment-style name (`$HOME`, `$TMPDIR-1`,
  `$HOME.bak`) count as a variable reference.
