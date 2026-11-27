---
id: tech-gateway
title: The credential gateway
status: active
version: 1
owner: ar4mirez
requirements: [LUCY-CRED-001, LUCY-CRED-004, LUCY-CRED-005]
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
- **Headers.** The worker's own credential and forwarding headers (`Authorization`, `Cookie`,
  `X-Api-Key`, `Proxy-Authorization`, `Forwarded`, `X-Forwarded-*` and the like) and hop-by-hop
  headers are removed before the route's credential is added. So are `Accept-Encoding`, `Range`
  and `If-Range`, and the upstream is asked for `Accept-Encoding: identity`, so a worker cannot ask
  for an answer the scrubber cannot read whole.
- **Policy and audit.** Each forwarded request is a `credential_use` by the org's gateway, decided
  by the policy engine (rule `credential.gateway-only`, the route's scope as a fact, the worker's
  request as an untrusted `agent` input, so the record is marked tainted) and recorded on the
  `policy` stream with a `gateway.forwarded` event, in one transaction, before anything is sent.
  The outcome follows as `gateway.completed` (status, how many echoed copies were scrubbed, or that
  the call failed). A refused request is a `gateway.denied` event with its reason. Paths are
  recorded redacted as `policy.RecordedResource` records a fetch; no event holds a credential, a
  query string or a body. Every event is in the audit chain.
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

## Not built yet

- TODO(LUCY-CRED-002): the org's credentials come from the per-org encrypted vault (slice 4b);
  `gateway.Credentials` is the seam, and tests use made-up credentials only.
- TODO(LUCY-CRED-003): a worker's request carries its capability token, scoped to (org, task,
  action, expiry), and the gateway checks it before a route is used (slice 4b).
- TODO(LUCY-CRED-001): a `lucy gateway` process per org, listening where only that org's container
  reaches it, and the routes as config; the worker's tools (git, the forge CLI, the harness) pointed
  at it.
- TODO(LUCY-CRED-005): `lucy preflight --check` scans containers' environments and mounts for
  tokens (slice 15).
