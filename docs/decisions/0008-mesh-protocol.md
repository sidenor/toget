# 0008 — QUIC + Protobuf mesh protocol with an end-to-end envelope

**Status:** accepted (design)
**Supersedes:** [0005](0005-protocol.md)
**Date:** 2026-10-31

## Context

The first protocol draft assumed one hub: browsers and daemons spoke HTTP JSON and WebSocket JSON-RPC to a central server, and the server owned the event log. That conflicts with the current product model:

- accounts can live on the official service or on a machine the user controls;
- nodes connect directly when they can and use relays only as a fallback;
- messages are end-to-end encrypted, so the relay and host cannot be the place where events are assembled;
- history is copied to participants, not held by a single server.

A hub protocol cannot express direct node-to-node rooms, relay paths, or an outer header that hides the room ID.

## Decision

1. **Node protocol:** QUIC on `7717` as the primary transport, WSS/TCP `443` as a fallback for networks that block UDP. TLS 1.3 everywhere.
2. **Encoding:** Protobuf, defined in `proto/toget/v1/`. Generated Go and TypeScript; `buf breaking` in CI.
3. **Two-layer envelope:** an outer header visible to relays (message ID, target, time, size, sender device, signature) and an encrypted inner message (room ID, epoch, sender, type, body, attachments, mentions). AAD binds the ciphertext to room, epoch, and message ID.
4. **Room keys:** a 256-bit epoch key per membership epoch, rotated on every join/leave, wrapped to each member endpoint with HPKE (X25519 + HKDF-SHA256 + ChaCha20-Poly1305).
5. **Direct first, relay fallback:** try IPv6, port mapping, and QUIC hole punching; if that fails, forward through the relay tree. Any reachable node can relay; the official service is the default parent.
6. **Local API:** HTTP JSON + OpenAPI 3.1 on `127.0.0.1:7718` for the local web UI, CLI, and Agents. This is the only JSON surface on a node.
7. **Directory API:** HTTPS JSON with self-certifying signed manifests. The directory is a convenience, not a root of trust.
8. **Accounts:** 14-digit ID as defined in [0006](0006-account-model.md); login uses PAKE.

## Consequences

- Nodes can talk to each other without a central server being the room owner.
- The relay sees only routing metadata, not room content.
- QUIC gives independent streams and cheap reconnects; the WSS fallback keeps the product usable on restrictive networks.
- Protobuf keeps the wire compact and forward-compatible, at the cost of a codegen step and less readable captures than JSON.
- Building a relay tree, path calculation, and NAT traversal is real work; libp2p and Matrix were considered and rejected because their trust and room models do not match.
- The local API stays easy to debug with `curl`, so the developer experience is not all binary.
- Two protocol surfaces (node and local) must share domain types without sharing wire formats.

## Alternatives considered

- **Keep HTTP + WebSocket JSON-RPC (0005):** easiest to start, but hub-centric and incompatible with direct node-to-node rooms and encrypted relays. Superseded.
- **Matrix:** mature rooms and federation, but its homeserver replication and event graph conflict with participant-held history and a blind official service.
- **libp2p:** broad transport and routing stack, but heavy and opinionated; Toget's relay tree and account model are specific enough that we would fight it.
- **gRPC/Connect everywhere:** good for service-to-service, but browser support and debugging are harder for an open protocol.
- **Custom binary protocol:** maximum control, but raises the barrier for independent implementations and security review.
- **Central relay with server-readable messages:** simple, but contradicts 0007.

## Review trigger

Revisit if QUIC proves unreliable on a meaningful share of networks, if the relay tree cannot meet a latency or availability target, or if a required platform cannot speak the protocol. The envelope and account model should survive a transport change.
