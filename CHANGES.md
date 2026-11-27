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
  through a symlink, is refused without asking, and shell or fetch prompts are still refused
  (OpenRPC revision 4).
