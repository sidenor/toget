# 0005 — HTTP JSON + WebSocket, contract-first

**Status:** accepted
**Date:** 2026-10-30

## Context

The web client and daemon need request/response APIs and a realtime event stream. Third-party clients and connectors should be able to implement the protocol without reverse-engineering a proprietary SDK.

## Decision

- HTTP JSON for CRUD and queries, described by OpenAPI 3.1.
- WebSocket with JSON-RPC 2.0 for events and commands.
- JSON Schema for event payloads.
- Generated Go server types and TypeScript clients; CI checks that generated code is current.
- `/api/v1` is stable; breaking changes require a new major version and an overlap period.

## Consequences

- The protocol is readable with `curl` and browser developer tools.
- Third-party clients can be written in any language.
- WebSocket reconnection, ordering, idempotency, and backpressure must be designed explicitly.
- JSON is less compact than binary protocols; acceptable for the expected event volume.
- Codegen adds a build step, but removes hand-written wire types and contract drift.

## Alternatives considered

- **GraphQL:** more schema and resolver machinery; poor fit for an event-oriented realtime model.
- **gRPC/Connect everywhere:** excellent for service-to-service, but browser support and debugging are more complex for an open protocol.
- **Custom binary protocol:** efficient, but raises the barrier for contributors and third-party clients.
- **Hand-written types:** faster at first, but drifts as the API grows.

## Review trigger

Revisit if event volume makes JSON a measured bottleneck, or if a required client platform cannot use WebSocket. A binary encoding can be added as an optional transport without changing the event model.
