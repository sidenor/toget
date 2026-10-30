# 0002 — Go for the server and daemon

**Status:** accepted
**Date:** 2026-10-30

## Context

The daemon must run on Linux, macOS, and Windows, start at boot, supervise agent processes, hold local secrets, and be installable by non-experts. The server must be easy to deploy as a container or a single binary, handle many long-lived WebSocket connections, and share protocol code with the daemon.

## Decision

Use Go for both the server and the daemon. Build one binary, `toget`, with subcommands for `server`, `daemon`, `login`, `pair`, `agent`, `doctor`, and `migrate`.

## Consequences

- Static, `CGO_ENABLED=0` cross-compilation produces one binary per OS/arch.
- The daemon can use systemd, launchd, and Windows service integration without a runtime dependency.
- Process control, stdio streaming, signals, and HTTP/WebSocket servers are straightforward.
- The server and daemon share protocol types, policy evaluation, and validation.
- Go's compatibility promise lowers long-term maintenance risk.
- The team must be comfortable with Go and TypeScript. This is a deliberate two-language trade.
- UI development stays in TypeScript; Go is not used for the web app.

## Alternatives considered

- **All TypeScript / Node daemon:** one language, but native modules, cross-platform service installation, and packaging a Node runtime into a daemon are fragile.
- **Rust:** excellent runtime properties, but slower iteration and a smaller hiring pool for CRUD-heavy server work. Rust remains an option for a future performance-critical helper.
- **Python:** fast to prototype, but packaging and long-running daemon operations are less predictable.
- **Elixir/Phoenix:** strong realtime model, but adds a second backend ecosystem and a smaller hiring pool.

## Review trigger

Revisit only if Go cannot meet a concrete platform requirement. A replacement must preserve the protocol, domain interfaces, and event log.
