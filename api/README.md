# API Contracts

Toget has several wire contracts; this directory holds the local one and
points at the others.

| Contract | Location | Transport | Encoding | Used by |
| --- | --- | --- | --- | --- |
| **Local API** | `api/openapi.yaml` | HTTP on `127.0.0.1:7718` or a Unix socket | JSON | Local web UI, CLI, Agents on the same machine |
| **Node protocol** | `internal/node/`, `docs/protocol.md` | HTTPS and HTTP/3 over QUIC on `7717` | JSON envelopes | Nodes on the same LAN or a direct path |
| **Relay API** | `internal/relay/`, `docs/relay.md` | HTTPS, WebSocket tunnel, HTTPS long-poll | JSON envelopes + opaque frames | Nodes and relays across networks |
| **Account host API** | `internal/hosted/`, `docs/protocol.md` | HTTPS on `7722` with a pinned host key | JSON + OPAQUE messages | Official-hosted accounts and the reset page |
| **Directory API** | `internal/directory/`, `docs/protocol.md` | HTTPS on `7720` | JSON + signed manifests | Clients resolving a server code |

The local API is what a user or a local program can inspect and script. The
other contracts are what machines speak to each other. They share domain types
but not wire formats.

## Local API

- `openapi.yaml` is the source of truth.
- Authentication: a per-session token written to a user-only file, plus `Origin` checks for browser requests.
- Errors use a compact, stable shape: `{"error": {"code": "...", "message": "..."}}`.
- Timelines use a sequence cursor (`after` plus `limit`); list endpoints return
  the current bounded set.
- Approval consumption is single-use and bound to the exact action digest.
  Idempotency keys for arbitrary task creation are not implemented yet.

The implementation covers health, updates, session, local and official
account creation, host login, password change, key rotation, account sync,
recovery files, devices, rooms, events, messages, invites, node sync, business
cards and QR codes, attachments, search, read cursors, audit, Agents,
approvals, Agent policies, tools, backups, staged restores, and content-free
Web Push notifications. `openapi.yaml` documents every route currently
registered in `internal/api/server.go`. When the two disagree, the route table
is authoritative; update the YAML in the same change.

Planned generated output (not wired yet):

- Go: `internal/api/` types and server stubs
- TypeScript: `web/packages/api-client/`

For the first local slice, the handlers in `internal/api/` and the web client
in `web/app/src/api.ts` are written by hand against this document. Client
generation and the CI drift check land before the API grows further.

## Node protocol

- The implemented wire format is JSON envelopes, documented in
  [docs/protocol.md](../docs/protocol.md) and implemented in `internal/node`,
  `internal/relay`, and `internal/directory`.
- `.proto` files under `proto/` are a reserved schema for a future compact
  framing; generated Go lives in `internal/protocol/`. Protobuf is not on the
  wire today, so `docs/protocol.md` is authoritative for interoperability.
- `buf lint` and `buf breaking` run in CI against the previous release.
- HTTP/3 over QUIC and HTTPS fallback are implemented for nodes and relays;
  relay sessions use a WebSocket tunnel with HTTPS long-poll fallback.
- The end-to-end envelope (outer header and encrypted inner message) is
  described in [docs/protocol.md](../docs/protocol.md).

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
