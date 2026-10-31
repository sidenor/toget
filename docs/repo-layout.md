# Repository Layout (Draft)

Toget is one repository containing the node, the official service, the local web UI, the reset page, shared packages, deployment assets, and documentation.

> **Current implementation:** the working tree contains `cmd/toget`,
> `cmd/toget-official`, `internal/{account,agent,api,approval,backup,config,identity,node,protocol,room,session,storage,tool,ui,vault,version}`,
> `web/app`, `api/`, `proto/`, and `docs/`. The tree below is the target
> layout; directories such as `relay`, `transport`, `sync`, `official`,
> `deploy`, `tests`, and `examples` are planned, not present yet. The LAN node
> lives in `internal/node`, and remote-room sync/lifecycle code lives in
> `internal/room/remote.go`.

## Top-level tree

```text
toget/
├── .github/
│   ├── workflows/
│   │   ├── ci.yml                 # lint, test, cross-platform build
│   │   ├── release.yml            # artifacts, manifests, signatures
│   │   └── security.yml           # dependency and vulnerability scanning
│   ├── ISSUE_TEMPLATE/
│   └── pull_request_template.md
├── .editorconfig
├── .gitignore
├── .mise.toml                     # pinned Go, Node, pnpm, buf, task
├── Taskfile.yml                   # cross-language entry point
├── go.mod
├── go.sum
├── buf.yaml
├── buf.gen.yaml
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
├── cmd/
│   ├── toget/
│   │   └── main.go                # node, daemon, CLI, relay, self-hosted account host
│   └── toget-official/
│       └── main.go                # directory, official account host, update service
│
├── internal/                      # Go implementation, not importable outside the module
│   ├── account/                   # account records, 14-digit ID, login, recovery
│   ├── device/                    # device identity, enrollment, certificates, revocation
│   ├── identity/                  # keys, signatures, trust anchors, manifests
│   ├── crypto/                    # HPKE, AEAD, KDF, PAKE wrappers; no domain logic
│   ├── protocol/                  # generated Protobuf types and validation
│   ├── transport/                 # QUIC, WSS fallback, framing, handshake, reconnect
│   ├── relay/                     # parent table, routing, forwarding, offline queue
│   ├── room/                      # rooms, membership, epochs, events, ordering
│   ├── message/                   # envelope construction, inner/outer split, attachments
│   ├── sync/                      # cursors, deltas, snapshots, outbox replay
│   ├── storage/                   # storage interfaces and SQLite implementation
│   ├── agent/                     # Agent identity, sessions, supervision, permissions
│   ├── mdns/                      # implemented _toget._tcp.local. advertisement and browse cache
│   ├── discovery/                 # planned manifest cache, QR/manual addresses (directory client lives in node/)
│   ├── official/                  # directory, update service, official account host
│   ├── update/                    # version check, download, verify, switch, rollback
│   ├── ui/                        # local web UI serving, token, origin checks
│   ├── api/                       # local HTTP API handlers (OpenAPI)
│   ├── config/                    # config loading, defaults, validation
│   ├── platform/                  # paths, services, keychain, PTY
│   └── version/                   # app, protocol, schema versions and capability flags
│
├── proto/                         # node protocol source of truth
│   ├── toget/v1/
│   │   ├── session.proto
│   │   ├── account.proto
│   │   ├── room.proto
│   │   ├── message.proto
│   │   ├── relay.proto
│   │   ├── agent.proto
│   │   └── envelope.proto
│   └── README.md
│
├── api/                           # local API source of truth
│   ├── openapi.yaml
│   └── README.md
│
├── web/
│   ├── app/                       # local web UI (React SPA)
│   │   ├── index.html
│   │   ├── vite.config.ts
│   │   └── src/
│   │       ├── app/               # bootstrap, providers, error boundary
│   │       ├── routes/
│   │       ├── features/          # rooms, agents, approvals, devices, relay, audit
│   │       ├── components/
│   │       ├── lib/
│   │       └── main.tsx
│   ├── reset/                     # static account-reset page; reads the credential locally
│   ├── site/                      # project site and docs site
│   └── packages/
│       ├── ui/                    # design system and shared components
│       ├── api-client/            # generated from api/openapi.yaml
│       ├── proto/                 # generated from proto/
│       └── config/                # eslint, tsconfig, tailwind presets
│
├── deploy/
│   ├── systemd/
│   │   └── toget.service
│   ├── launchd/
│   │   └── com.toget.daemon.plist
│   ├── windows/
│   │   └── toget.wxs
│   ├── docker/
│   │   └── Dockerfile
│   └── compose/
│       └── docker-compose.yml
│
├── docs/
│   ├── README.md
│   ├── philosophy.md
│   ├── product.md
│   ├── architecture.md
│   ├── protocol.md
│   ├── security-model.md
│   ├── deployment.md
│   ├── tech-stack.md
│   ├── repo-layout.md
│   ├── roadmap.md
│   └── decisions/                 # architecture decision records
│       ├── 0001-monorepo.md
│       ├── 0002-go-backend.md
│       ├── 0003-web-stack.md
│       ├── 0004-storage.md
│       ├── 0005-protocol.md       # superseded by 0008
│       ├── 0006-account-model.md
│       ├── 0007-endpoint-trust.md
│       ├── 0008-mesh-protocol.md
│       └── 0009-versioning.md
│
├── scripts/
│   ├── build.sh                   # build node, official service, web assets
│   ├── release.sh                 # local release smoke test
│   ├── sign.sh                    # sign manifests and artifacts
│   └── generate.sh                # regenerate Protobuf and OpenAPI clients
│
├── tests/
│   ├── integration/               # multi-node, relay, storage, sync
│   ├── e2e/                       # Playwright against a real local node
│   ├── conformance/               # protocol fixtures a second implementation can reuse
│   └── fixtures/
│
└── examples/
    ├── cli-agent/                 # minimal Agent that speaks the CLI protocol
    └── relay/                     # smallest relay-only node
```

## Rules

### `cmd/` contains wiring only

`cmd/toget/main.go` and `cmd/toget-official/main.go` parse flags, build dependencies, and call into `internal/`. No business logic lives here. The official binary reuses the same internal packages as the node; it is a different composition, not a fork.

### `internal/` boundaries

- `crypto` depends only on standard libraries and vetted crypto packages. It has no network, storage, or domain types.
- `protocol` contains generated types and validation. It does not perform I/O.
- `transport` moves bytes and manages connections. It knows nothing about rooms or approvals.
- `room`, `message`, `account`, `device`, `agent`, and `sync` own the domain rules and do not import HTTP, QUIC, or SQL drivers.
- `storage` defines interfaces; the SQLite implementation lives beside them. Domain code depends on the interface.
- `relay` forwards opaque envelopes and must not import `message` decryption or room keys.
- `official` composes the directory, update service, and official account host from the same building blocks as a self-hosted node.
- `ui` and `api` translate transport into domain calls; policy decisions belong in the domain.

### `proto/` is the node contract

- `.proto` files are the source of truth for node-to-node messages.
- Generated Go goes to `internal/protocol`; generated TypeScript goes to `web/packages/proto`.
- Never hand-edit generated files. CI runs `task generate` and fails on a diff.
- Breaking changes require a new package version (`toget/v2`), not a silent field change.
- `buf breaking` runs against the previous release in CI.

### `api/` is the local contract

- `api/openapi.yaml` describes the local HTTP API on `127.0.0.1:7718`.
- It covers rooms, messages, agents, approvals, devices, relay, settings, and audit.
- The CLI and the web UI are both clients of this API; neither gets a private backdoor.
- Generated Go server types and TypeScript clients are checked in and regenerated by CI.

### `web/` contains clients, not business logic

- `web/app` is the local UI SPA. It talks only through the local API and the event stream.
- `web/reset` is a static page. It must not load third-party scripts or telemetry; it reads the recovery credential locally with the File API and WebCrypto.
- `web/site` is the public project and documentation site. It can be statically generated and deployed separately.
- `web/packages/ui` is the design system; feature-specific components stay in the app.
- `web/packages/api-client` and `web/packages/proto` are generated.

### `deploy/` is per platform

- systemd, launchd, and Windows service definitions live here and are embedded into installers.
- Docker and Compose are convenience paths for self-hosted account hosts and relays.
- No secrets in this directory. Use environment variables, keychain, or root-readable files.

### `docs/` is part of the product

- Every expensive-to-reverse decision gets an ADR in `docs/decisions/`.
- `docs/architecture.md` describes the current design, not aspirational features.
- `docs/roadmap.md` defines what "done" means for each milestone.
- Docs are in English for the open-source community; Chinese summaries can live in the README and release notes.

### `tests/` is for cross-cutting tests

- Unit tests live next to the code.
- `tests/integration` covers multi-node, relay, storage, and sync behavior.
- `tests/e2e` covers user-visible flows with a real local node and a fake Agent.
- `tests/conformance` holds protocol fixtures and a small reference harness that an independent implementation can run.
- `tests/fixtures` holds shared data used by Go, TypeScript, and conformance tests.

## Naming conventions

- Go packages: short, lowercase, no underscores.
- TypeScript files: `kebab-case.ts` for modules, `PascalCase.tsx` for components.
- Feature folders: singular nouns in Go (`approval`, `agent`); plural route segments in the web app (`approvals`, `agents`).
- Event types: `domain.action`, past tense, consistently: `message.posted`, `approval.granted`.
- Environment variables: `TOGET_` prefix.
- Database tables: plural snake_case (`room_members`, `approval_requests`).
- Release directories: `daemon/<os>-<arch>/v<version>/`, immutable.

## Why one repository

- One change can update the protocol, node, official service, web UI, and docs together.
- One CI pipeline catches contract drift.
- One release train keeps versions aligned.
- Contributors can run the whole system with one command.
- The official service and the self-hosted node share code, which keeps the self-hosted path honest.

This is a modular monolith, not a pile of unrelated projects. Boundaries are enforced by packages and contracts, not by separate repositories.

## Private notes are not in this repository

The repository is public. Personal notes, credentials, customer data, and internal strategy belong outside it. Public docs may summarize product decisions, but they must not contain private information.
