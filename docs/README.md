# Documentation

Start here:

| Document | What it covers |
| --- | --- |
| [Philosophy](philosophy.md) | Why Toget exists, what we believe, and what we are not |
| [Product](product.md) | What Toget is, who it is for, MVP scope, non-goals |
| [Architecture](architecture.md) | Nodes, account hosts, rooms, relay tree, trust boundaries |
| [Connectors](connectors.md) | Stable v1 command/connector protocol, Go SDK, compatibility, security |
| [Agent policies](policies.md) | Allow/deny/review rules, matching, precedence, limits |
| [Protocol](protocol.md) | Node protocol, end-to-end envelope, local API, versioning |
| [Security model](security-model.md) | Assets, who can see what, threats, controls, limits |
| [Account recovery](account-recovery.md) | Per-device vault recovery, password change, local reset, limits |
| [Relay and directory](relay.md) | Cross-network routing, trust boundaries, quotas, limits |
| [LAN discovery](discovery.md) | mDNS advertisement, verified lookup, privacy, limits |
| [Business cards](cards.md) | Portable signed cards, QR codes for cards, invites, and pairing, saved-card pinning, limits |
| [Deployment](deployment.md) | Self-hosted, official-hosted, relay, and offline modes |
| [Signed updates](updates.md) | Release keys, manifests, channels, index/yank, reproducible builds, SBOMs, publish script, automatic checks, atomic install, rollback |
| [Technology stack](tech-stack.md) | Chosen stack, rationale, rejected alternatives, version policy |
| [Repository layout](repo-layout.md) | Directory structure and rules for where code belongs |
| [Roadmap](roadmap.md) | Milestones, definitions of done, near-term tasks |
| [Decisions](decisions/) | Architecture decision records (ADRs) |

## Reading order

1. Philosophy — why this exists and what we believe.
2. Product — who it is for, and what the first version does.
3. Architecture — how the pieces fit.
4. Protocol — how nodes, devices, and relays talk.
5. Connectors — how a local Agent runtime talks to the daemon.
6. Agent policies — how the node decides what an Agent may do.
7. Security model — who can decrypt what, and what we do not protect yet.
8. Account recovery — how passwords and local vault keys are recovered.
9. Deployment — how a node is installed and operated.
10. Signed updates — how releases are signed, served, installed, and rolled back.
11. Tech stack — why these tools.
12. Repository layout — where code goes.
13. Roadmap — what to build next.

## Decision records

| ADR | Decision |
| --- | --- |
| [0001](decisions/0001-monorepo.md) | One monorepo |
| [0002](decisions/0002-go-backend.md) | Go for the node and the official service |
| [0003](decisions/0003-web-stack.md) | React + Vite for the web UI |
| [0004](decisions/0004-storage.md) | SQLite first; Postgres only at scale |
| [0005](decisions/0005-protocol.md) | HTTP JSON + WebSocket (superseded by 0008) |
| [0006](decisions/0006-account-model.md) | 14-digit account ID; official or self-hosted account host |
| [0007](decisions/0007-endpoint-trust.md) | The endpoint is the trust boundary; E2E and a blind official service |
| [0008](decisions/0008-mesh-protocol.md) | QUIC + Protobuf node protocol, relay tree, E2E envelope |
| [0009](decisions/0009-versioning.md) | Versioned protocol, signed releases, atomic updates |

## Status

All documents are drafts. The architecture and protocol will change as the first version is built. Decisions that are expensive to reverse should be recorded as ADRs before implementation.
