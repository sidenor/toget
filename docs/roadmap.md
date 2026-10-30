# Roadmap

> Dates are targets, not promises. A milestone is done only when its definition of done is met.

## M0 — Design and scaffold (this week)

**Goal:** make the project buildable and the decisions explicit.

- [x] Product definition and non-goals (`docs/product.md`)
- [x] Architecture draft (`docs/architecture.md`)
- [x] Technology choices and rationale (`docs/tech-stack.md`)
- [x] Repository layout (`docs/repo-layout.md`)
- [x] Protocol draft (`docs/protocol.md`)
- [ ] Go module, `cmd/toget`, and health-check server
- [ ] React app shell with routing and a design-system baseline
- [ ] `Taskfile` with `dev`, `test`, `generate`, `build`
- [ ] CI: Go tests, web build, lint, cross-platform daemon build
- [ ] Docker Compose with Postgres
- [ ] `api/openapi.yaml` with the first endpoints
- [ ] ADRs for monorepo, Go backend, web stack, storage, protocol

**Done when:** `task dev` starts the server and web app; `task test` passes on a clean checkout; CI is green on Linux, macOS, and Windows builds.

## M1 — Local MVP (weeks 1–3)

**Goal:** prove the core loop on one machine.

- GitHub OAuth login and session handling
- Create a room and invite one member (the same user on a second device is enough)
- Pair a daemon with a one-time code
- Register one CLI agent via the process adapter
- Create a task and assign it to the agent
- Stream plan and tool events to the web timeline
- Approval gate for `external.send`, `money.spend`, and `destructive`
- Approve or deny from the web app
- Append-only event log with hash chain
- Daemon outbox and reconnect
- Docker Compose deployment
- End-to-end test: login → pair → task → approval → result

**Done when:** a new user can install the daemon, pair it, run a task, and approve a risky action in under 10 minutes, following the README.

## M2 — Team rooms (weeks 4–7)

**Goal:** make Toget useful for a small team.

- Multiple users per room
- Roles: owner, maintainer, member, viewer
- Invitations by link and email
- Multiple daemons per user
- Agent sharing with scoped tokens
- Policy templates per room
- Audit timeline filters and export (JSONL/CSV)
- Presence and heartbeat
- Web push notifications for approvals
- Self-hosted deployment guide
- Backup and restore guide

**Done when:** two people on two machines can share a room, run their own agents, and approve each other's risky actions without sharing credentials.

## M3 — Connectors and platform (weeks 8–12)

**Goal:** make integration a first-class, extensible surface.

- MCP client with tool discovery
- HTTP/OpenAI-compatible agent adapter
- Connector SDK and example connectors
- Agent capability manifests
- Policy language v1 with amount, recipient, and time-window rules
- Audit export to S3-compatible storage
- Metrics and traces for self-hosters
- Helm chart
- Desktop shell (Tauri) with tray and auto-start
- PWA install flow and mobile approval experience

**Done when:** a third-party developer can add a connector without modifying the core, and a self-hoster can deploy with Helm and export audit logs.

## M4 — Commercial and enterprise (after M3)

Only after the open-source core is useful:

- Hosted cloud with billing (Stripe or Paddle)
- SSO (OIDC, then SAML)
- SCIM provisioning
- Advanced retention and legal hold
- High availability and horizontal scaling
- Enterprise support and private deployment

**Done when:** a company can deploy Toget with its own IdP, retain audit logs according to policy, and pay for support without forking the core.

## Sequencing rules

1. **One loop first.** Do not add a second client or a second agent adapter until the first loop is reliable.
2. **No billing before retention.** Charge only after users return without being asked.
3. **No enterprise features before team features.** SSO is not a substitute for a useful room.
4. **No microservices before measurements.** The modular monolith is the default.
5. **No protocol break without a version.** Additive changes first.

## Near-term task list

1. Scaffold Go module and `cmd/toget`.
2. Add `api/openapi.yaml` with health, auth, rooms, and tasks.
3. Scaffold `apps/web` with React Router, TanStack Query, Tailwind, and a layout shell.
4. Add Docker Compose with Postgres.
5. Implement the daemon pairing flow and a fake agent adapter.
6. Implement the event append and room subscription path.
7. Implement the approval gate and timeline.
8. Write the first end-to-end test.
