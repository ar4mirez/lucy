---
id: D10
title: "D10: the one API is restricted JSON-RPC 2.0 over HTTP, with a generated OpenRPC schema"
status: accepted
version: 1
owner: ar4mirez
requirements: [LUCY-API-001, LUCY-API-002, LUCY-API-006, LUCY-API-007, LUCY-API-008]
decided: 2026-11-26
---

# D10: the one API is restricted JSON-RPC 2.0 over HTTP, with a generated OpenRPC schema

## Context

[R1](R1-adversarial-review.md) P1-8 asks for one typed API, "JSON-RPC or Connect over the socket, the
same over HTTPS", behind the CLI, TUI, dashboard, MCP and remote control (LUCY-API-001). m0 slice 4
had to pick one. The choice was delegated to the slice with a rule: the least dependency weight that
still gives typed Go clients and a schema other surfaces can generate from. Unlike D1 to D9, this
ADR was written by the slice's agent under that rule; the captain can revise it.

## Options

| | JSON-RPC 2.0 over HTTP/1.1 | Connect (connectrpc.com) |
|---|---|---|
| Go dependencies | None: `net/http` and `encoding/json` | `connectrpc.com/connect`, `google.golang.org/protobuf` |
| Build toolchain | `go test` regenerates the schema | `buf` and `protoc-gen-go`, `protoc-gen-connect-go` pinned through mise |
| Schema | OpenRPC 1.3 (JSON Schema), generated from the Go method table | Protobuf, the source of truth |
| Typed Go client | Hand-written thin wrappers over one `Call` | Generated |
| Other surfaces | Any JSON client; OpenRPC generators for TypeScript | Connect clients for TypeScript, Swift, Kotlin |
| Streaming (`subscribe`, `attach`) | Not in JSON-RPC: a separate WebSocket or NDJSON endpoint | Server streaming built in (HTTP/2 for bidi) |
| Strict decoding (LUCY-API-006) | Ours to enforce, and testable at the byte level | Protobuf JSON is strict; binary protobuf is a second format to fuzz |
| MCP fit | MCP is itself JSON-RPC 2.0, so `lucy mcp` maps tools almost one to one | A translation layer |

## Decision

- **JSON-RPC 2.0, restricted,** one request per HTTP POST to `/rpc/v1`, served by the same
  `net/http` handler on the unix socket now and on HTTPS from m1. Restrictions: no batches, no
  notifications (an id is required, string up to 128 bytes or an integer), params only by name,
  `Content-Type: application/json` only, bodies up to 64 KiB, nesting up to 32 levels, no duplicate
  keys (compared case-insensitively, because Go matches fields that way), no unknown members or
  fields, exact-case member names, declared bounds on every field (LUCY-API-006).
- **One declaration.** The method table in `internal/api/methods.go` names each method, its scope,
  whether it mutates, and its params and result Go types; bounds live in `lucy` struct tags used by
  both the validator and the schema generator. `internal/api/openrpc.json` is generated from it
  (`mise run api-schema`), committed, checked current by a test, and served by `rpc.discover`.
  Surfaces in other languages generate from that file (LUCY-API-007).
- **Versioning.** The major version is in the path (`/rpc/v1`) and negotiated by `hello`. Within a
  major version only additive changes: new methods and new optional result fields. The server stays
  strict about params; clients ignore unknown result fields. A breaking change is `/rpc/v2`, served
  alongside v1 until no surface needs v1 (LUCY-API-007).
- **Errors.** JSON-RPC's codes plus lucy's in -32099..-32000 (forbidden, unsupported version, too
  large, unavailable, timeout). Each code has one fixed message and a stable `kind`; `data` holds
  only the field at fault, help about the caller's input, the supported versions, or a correlation
  id. Internal error text is logged and never sent (LUCY-API-008).
- **Streaming** (`subscribe`, `attach`) will be a separate streaming endpoint beside `/rpc/v1` (NDJSON
  over the socket, WebSocket remotely), described in the same OpenRPC document. TODO(LUCY-API-002):
  it lands in a later m0 slice-4 PR.

## Consequences

- No new module for the API; the one dependency the slice adds is `golang.org/x/sys`, already in the
  build through `modernc.org/sqlite`, made direct for the peer-credential syscalls on Linux and macOS.
- The Go client is written by hand. It is small (one `Call` plus a wrapper per method) and a test
  checks every declared method has a server handler of the declared params type.
- If streaming or a second-language client later costs more than Connect would, a new ADR can move
  to Connect; the method table, scopes and error kinds carry over unchanged.
