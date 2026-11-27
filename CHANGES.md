---
id: changes
title: Changes
status: active
version: 23
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Changed

- A run's HOME is no longer your home directory: every run gets its org's synthetic home, `home/`
  under the runner's `--root` (0700, kept across runs and shared by the org's runs), so a Pilot
  reads nothing of your home. A harness that needs a login of its own finds none there until one is
  set up in that home. The runner refuses an org home that is a symlink, open to others, or is or
  holds your home, and its scratch delete removes only a run's own `run-*` directory.
