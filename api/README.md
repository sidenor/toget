# API Contracts

Toget has two contracts, and this directory holds one of them.

| Contract | Location | Transport | Encoding | Used by |
| --- | --- | --- | --- | --- |
| **Local API** | `api/openapi.yaml` | HTTP on `127.0.0.1:7718` or a Unix socket | JSON | Local web UI, CLI, Agents on the same machine |
| **Node protocol** | `proto/toget/v1/` | QUIC on `7717`, WSS/TCP `443` fallback | Protobuf | Nodes, devices, relays, account hosts |

The local API is what a user or a local program can inspect and script. The node protocol is what machines speak to each other. They share domain types but not wire formats.

## Local API

- `openapi.yaml` is the source of truth.
- Authentication: a per-session token written to a user-only file, plus `Origin` checks for browser requests.
- Errors use a compact, stable shape: `{"error": {"code": "...", "message": "..."}}`.
- Pagination is cursor-based (`after` plus `limit`).
- Idempotency keys for task creation and approval decisions.

The first implementation covers health, status, account, rooms, events, and
messages. Agents, approvals, devices, relay settings, and audit endpoints are
still design-only; they will be added to the same document as they land.

Planned generated output (not wired yet):

- Go: `internal/api/` types and server stubs
- TypeScript: `web/packages/api-client/`

For the first local slice, the handlers in `internal/api/` and the web client
in `web/app/src/api.ts` are written by hand against this document. Client
generation and the CI drift check land before the API grows further.

## Node protocol

- `.proto` files under `proto/` are the source of truth.
- Generated Go: `internal/protocol/`
- Generated TypeScript: `web/packages/proto/`
- `buf lint` and `buf breaking` run in CI against the previous release.
- The end-to-end envelope (outer header and encrypted inner message) is defined in `proto/toget/v1/envelope.proto` and described in [docs/protocol.md](../docs/protocol.md).

## Generation

Protobuf types are generated and checked in:

```bash
task generate
```

CI runs the same command and fails if the working tree changes. OpenAPI client
generation is not wired yet; until it is, changes to `openapi.yaml` and the
hand-written handlers must be reviewed together.

## Versioning

- **Local API:** `/api/v1`; additive changes are allowed within `v1`; removing or renaming a field requires `/api/v2`.
- **Node protocol:** integer version negotiated in the handshake; the current and previous two minor app versions remain compatible.
- **Unknown fields:** preserved when forwarding, ignored when reading.
- **Breaking changes:** a new protocol version and an overlap period of at least six months.

See [docs/protocol.md](../docs/protocol.md) for the full protocol and [docs/roadmap.md](../docs/roadmap.md) for when each piece lands.
