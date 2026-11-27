---
id: tech-sandbox
title: The org sandbox and the runtime seam
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-SBX-001, LUCY-SBX-002, LUCY-SBX-003, LUCY-SPEC-016]
---

# The org sandbox and the runtime seam

How lucy runs an org's container (TECH.md section 6). The seam is `internal/sandbox`; berth, the
first provider, is `internal/sandbox/berth` ([D3a](../decisions/D3a-berth-sandbox.md)).

- **Provider.** A `sandbox.Provider` runs org containers: it reads an org's container (`Status`)
  and runs an operation on it (`Do`: `up`, `restart` or `down`), without deciding anything itself.
  It is the ai-sdlc `Sandbox` interface (LUCY-SPEC-016): an `AdapterBinding` resource with
  `interface: Sandbox` names the provider's `type`, the major `version` of its contract and its
  `config`, and `sandbox.Providers` opens it. A new runtime, a cloud runner say, is a new provider
  type registered there; nothing in the core changes (LUCY-SBX-002). A binding's config never holds
  a credential.
- **The guard.** Nothing calls a provider's `Do` but `sandbox.Sandbox`, because each operation
  stops the work running in the container. It lists the sessions live in the org's container (the
  runner broker's leased runs placed there, `runner.Broker.LiveInContainer`; a lease that has run
  out but is not yet recorded as expired still counts) and asks the policy engine with the
  `container` action kind and those sessions as facts. The decision is recorded on the `policy`
  stream and a `sandbox.requested` event on `sandbox/<org>` before the provider is called, and
  what the provider did is recorded as `sandbox.acted`. With no live session the operation is
  cleared; with live sessions it is holding, and `Confirm` records the owner's confirmation
  (`sandbox.confirmed`, actor `owner`): an approval bound to that operation on that container and
  to exactly the sessions the owner was shown, so a session that starts or ends since makes it
  lapse, and a cleared request retires every earlier confirmation of that operation, used or not.
  Only the daemon and the owner may ask; an agent's request is denied (LUCY-SBX-003). One operation
  runs at a time.
- **The hold.** A cleared `restart` or `down` first holds the container in the runner broker
  (`runner.Broker.HoldContainer`): one store transaction checks that the container's live sessions
  are still exactly those decided on and records `container.held` on `container/<org>`. If they
  changed, nothing is recorded or done and the requester asks again. While held, the broker leases
  no run of that org to a runner placed in a container: such a runner is given the oldest queued run
  of an org that is not held, and when every queued run's org is held it is refused with a typed
  "sandbox busy" refusal (`runner.ErrSandboxBusy`, the API's `sandbox_busy` error, -32007, on
  `runner.lease`), which the runner takes as lease again later. Host runners are not held. The Sandbox records `container.released` once the
  provider is done, whether it succeeded or failed. The hold is a projection of the log, so a
  daemon restart rebuilds it, and it expires after `sandbox.HoldTTL` (15 minutes, which also bounds
  the provider's operation), so a daemon that dies mid-operation cannot leave it stuck. `up` needs
  no hold: no runner is inside a stopped container. `sandbox.acted` and the release are recorded
  even when the request's context ended, since the provider may already have acted.
- **berth.** The provider talks to berth only through berth's documented HTTP API v1, on its Unix
  socket (no token: only its owner can open it) or over TLS with a bearer token. It reads
  `GET /v1` (`berth.api/v1`) and an org's sheet `GET /v1/orgs/{org}` (`berth.info/v1`: `running` or
  `stopped`), and runs `POST /v1/orgs/{org}/{up,restart,down}` with berth's own confirmation,
  `confirm` set to the org's name, which it sends only when the guard has cleared the operation.
  Failures are `berth.error/v1` documents, mapped onto the sandbox's errors (404 not found, 401 and
  403 refused, 409 not in a state for this, 5xx unavailable). An answer of another schema version, an unknown state,
  an answer for another org, an error without its document or a body over 1 MiB is refused as a
  contract break, never guessed at. Its binding takes `socket` only, for now.
- **The contract.** `internal/sandbox/berth/testdata/berth-api-v1.json` is an unchanged excerpt of
  berth's OpenAPI document with the operations lucy uses. The tests check every request the client
  sends against its request schemas, and every answer of the fake berth server
  (`berthtest`, which also applies berth's documented rules: unknown arguments, a missing
  `confirm`, a read-only server) against its response schemas. Tests and CI use only the fake.

## Not built yet

- TODO(LUCY-SBX-001): the daemon opens the org's Sandbox binding and places the in-container
  runner through the seam; idle orgs are stopped. The daemon does not call a provider yet, so
  nothing here acts on a live container.
- TODO(LUCY-SBX-003): the owner's confirmation through the API and the CLI; until then a holding
  operation stays held.
- TODO(LUCY-SBX-001): proving the provider against a real berth needs a real berth on a test
  host; the contract tests run against the fake only.

berth's API has no way yet to start a process inside an org's container (its `berth org exec` has no
endpoint) or to mount lucy's socket there, which placing the in-container runner needs; both are
follow-ups for berth. Its `GET /v1` document's schema name, `berth.api/v1`, is served but not
written in berth's docs.
