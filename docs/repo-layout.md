# Repository Layout

Toget is a single repository containing the server, the cross-platform daemon, the web app, future desktop/mobile shells, shared packages, deployment assets, and documentation.

## Top-level tree

```text
toget/
├── .github/
│   ├── workflows/
│   │   ├── ci.yml                 # lint, test, build, cross-platform daemon matrix
│   │   ├── release.yml            # GoReleaser, container images, checksums
│   │   └── security.yml           # dependency and vulnerability scanning
│   ├── ISSUE_TEMPLATE/
│   └── pull_request_template.md
├── .editorconfig
├── .gitignore
├── .mise.toml                     # pinned Go, Node, pnpm, task versions
├── Taskfile.yml                   # cross-language task entry point
├── go.mod
├── go.sum
├── package.json
├── pnpm-lock.yaml
├── pnpm-workspace.yaml
├── tsconfig.base.json
├── README.md
├── LICENSE
├── CONTRIBUTING.md
├── SECURITY.md
├── TRADEMARK.md
│
├── api/                           # contract source of truth
│   ├── openapi.yaml               # HTTP API 3.1
│   ├── events/                    # JSON Schema for realtime events
│   │   ├── envelope.schema.json
│   │   ├── task.schema.json
│   │   ├── approval.schema.json
│   │   ├── agent.schema.json
│   │   └── tool.schema.json
│   └── README.md
│
├── cmd/
│   └── toget/
│       └── main.go                # one binary: server, daemon, login, pair, agent, doctor, migrate
│
├── internal/                      # Go implementation (not importable outside this module)
│   ├── cli/                       # Cobra commands and wiring
│   ├── config/                    # config loading, validation, defaults
│   ├── server/                    # Hub: HTTP, WebSocket, middleware
│   │   ├── http/
│   │   ├── ws/
│   │   └── middleware/
│   ├── daemon/                    # local runtime
│   │   ├── runtime/               # lifecycle, reconnect, outbox
│   │   ├── supervisor/            # agent process supervision
│   │   ├── pairing/               # device pairing
│   │   └── updater/               # signed self-update (later)
│   ├── domain/                    # business rules, no transport types
│   │   ├── user/
│   │   ├── room/
│   │   ├── agent/
│   │   ├── task/
│   │   ├── approval/
│   │   ├── policy/
│   │   └── audit/
│   ├── store/                     # storage interfaces and implementations
│   │   ├── store.go
│   │   ├── postgres/              # server
│   │   └── sqlite/                # daemon local state
│   ├── events/                    # event envelope, validation, hash chain
│   ├── protocol/                  # generated wire types (do not edit)
│   ├── auth/                      # OAuth, sessions, device tokens
│   ├── secrets/                   # OS keychain + encrypted fallback
│   ├── connectors/                # agent and tool integrations
│   │   ├── mcp/                   # MCP client
│   │   ├── process/               # generic CLI agent adapter
│   │   └── http/                  # OpenAI-compatible / HTTP agent adapter
│   ├── platform/                  # OS-specific behavior behind interfaces
│   │   ├── paths/                 # config, state, log locations
│   │   ├── service/               # systemd, launchd, Windows service
│   │   ├── keychain/              # Keychain, Credential Manager, Secret Service
│   │   └── pty/                   # PTY abstraction (later)
│   └── telemetry/                 # logs, metrics, traces
│
├── apps/                          # user-facing clients
│   ├── web/                       # React SPA (first client)
│   │   ├── index.html
│   │   ├── package.json
│   │   ├── vite.config.ts
│   │   ├── public/
│   │   └── src/
│   │       ├── app/               # bootstrap, providers, error boundary
│   │       ├── routes/            # route definitions
│   │       ├── features/          # feature folders
│   │       │   ├── auth/
│   │       │   ├── rooms/
│   │       │   ├── tasks/
│   │       │   ├── agents/
│   │       │   ├── approvals/
│   │       │   └── audit/
│   │       ├── components/        # app-specific composed components
│   │       ├── lib/               # API client, hooks, utilities
│   │       ├── styles/
│   │       └── main.tsx
│   ├── desktop/                   # Tauri 2 shell (later)
│   └── mobile/                    # PWA first; Capacitor shell later
│
├── packages/                      # shared TypeScript
│   ├── api-client/                # generated from api/openapi.yaml; do not edit by hand
│   ├── ui/                        # design system and shared components
│   └── config/                    # eslint, tsconfig, tailwind presets
│
├── deploy/
│   ├── docker/
│   │   ├── server.Dockerfile
│   │   └── web.Dockerfile
│   ├── compose/
│   │   └── docker-compose.yml     # Postgres + server + web
│   ├── helm/                      # enterprise/self-hosted chart (later)
│   ├── systemd/
│   │   └── toget.service
│   ├── launchd/
│   │   └── com.toget.daemon.plist
│   └── windows/                   # service installer / MSI (later)
│
├── docs/
│   ├── README.md                  # docs index
│   ├── product.md                 # what we are building and for whom
│   ├── architecture.md            # components, flows, security model
│   ├── tech-stack.md              # choices and rationale
│   ├── repo-layout.md             # this file
│   ├── protocol.md                # HTTP/WebSocket/event protocol
│   ├── security-model.md          # threat model and controls
│   ├── deployment.md              # local, self-hosted, hosted
│   ├── roadmap.md                 # milestones and definition of done
│   └── decisions/                 # architecture decision records
│       ├── 0001-monorepo.md
│       ├── 0002-go-backend.md
│       ├── 0003-web-stack.md
│       ├── 0004-storage.md
│       └── 0005-protocol.md
│
├── scripts/
│   ├── dev.sh                     # start Postgres, server, web
│   ├── generate.sh                # OpenAPI and JSON Schema codegen
│   ├── build.sh                   # build server + web
│   └── release.sh                 # local release smoke test
│
├── test/
│   ├── integration/               # Go + testcontainers
│   ├── e2e/                       # Playwright
│   └── fixtures/                  # shared JSON fixtures for contract tests
│
└── examples/
    ├── cli-agent/                 # minimal agent that speaks the adapter protocol
    └── mcp-server/                # example MCP server for connector tests
```

## Rules

### `cmd/` contains wiring only

`cmd/toget/main.go` parses flags, builds dependencies, and calls into `internal/`. Business logic belongs in `internal/domain` or the relevant subsystem. This keeps the binary entry point replaceable.

### `internal/` is the Go application

- `internal/domain` owns business rules and must not import HTTP, WebSocket, or database drivers.
- `internal/store` defines interfaces and SQL implementations. Domain code depends on the interface, not Postgres or SQLite.
- `internal/server` translates HTTP/WS into domain calls. It does not contain policy decisions.
- `internal/daemon` owns local lifecycle, agent supervision, and the outbox.
- `internal/connectors` contains adapters. Adding a new agent runtime should not require changes to the server.

### `api/` is the contract

- `api/openapi.yaml` is the source of truth for HTTP.
- `api/events/*.schema.json` is the source of truth for realtime events.
- Generated code goes to `internal/protocol` (Go) and `packages/api-client` (TypeScript).
- Never hand-edit generated files. CI checks that regeneration produces no diff.
- Breaking changes require a new API major version; additive changes are preferred.

### `apps/` contains clients, not business logic

- `apps/web` is a SPA. It talks to the server only through the generated API client and WebSocket protocol.
- `apps/desktop` and `apps/mobile` are shells around the same web build. They must not fork the UI.
- Feature code lives under `src/features/<feature>` with its own components, hooks, and tests.

### `packages/` contains shared TypeScript

- `packages/api-client` is generated.
- `packages/ui` is the design system. Components here are generic; feature-specific components stay in the app.
- `packages/config` holds shared lint, TypeScript, and Tailwind configuration.

### `deploy/` is per environment

- Docker Compose is the reference self-hosted deployment.
- Helm is for enterprise/HA deployments later.
- Service files for systemd, launchd, and Windows live here and are embedded into the daemon installer.
- Do not put secrets in this directory. Use environment variables or secret files.

### `docs/` is part of the product

- Every irreversible or expensive decision gets an ADR in `docs/decisions/`.
- `docs/architecture.md` describes the current design, not aspirational features.
- `docs/roadmap.md` defines what "done" means for each milestone.
- Keep docs in English for the open-source community; Chinese summaries can live in release notes or the README.

### `test/` is for cross-cutting tests

- Unit tests live next to the code they test.
- `test/integration` covers database and multi-component behavior.
- `test/e2e` covers user-visible flows with a real server, database, and fake daemon.
- `test/fixtures` contains shared JSON used by both Go and TypeScript contract tests.

## Private notes are not in this repository

The repository is public. Personal notes, credentials, customer data, and internal strategy belong outside it. Public docs may summarize product decisions, but they must not contain private information.

## Naming conventions

- Go packages: short, lowercase, no underscores.
- TypeScript files: `kebab-case.ts` for modules, `PascalCase.tsx` for components.
- Feature folders: singular nouns (`approval`, `agent`) in Go; plural route segments (`approvals`, `agents`) in the web app.
- Event types: `domain.action` in past or present tense, consistently: `task.created`, `tool.requested`, `approval.granted`.
- Environment variables: `TOGET_` prefix.
- Database tables: plural snake_case (`room_members`, `approval_requests`).

## Why one repository

- One change can update the protocol, server, daemon, web client, and docs together.
- One CI pipeline catches contract drift.
- One release train keeps versions aligned.
- Contributors can run the whole system with one command.
- The open-source core and the hosted product share the same code, which keeps the self-hosted path honest.

The repository is a modular monolith, not a pile of unrelated projects. Boundaries are enforced by package structure and the API contract, not by separate repositories.
