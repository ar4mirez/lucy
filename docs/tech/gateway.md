---
id: tech-gateway
title: The credential gateway
status: active
version: 4
owner: ar4mirez
requirements: [LUCY-CRED-001, LUCY-CRED-004, LUCY-CRED-005, LUCY-SPEC-016]
---

# The credential gateway

How a worker reaches a service without ever holding its credential (TECH.md section 6,
[D3c](../decisions/D3c-credential-free-workers.md)). The code is `internal/gateway`; one
`gateway.Gateway` serves one org and runs outside the org's container.

- **Routes.** A worker calls `http://<gateway>/<route>/<path>` instead of the service. A route
  names an `https://` upstream (host, optional port and base path; no other scheme, no user info),
  the credential's name in the org's store, the scope it grants, the header it goes in
  (`Authorization` by default, with an optional prefix such as `Bearer `), and an allowlist of
  rules: methods on a path prefix that ends at a segment boundary (`/repos/acme/web` allows
  `/repos/acme/web/pulls`, never `/repos/acme/web-other`). Nothing else is forwarded
  (LUCY-CRED-001).
- **Paths are taken as written.** A path with an encoded slash, backslash or dot, a dot segment, an
  empty segment or a control character is refused, not normalised, so what the allowlist matched is
  exactly what the upstream gets.
- **Addresses** (LUCY-CRED-004). The upstream's name is resolved once per request, and the request
  is refused if any address it resolves to is not public: loopback, private, link-local (the
  `169.254.169.254` metadata address), shared address space (`100.64.0.0/10`), reserved,
  documentation, benchmarking, multicast, and IPv6 forms that embed an IPv4 address (mapped, NAT64,
  6to4, Teredo) or a private one (`fc00::/7`, `fe80::/10`). The connection goes only to the
  addresses that were checked, rechecked at dial time, so an answer that changes in between (DNS
  rebinding) is never used. Environment proxies are ignored.
- **Capability.** Every request carries the run's capability token in `X-Lucy-Capability`
  ([capabilities](capability.md)); one without is refused before any credential is read, and the
  daemon checks the token in `gateway.use` before the credential's use is decided. It is never
  forwarded.
- **Headers.** The worker's own credential and forwarding headers (`Authorization`, `Cookie`,
  `X-Api-Key`, `Proxy-Authorization`, `Forwarded`, `X-Forwarded-*` and the like) and hop-by-hop
  headers are removed before the route's credential is added. So are `Accept-Encoding`, `Range`
  and `If-Range`, and the upstream is asked for `Accept-Encoding: identity`, so a worker cannot ask
  for an answer the scrubber cannot read whole.
- **Policy and audit.** Each forwarded request is a `credential_use` by the org's gateway, decided
  by the policy engine (rule `credential.gateway-only`, the route's scope as a fact, the worker's
  request as an untrusted `agent` input, so the record is marked tainted). The decision is recorded
  on the `policy` stream and a `gateway.forwarded` event on the org's `gateway/<org>` stream, in one
  transaction, before anything is sent. Every gateway event is on that org stream.
  The outcome follows as `gateway.completed` (status, how many echoed copies were scrubbed, or that
  the call failed). A refused request is a `gateway.denied` event with its reason. The gateway
  itself turns the worker's path into its recorded form before it calls its recorder, as
  `policy.RecordedResource` records a fetch: redacted, capped, with the SHA-256 of the whole path.
  An empty path is recorded as `(empty path)`. The recorder records it as given, so a path of any
  length is recorded and its hash covers all of it. A method other than GET, HEAD, POST, PUT,
  PATCH, DELETE or OPTIONS is refused before anything else, recorded with the method clipped to 16
  bytes. No event holds a credential, a query string or a body. Every event is in the audit chain.
- **No redirect is followed.** The gateway uses one transport round trip per request; a redirect
  goes back to the worker as it came, so the credential is never sent to a host the route does not
  name.
- **Echoes are scrubbed** (LUCY-CRED-005). Every copy of the credential in the upstream's headers
  and body is replaced by `<redacted>` as the answer streams, including one split across reads,
  so an upstream that reflects the request never hands the credential to the worker. An answer in
  any content coding but identity is refused with a 502 and recorded as `gateway.completed` with
  an error; none of it reaches the worker.
- **Fails closed.** An unknown route, a refused path or address, a credential that cannot be read,
  or a decision that cannot be recorded is a refusal; nothing is forwarded without its record.

## The Pilot's environment

The runner gives a Pilot only `PATH`, `HOME`, `TMPDIR` and the run's `LUCY_RUN_*` variables, and
the harness adapter adds only its fixed switches, whatever the runner's own environment holds. A
test sets a range of credential variables in the runner's environment and checks that none of
their names or values reaches the Pilot (LUCY-CRED-005).

## The gateway process and its config

- **Config.** An org's routes are an ai-sdlc `AdapterBinding` with `interface: SecretStore` and
  `type: lucy-gateway` in the org's namespace (LUCY-SPEC-016), version 1.x, with `config.routes`:
  each a `name`, `upstream`, `credential` (its name, never its value), optional `scope`, `header`
  and `prefix`, and `allow` rules of `methods` (GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS) and a
  `path`. The daemon reads it when it loads config (`gateway.Bindings`). An unknown or misspelt
  key, a value of the wrong type, a route that does not check, or a second gateway binding for the
  same org is logged and that org's gateway gets no routes at all, so it forwards nothing.
- **Recording through the daemon.** A gateway records through a `gateway.Recorder`: the daemon's
  `StoreRecorder` over its event store, which a gateway process reaches over the API as
  `gateway.Remote`. The methods are `gateway.routes` (the org's routes, read), `gateway.use` (decide
  and record a credential use before forwarding), `gateway.deny` and `gateway.complete`, all under
  the `gateway` scope. The daemon decides from its own routes: a use names only the route and the
  host the gateway will call, and a route the daemon does not have for the org, or another host, is
  refused, so a gateway cannot widen what its credentials are used for. Each call is also an
  `api.command` holding only its parameters' digest.
- **`lucy gateway --org <org>`** fetches the org's routes from the daemon, listens on a Unix socket
  (`gateway/<org>.sock` in lucy's runtime directory by default: a 0700 directory that must not be
  a symlink, the socket 0600; a socket still answering is refused, a stale one replaced) and serves
  until a signal. Its credentials come from the org's vault ([vault](vault.md)), for its org only;
  a route whose credential the vault does not hold is refused, and the refusal recorded.

## Not built yet

- TODO(LUCY-CRED-003): the worker's tools present the run's capability token
  ([capabilities](capability.md)); today only a caller holding the runner's token can.
- TODO(LUCY-CRED-001): the gateway's socket reachable from the org's container and nowhere else,
  which needs berth to mount it there (berth#216), and the worker's tools (git, the forge CLI, the
  harness) pointed at it.
- TODO(LUCY-CRED-005): `lucy preflight --check` scans containers' environments and mounts for
  tokens (slice 15).
