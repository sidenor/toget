# Documentation

Start here:

| Document | What it covers |
| --- | --- |
| [Product](product.md) | What Toget is, who it is for, MVP scope, non-goals |
| [Architecture](architecture.md) | Components, event model, approval flow, security boundaries |
| [Technology stack](tech-stack.md) | Chosen stack, rationale, rejected alternatives, version policy |
| [Repository layout](repo-layout.md) | Directory structure and rules for where code belongs |
| [Protocol](protocol.md) | HTTP API, WebSocket commands, event envelope, versioning |
| [Security model](security-model.md) | Assets, trust boundaries, threats, controls, MVP limits |
| [Deployment](deployment.md) | Local, self-hosted, hosted, and enterprise deployment |
| [Roadmap](roadmap.md) | Milestones, definitions of done, near-term tasks |
| [Decisions](decisions/) | Architecture decision records (ADRs) |

## Reading order

1. Product — why this exists.
2. Architecture — how the pieces fit.
3. Tech stack — why these tools.
4. Repository layout — where code goes.
5. Protocol — how clients and daemons talk.
6. Security model — what is trusted and what is not.
7. Roadmap — what to build next.

## Decision records

| ADR | Decision |
| --- | --- |
| [0001](decisions/0001-monorepo.md) | One monorepo |
| [0002](decisions/0002-go-backend.md) | Go for the server and daemon |
| [0003](decisions/0003-web-stack.md) | React + Vite for the web app |
| [0004](decisions/0004-storage.md) | Postgres for the server, SQLite for the daemon |
| [0005](decisions/0005-protocol.md) | HTTP JSON + WebSocket, contract-first |

## Status

All documents are drafts. The architecture and protocol will change as the first version is built. Decisions that are expensive to reverse should be recorded as ADRs before implementation.
