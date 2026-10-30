# API Contract

This directory is the source of truth for the wire protocol.

- `openapi.yaml` — HTTP API 3.1.
- `events/*.schema.json` — JSON Schema for WebSocket event payloads.

## Generation

```bash
task generate
```

Generated output:

- Go: `internal/protocol/`
- TypeScript: `packages/api-client/`

Do not edit generated files by hand. CI runs `task generate` and fails if the working tree changes.

## Versioning

- HTTP base path: `/api/v1`
- Additive changes are allowed within `v1`.
- Removing or renaming a field, or changing its meaning, requires `/api/v2`.
- Event types are versioned by their schema; clients must ignore unknown types and fields.

See [docs/protocol.md](../docs/protocol.md) for the full protocol.
