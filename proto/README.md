# Node Protocol

This directory is the source of truth for messages exchanged between Toget
nodes, devices, relays, and account hosts.

- Transport: QUIC on port `7717`; WSS/TCP `443` as the fallback.
- Encoding: Protobuf, one message per stream or binary frame.
- Compatibility: additive changes within `toget/v1`; breaking changes move to
  a new package version after an overlap period.

## Status

These are the first message definitions. Only the local API is implemented so
far; the node transport lands in milestone M2. Fields may still change while
the protocol is a draft.

## Layout

| File | Contents |
| --- | --- |
| `toget/v1/common.proto` | Endpoints, server manifests, device certificates |
| `toget/v1/session.proto` | Hello, authentication, liveness, shutdown |
| `toget/v1/account.proto` | Account registration, login, device management |
| `toget/v1/room.proto` | Rooms, events, sync |
| `toget/v1/message.proto` | Outer envelope, inner message, attachments |

## Generation

```bash
task generate
```

Go types are generated into `internal/protocol/toget/v1/`. TypeScript
generation is added when `web/packages/proto` lands. `buf lint` runs in CI,
and CI regenerates the Go types and fails if the working tree changes.
Generated files are checked in and must not be edited by hand.
