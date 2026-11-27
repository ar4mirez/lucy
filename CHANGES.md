---
id: changes
title: Changes
status: active
version: 6
owner: ar4mirez
requirements: [LUCY-REPO-005]
---

# Changes

Changes since the last release. When a version is cut, its section moves to
`docs/changes/<version>.md` and a fresh `## Unreleased` starts here ([rotation](docs/changes/README.md)).

## Unreleased

### Added

- The org sandbox seam, the start of m1 slice 3: a Sandbox provider runs an org's container, named
  by an ai-sdlc `AdapterBinding` with `interface: Sandbox`, and berth is the first one, reached only
  through its documented API on its socket. Starting, restarting or stopping an org's container
  stops the work in it, so each goes to the policy engine first, as the new `container` action
  kind with the sessions live in that container as facts (policy rule set version 6, OpenRPC
  revision 8). With none it is cleared; with live sessions it holds until the owner confirms
  stopping exactly those sessions, a confirmation lapses when a session starts or ends and is used
  once, and no agent may ask. While a cleared restart or stop runs, no new session is placed in that
  container: a runner in a container does not count that org's runs as waiting, and if it leases
  meanwhile it is told the sandbox is busy and leases again later. `runner.queued` takes the
  runner's placement for this; without one it counts as before. Every
  decision is recorded before the container is touched, and what berth did after it. The daemon does not use the seam yet, so nothing acts on a live container,
  and the owner's confirmation has no API or command yet.
