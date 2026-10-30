# 0002 — Go for the node and the official service

**Status:** accepted
**Date:** 2026-10-30
**Updated:** 2026-10-31 — reworded from "server and daemon" to the node/official-service model.

## Context

The node must run on Linux, macOS, and Windows, start at boot, hold local keys, supervise Agent processes, move encrypted envelopes, and be installable by non-experts. The official service must run the directory, a relay, an account host, and the update service, share protocol and crypto code with the node, and be easy to deploy.

## Decision

Use Go for the node and the official service. Build two binaries from one module:

- `toget` — node, daemon, CLI, relay, self-hosted account host.
- `toget-official` — directory, official account host, update service.

The CLI subcommands include `account`, `device`, `room`, `send`, `messages`, `agent`, `relay`, `update`, `backup`, `doctor`, and `status`.

## Consequences

- Static, `CGO_ENABLED=0` cross-compilation produces one binary per OS/arch.
- The node can use systemd, launchd, and Windows service integration without a runtime dependency.
- Process control, stdio streaming, signals, QUIC, and HTTP servers are straightforward.
- The node and the official service share protocol types, crypto wrappers, validation, and storage interfaces.
- Go's compatibility promise lowers long-term maintenance risk.
- The team must be comfortable with Go and TypeScript. This is a deliberate two-language trade.
- UI development stays in TypeScript; Go is not used for the web UI.

## Alternatives considered

- **All TypeScript / Node daemon:** one language, but native modules, cross-platform service installation, and packaging a Node runtime into a daemon are fragile.
- **Rust:** excellent runtime properties, but slower iteration for a small team. Rust remains an option for a future performance-critical helper.
- **Python:** fast to prototype, but packaging and long-running service operations are less predictable.
- **Elixir/Phoenix:** strong realtime model, but adds a second backend ecosystem and a smaller hiring pool.

## Review trigger

Revisit only if Go cannot meet a concrete platform requirement. A replacement must preserve the protocol, domain interfaces, and event log.
