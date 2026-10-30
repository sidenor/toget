# Technology Stack

> Status: proposed direction. Exact versions are pinned in `.mise.toml`, `go.mod`, and `pnpm-lock.yaml` when the scaffold lands.

## Decision summary

| Layer | Choice | Why |
| --- | --- | --- |
| Server | **Go** + `net/http` + `chi` | Static binary, strong concurrency, simple deployment, stable language |
| Daemon | **Go** | Cross-compiles to Linux, macOS, Windows; good process and service control; low memory |
| Web | **TypeScript + React + Vite** | Largest UI ecosystem, fast builds, easy to wrap in desktop/mobile shells later |
| UI system | **Tailwind CSS + Radix primitives + shadcn/ui** | Accessible primitives, copy-in components, no design-system lock-in |
| Server state | **TanStack Query** | Caching, invalidation, and optimistic updates without a global store |
| Forms | **React Hook Form + Zod** | Standard, typed, small |
| API contract | **OpenAPI 3.1** | Language-neutral, generates Go server types and TypeScript clients |
| Realtime | **WebSocket + JSON-RPC 2.0** | Bidirectional, simple, easy to debug, works in browsers and native clients |
| Server database | **PostgreSQL** (team/hosted) · **SQLite** (local mode) | Mature server database; embedded zero-ops mode for desktop/local use |
| Daemon database | **SQLite** | Embedded, zero-ops, good for local state and offline outbox |
| Database access | **pgx + sqlc + goose** | SQL-first, typed queries, no ORM lock-in |
| Auth | **OAuth/OIDC + device pairing** | No password storage; GitHub first, enterprise SSO later |
| CLI | **Cobra** | Standard Go CLI structure |
| Logging | **log/slog** | Standard library, structured logs |
| Telemetry | **OpenTelemetry** | Vendor-neutral traces and metrics |
| Tests | **Go testing, Vitest, Playwright** | Standard tools with broad support |
| Monorepo | **Go module + pnpm workspaces + mise + Taskfile** | Lightweight, reproducible, no heavy build system |
| CI/CD | **GitHub Actions + GoReleaser** | Standard, cross-platform release artifacts |
| Deployment | **Docker/OCI + Compose; Helm later** | Portable from laptop to cloud |
| Desktop (later) | **Tauri 2** | Small shell, system webview, reuses the web UI |
| Mobile (later) | **PWA first, Capacitor later** | One UI codebase; native shell only when push/background is needed |

## Why Go for the server and daemon

The daemon is the hardest part of the product to distribute:

- it must run on Linux, macOS, and Windows;
- it must start at boot, survive logout, and restart agents;
- it must spawn processes, stream stdio, watch files, and hold local secrets;
- it must be installable by a non-expert.

Go fits all of those:

- **Single static binary.** `CGO_ENABLED=0` cross-compilation gives one file per OS/arch. No Node runtime, no Python environment, no DLL hell.
- **Cross-platform service support.** Linux systemd, macOS launchd, and Windows services can share one service abstraction.
- **Good process control.** `os/exec`, signals, stdio pipes, and context cancellation are first-class. PTY support can be added behind an interface when needed.
- **Low idle cost.** A daemon may run all day; Go's memory footprint and startup time are predictable.
- **Stable language.** Go's compatibility promise means code written today keeps compiling. The standard library covers HTTP, TLS, JSON, logging, and crypto.
- **Shared code with the server.** Protocol types, policy evaluation, event validation, and storage interfaces live in one module and are tested once.

The server benefits from the same properties: one binary, easy containers, good concurrency, and no runtime dependency.

### What Go is not used for

Go is not used for the UI. Building a rich, accessible, fast-moving web app in Go templates would slow the product down and make the future desktop/mobile shells harder. The UI boundary is the API contract, so the two languages do not leak into each other.

## Why TypeScript and React for the web

- **Ecosystem and longevity.** React has the largest component, testing, and hiring ecosystem. It is a safe long-term choice for an open-source product.
- **One UI, three shells.** The web app can run as a normal SPA, inside Tauri for desktop, and inside Capacitor for mobile. That avoids maintaining a separate native UI.
- **Type safety across the boundary.** OpenAPI generates the API client; JSON Schema generates event types. The web app does not hand-write wire types.
- **Fast feedback.** Vite's dev server and React Fast Refresh keep the iteration loop short.
- **Accessible components.** Radix primitives handle focus, keyboard navigation, and screen-reader behavior; shadcn/ui provides copy-in components that can be edited instead of fought.

### Frontend choices in detail

- **Router:** React Router. It is widely used, well documented, and stable. The app is a SPA; SSR is not required.
- **Server state:** TanStack Query. It handles caching, retries, background refresh, and invalidation, which map directly to rooms, tasks, and approvals.
- **Local UI state:** Zustand only where needed. Most state should be server state or URL state.
- **Styling:** Tailwind CSS with CSS variables for theming. Utility classes are easy to remove or restyle; no proprietary component runtime.
- **Forms:** React Hook Form plus Zod. Both are widely used and have no vendor lock-in.
- **i18n:** `react-i18next` from the start, with English and Chinese message catalogs.
- **PWA:** Vite PWA plugin. The web app should be installable and usable for approvals on a phone before a native app exists.

### Why not Next.js

Next.js is excellent for content sites and server-rendered products. Toget's authenticated app is a dashboard with realtime data; SSR adds deployment complexity, caching rules, and a server runtime that the product does not need. A static SPA served by the Go server or a CDN is simpler and easier to embed in Tauri/Capacitor.

If a marketing site later needs SEO, it can be a separate static site. It should not force the app into a server-rendering framework.

## Data storage

### PostgreSQL for team deployments

- Mature, predictable, and available as a managed service everywhere.
- JSONB is useful for event payloads and policy documents.
- `LISTEN/NOTIFY` can power multi-instance realtime later without adding Redis on day one.
- Row-level security and partitioning are available when the product needs them.
- Standard SQL keeps the door open to managed Postgres, self-hosted Postgres, and compatible services.

### SQLite for local mode and the daemon

- Embedded, zero-ops, and reliable for local state.
- Server local mode uses SQLite so a desktop user does not need Docker or Postgres.
- The daemon uses SQLite for its event outbox, agent registry cache, and offline queue.
- Use the pure-Go `modernc.org/sqlite` driver so cross-compilation stays `CGO_ENABLED=0`.
- The server's domain layer depends on a store interface; Postgres and SQLite are two implementations. Parity tests run the same behavior suite against both.

### Why two server stores

Local-first is a product requirement, not a deployment detail. A desktop user should be able to run Toget without installing a database server. A team should be able to run it on managed Postgres. The store interface keeps the domain logic independent of either choice.

The cost is two SQL dialects and a parity test suite. That cost is lower than forcing every local user to run Postgres, and lower than a later migration from SQLite to Postgres.

### Why not an ORM

`sqlc` generates typed Go code from SQL. The queries remain visible, reviewable, and portable. Migrations are plain SQL managed by `goose`. This avoids the two common failure modes of ORMs: hidden queries and a hard-to-reverse schema abstraction.

## API and realtime protocol

### HTTP JSON for CRUD

- `/api/v1/...` for auth, rooms, tasks, agents, approvals, and audit queries.
- Cursor-based pagination, not offset pagination.
- RFC 9457 Problem Details for errors.
- Idempotency keys for task creation and approval decisions.
- OpenAPI 3.1 is the source of truth. `oapi-codegen` produces Go server types; `openapi-typescript` produces the web client.
- If a generator lags on 3.1 features, keep the document 3.0.3-compatible rather than changing the API. The contract matters more than the minor version of the spec.

### WebSocket for events and commands

- One persistent connection per web client and one per daemon.
- JSON-RPC 2.0 envelopes: request, response, notification, error.
- Per-room monotonic sequence numbers; reconnect with `after` to resume.
- Bounded queues and backpressure; slow clients are disconnected rather than consuming unbounded memory.
- Protocol version negotiated during the handshake. Additive changes are allowed within a major version; breaking changes require a new major version with an overlap window.

### Why not GraphQL

GraphQL would add a schema layer, resolver complexity, and caching questions for little benefit. The data model is event-oriented and realtime; REST plus WebSocket is a better fit and easier for third-party clients to implement.

### Why not gRPC/Connect everywhere

gRPC is excellent for service-to-service calls, but browser support and debugging are more complex. A JSON protocol keeps the barrier low for an open-source ecosystem. If internal service-to-service traffic grows, gRPC can be added behind the same domain interfaces without changing the public API.

## Authentication and secrets

- **Users:** GitHub OAuth first, email magic link second. No password database in the MVP.
- **Browser sessions:** short-lived access token in memory, refresh token in an `HttpOnly`, `Secure`, `SameSite=Lax` cookie. CSRF protection on refresh and state-changing cookie endpoints.
- **Daemon pairing:** a one-time code shown in the web app; the daemon exchanges it for a device credential.
- **Daemon credentials:** stored in the OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service). Headless Linux falls back to an encrypted file with a machine-bound key.
- **Enterprise SSO:** OIDC first, SAML later. Keep the identity layer behind an interface so a self-hosted enterprise deployment can connect its own IdP.

Secrets never go to the server. The server stores references and metadata, not API keys or agent credentials.

## Repository and build tooling

- **One Go module at the repository root.** `cmd/toget` is the only binary; subcommands are `server`, `daemon`, `login`, `pair`, `agent`, `doctor`, and `migrate`.
- **pnpm workspaces** for `apps/web` and `packages/*`.
- **mise** pins Go, Node, pnpm, and other tools per repository.
- **Taskfile** is the single entry point for cross-language tasks: `task dev`, `task test`, `task generate`, `task build`, `task release`.
- **No Nx, Bazel, or Turborepo at the start.** They add concepts and configuration that a small team does not need yet. pnpm's workspace scripts and Taskfile are enough. Turborepo can be added later for caching without changing the repository layout.

## Testing and quality

- **Go:** standard `testing`; table-driven tests; `httptest` for handlers; `testcontainers-go` for Postgres integration tests.
- **Web:** Vitest and React Testing Library for units and components; Playwright for end-to-end flows.
- **Contract tests:** shared JSON fixtures validate that Go and TypeScript agree on event envelopes and API payloads.
- **Cross-platform CI:** build and test the daemon on Linux, macOS, and Windows, for `amd64` and `arm64` where practical.
- **Static analysis:** `go vet`, `staticcheck`, `govulncheck`, ESLint, TypeScript strict mode, and `pnpm audit`.
- **Generated-code check:** CI runs `task generate` and fails if the working tree changes, so generated clients never drift from the contract.
- **Release checks:** GoReleaser builds archives, checksums, SBOMs, and container images; signatures via cosign.

## Deployment and operations

- **Self-hosted MVP:** one Go server binary, one Postgres instance, and a static web build served by the server. Docker Compose is the reference deployment.
- **Hosted:** the same binary and image, behind a load balancer, with managed Postgres.
- **Enterprise later:** Helm chart, horizontal scaling, external object storage, and SSO.
- **Observability:** structured logs to stdout, Prometheus metrics at `/metrics`, OpenTelemetry traces. No proprietary APM in the core.
- **Backups:** Postgres logical backups for team deployments; SQLite file backups for local mode; daemon state is reconstructable from the event log and agent configuration.

## Desktop and mobile later

- **Desktop:** Tauri 2 wraps the same web build and bundles the `toget` binary as a sidecar. This gives a tray icon, auto-start, deep links, and native notifications without a second UI codebase.
- **Mobile:** the web app is a PWA first. If push notifications, background execution, or app-store distribution become necessary, Capacitor wraps the same web build. React Native is not the default because it would create a second UI codebase.

## Version policy

- **Go:** track the current stable release; `go.mod` pins the minimum. Go's compatibility promise protects existing code.
- **Node:** use an active LTS release, pinned in `.mise.toml` and CI.
- **PostgreSQL:** support the current major and the previous major; test migrations against both.
- **API:** `/api/v1` is stable. Additive fields are allowed. Breaking changes require `/api/v2` and a documented overlap period.
- **Event schemas:** each event type has a version; unknown fields are preserved where possible.
- **Dependencies:** prefer libraries with a stable API and an active maintainer. Avoid dependencies that own the domain model.

## Rejected alternatives

| Alternative | Why not |
| --- | --- |
| **All TypeScript (Node daemon)** | Cross-platform service installation and native modules are fragile; packaging a Node runtime into a daemon is heavier; process/signal handling is weaker. Possible, but not the best long-term fit. |
| **Rust for the server** | Excellent performance and safety, but slower iteration and a smaller hiring pool for CRUD-heavy server work. Rust remains an option for a future performance-critical helper. |
| **Python for the server** | Great for prototypes and data work, but packaging, concurrency, and long-running daemon operations are less predictable. |
| **Elixir/Phoenix** | Strong realtime story, but adds a second backend ecosystem and a smaller hiring pool for a small open-source project. |
| **Next.js** | SSR and server runtime complexity are not needed for an authenticated realtime dashboard. |
| **GraphQL** | More moving parts than REST + WebSocket for this data model; harder for third-party clients to adopt. |
| **MongoDB** | The data is relational (users, rooms, memberships, events) and benefits from transactions and SQL. |
| **Microservices** | Premature for a small team. A modular monolith with clear package boundaries can be split later if measurements justify it. |
| **Kubernetes-first** | Too much operational surface for the MVP. Compose first; Helm when enterprise deployments need it. |
| **Proprietary BaaS (auth, database, realtime)** | Convenient at first, but creates migration risk and weakens the self-hosted/open-source promise. |

## What would make us revisit a decision

A stack decision should change only when there is evidence, not fashion. Triggers to revisit:

- The daemon cannot meet a platform requirement (for example, a needed OS API is unavailable in Go).
- Postgres becomes a proven bottleneck at a scale where a different store is justified.
- The web UI needs a capability that React cannot reasonably provide.
- A protocol requirement cannot be met by HTTP + WebSocket.

If a change is needed, the API contract, domain packages, and event log stay the same; only the adapter or runtime changes. That is the point of the boundaries.

## The short version

**Go for the server and daemon. TypeScript/React for the web. Postgres and SQLite for data. OpenAPI and WebSocket for the contract. One repository, one release train, boring standards.**

Two languages is a deliberate trade: Go gives the daemon a reliable cross-platform story, and TypeScript gives the UI the ecosystem it needs. The API contract keeps them independent, so neither choice forces a rewrite of the other.
