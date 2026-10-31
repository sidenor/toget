# Technology Stack (Draft)

> Status: proposed direction. Exact versions are pinned in `.mise.toml`, `go.mod`, and `pnpm-lock.yaml`.
>
> **Current implementation note:** the LAN node uses Go's standard `net/http`
> over TLS 1.3 with a pinned self-signed Ed25519 certificate and JSON
> payloads (`internal/node`), not QUIC or Protobuf yet. Go, SQLite, React,
> TypeScript, and Vite match this document. QUIC/WSS and Protobuf remain the
> target for relay-scale transport.

## Decision summary

| Layer | Choice | Why |
| --- | --- | --- |
| Node, daemon, account host, relay | **Go** | Static binary, cross-platform services, strong concurrency, predictable memory |
| Node transport | **QUIC** (`quic-go`), WSS/TCP `443` fallback | Low-latency multiplexing, NAT traversal, HTTPS fallback for hostile networks |
| Node encoding | **Protobuf** | Compact, versionable, generated types in Go and TypeScript |
| Local API | **HTTP JSON + OpenAPI 3.1** on `127.0.0.1:7718` | Easy to inspect, script, and generate clients for |
| Local web UI | **TypeScript + React + Vite** | Largest UI ecosystem; one codebase for web, desktop shell, and PWA |
| UI system | **Tailwind CSS + Radix + shadcn/ui** | Accessible primitives, copy-in components, no design-system lock-in |
| State | **TanStack Query** + minimal local state | Server state dominates: rooms, events, approvals |
| Local storage | **SQLite** (`modernc.org/sqlite`) | Embedded, zero-ops, `CGO_ENABLED=0`, good for offline outbox and room copies |
| Large official storage | **PostgreSQL** (optional) | Only when the official service outgrows SQLite |
| Crypto | **Go stdlib + `golang.org/x/crypto` + `cloudflare/circl`** | Ed25519, X25519, HKDF, ChaCha20-Poly1305, HPKE, Argon2id, SPAKE2 |
| CLI | **Cobra** | Standard Go CLI structure |
| Logging | **log/slog** | Structured logs without a framework |
| Metrics | **Prometheus client**, local only | Self-hosters can scrape; nothing is sent by default |
| Tests | **Go testing, Vitest, Playwright** | Standard tools; plus protocol conformance and fuzz tests |
| Build | **GoReleaser + Taskfile + mise** | Cross-platform artifacts, reproducible toolchain |
| Signing | **Ed25519 release manifests**, checksums, SBOM | Root-certified release keys; verifiable updates |
| Desktop (later) | **Tauri 2** | Small shell, system webview, reuses the web UI |
| Mobile (later) | **PWA first, native shell later** | One UI codebase until push/background requires more |

## Why Go for the node

The node is the hardest part to distribute and operate:

- it runs on Linux, macOS, and Windows, often as a background service;
- it must survive logout and reboot, supervise Agent processes, and hold local keys;
- it must multiplex many peer connections and relay traffic;
- it must be installable by someone who is not a systems expert.

Go fits:

- **One static binary.** `CGO_ENABLED=0` cross-compilation gives a single file per OS/arch. No runtime, no virtualenv, no DLLs.
- **Cross-platform services.** systemd, launchd, and Windows services share one abstraction.
- **Good process control.** `os/exec`, signals, stdio pipes, and context cancellation are first-class; a PTY layer can be added behind an interface.
- **Low idle cost.** A node runs all day; Go's memory and startup behavior are predictable.
- **Stable language.** Go's compatibility promise protects long-lived code.
- **Shared code.** Protocol types, crypto wrappers, event validation, and storage interfaces live in one module and are tested once across the node, account host, and relay.

Go is also used for the official service. The same binary with different subcommands keeps the self-hosted and official paths honest: they share the protocol and the storage code.

## Why QUIC and Protobuf

- **QUIC** gives independent streams over one connection, cheap reconnects, and built-in TLS 1.3. It works well for many rooms and many peers without head-of-line blocking.
- **WSS/TCP 443** is the fallback for networks that block UDP or only allow HTTPS. It carries the same Protobuf messages in binary frames.
- **Protobuf** is compact, forward-compatible, and generates code for Go and TypeScript. Unknown fields are preserved, which matters for an open protocol with independent implementations.
- **JSON stays at the edges**: the local API, the directory API, manifests, and configuration. Humans debug those; machines exchange Protobuf.

We are not using libp2p. It is a large dependency with its own identity, routing, and transport stack, and Toget's relay tree and account model are specific enough that we would fight it more than use it. We borrow ideas, not the dependency.

## Why not Matrix

Matrix is a mature room protocol with a large ecosystem. It was considered seriously. Toget differs in ways that are hard to retrofit:

- Matrix's room model is replicated across homeservers by default; Toget keeps history with participants and makes the official service optional and blind.
- Toget's account ID, device model, and relay tree are designed around local-first nodes, not always-on homeservers.
- Toget's Agent identity and approval model is a core object, not an extension.

Using Matrix would mean adopting its trust model and event graph. Building a small protocol gives us the privacy and hosting properties the product needs.

## Why TypeScript and React for the web

- **One UI, several shells.** The local web UI can run as a normal SPA, inside Tauri, and as a PWA. No second UI codebase.
- **Ecosystem.** React has the largest component, testing, and hiring ecosystem; Radix and shadcn/ui provide accessible primitives that can be edited rather than fought.
- **Typed contract.** OpenAPI generates the local API client; Protobuf generates event types. The UI does not hand-write wire types.
- **Fast iteration.** Vite's dev server and React Fast Refresh keep the loop short.

Details:

- **Router:** React Router. The app is a SPA; no SSR.
- **Server state:** TanStack Query for caching, retries, and invalidation.
- **Local state:** Zustand only where needed.
- **Styling:** Tailwind with CSS variables for theming.
- **Forms:** React Hook Form + Zod.
- **i18n:** `react-i18next` from the start, with English and Chinese catalogs.
- **PWA:** installable, with an approval experience that works on a phone before a native app exists.

### Why not Next.js

The authenticated UI is a realtime room list and conversation, not a content site. SSR adds a server runtime, caching rules, and deployment complexity we do not need. A static SPA served by the local daemon or a CDN is simpler and easy to embed in Tauri or a mobile shell.

## Why SQLite everywhere at first

- **Daemon:** room copies, event outbox, sync cursors, device and Agent registry cache. It must work offline and survive restarts.
- **Account host:** account records, device list, membership, and ciphertext for hosted rooms. A single-node self-hosted account host is the common case, and SQLite handles it without a database server.
- **Official service:** starts on SQLite too. If load demands it, the store interface allows PostgreSQL for the account and room metadata; ciphertext blobs can move to object storage.

The pure-Go `modernc.org/sqlite` driver keeps cross-compilation free of CGO. Queries are written in SQL and reviewed; no ORM hides them. Migrations are plain SQL, embedded in the binary, and applied explicitly or at startup.

Why not Postgres by default: requiring a database server to run a personal node would break the "install one binary" promise. Postgres is an option for scale, not a prerequisite for use.

## Cryptography

- **Ed25519** for signatures: device certificates, outer envelopes, manifests.
- **X25519** for key agreement.
- **HKDF-SHA256** for key derivation.
- **HPKE (RFC 9180)** with X25519 + HKDF-SHA256 + ChaCha20-Poly1305 for wrapping room epoch keys to member devices. `cloudflare/circl` provides the implementation.
- **XChaCha20-Poly1305** for message content; AES-256-GCM where hardware acceleration is clearly better.
- **Argon2id** for password verifiers and key derivation from passwords.
- **SPAKE2** (via `circl/pake`) for password-authenticated login, so the host never sees the password. If a mature OPAQUE implementation becomes available, it is a candidate to replace SPAKE2.
- **TLS 1.3** for transport; server identity is additionally proven with the signed manifest.

Rules:

- No custom cryptography.
- No keys in logs, crash reports, or telemetry.
- Version the envelope and the key schedule so a future algorithm can be introduced without breaking old clients.
- Treat every crypto dependency as a security dependency: pinned, reviewed, and scanned.

## Local API and contract

- `api/openapi.yaml` is the source of truth for the local HTTP API.
- `proto/` is the source of truth for the node protocol.
- `oapi-codegen` generates Go server types; `openapi-typescript` generates the web client.
- `buf` generates Go and TypeScript from Protobuf and checks for breaking changes.
- CI regenerates everything and fails if the working tree changes, so generated code never drifts.
- The CLI and the local web UI use the same local API. There is no private backdoor for the UI.

## Authentication and secrets

- **Account login:** password via PAKE; the account host stores only a verifier.
- **Local vault (implemented):** each device has a random vault key wrapped by an Argon2id-derived KEK from the password; an optional recovery key wraps the same vault key.
- **Official-hosted accounts (planned):** MK is generated client-side and stored as `Enc(KEK(password), MK)`; the recovery credential stores `Enc(RK, MK)`.
- **Devices:** each device has its own key pair and a certificate signed by the account key. Device enrollment requires approval from an existing device or the account host.
- **Local API:** per-session token in a user-only file; `Origin` checks and `SameSite=Strict` cookies.
- **Secrets at rest:** OS keychain (Keychain, Credential Manager, Secret Service) or an encrypted local vault for headless Linux. Agent API keys never go to the account host.

## Repository and build tooling

- **One Go module** at the repository root.
- **Two binaries:** `cmd/toget` (node, daemon, CLI, relay, account host) and `cmd/toget-official` (directory, official account host, update service). The official binary reuses the same internal packages.
- **pnpm workspaces** for `web/*` and `packages/*`.
- **mise** pins Go, Node, pnpm, buf, and other tools.
- **Taskfile** is the single entry point for development: `task dev`, `task test`, `task generate`, `task build`. Release signing and channel generation live in `toget-official release`, not in Taskfile.
- **No Nx, Bazel, or Turborepo at the start.** They add concepts a small project does not need yet. Turborepo can be added later for caching without changing the layout.

## Testing and quality

- **Go:** standard `testing`, table-driven tests, `httptest`, in-memory SQLite for store tests.
- **Protocol:** golden Protobuf fixtures, round-trip tests, fuzz tests for every decoder, and conformance tests that a second implementation could reuse.
- **Crypto:** known-answer tests for HPKE, AEAD, HKDF, and signatures; property tests for envelope binding.
- **Web:** Vitest and React Testing Library; Playwright for end-to-end flows.
- **Multi-node:** integration tests spin up two or three nodes, a relay, and a fake Agent on localhost.
- **Cross-platform CI:** build and test on Linux, macOS, and Windows, `amd64` and `arm64` where practical.
- **Static analysis:** `go vet`, `staticcheck`, `govulncheck`, ESLint, TypeScript strict mode, `pnpm audit`.
- **Generated-code check:** CI fails if regeneration produces a diff.
- **Release checks:** signature and checksum verification, archive-safety and state-validation tests, and an end-to-end smoke test that installs the previous version, stages and finishes an update, rolls back, and exercises automatic rollback after a failed restart.

## Release and signing

- Release artifacts per device: `daemon/<os>-<arch>/v<version>/`, immutable.
- `channels/stable.json` and `channels/beta.json` point at a release and a rollout percentage.
- `manifests/v<version>.json` lists artifacts, hashes, protocol and schema versions, minimum supported versions, and the release level.
- The manifest is signed by a release key; the release key is certified by the offline root key.
- Clients embed the root public key and refuse anything that does not chain to it.
- `toget-official release` generates keys, certificates, manifests, and channels, and can verify a channel exactly as a client would. The static update service serves the release tree.
- Deterministic client-side canary buckets support 1% → 10% → 50% → 100%; automatic rollout pause needs external monitoring, because the service is static.
- The manifest can reference an SBOM. Automated SBOM generation and reproducible builds are still goals.

## Deployment and operations

- **Self-hosted:** one binary and one SQLite file. Docker is optional; systemd is the reference.
- **Official:** the same packages behind a load balancer, with Postgres and object storage when scale requires it.
- **Observability:** structured logs, local Prometheus metrics, optional OpenTelemetry traces; no telemetry sent anywhere by default.
- **Backups:** encrypted local archives; the official service backs up ciphertext and account metadata, never plaintext.

## Desktop and mobile later

- **Desktop:** Tauri 2 wraps the same web build and bundles the `toget` binary as a sidecar. Tray, auto-start, deep links, native notifications.
- **Mobile:** PWA first. If push, background execution, or app-store distribution become necessary, a native shell wraps the same web build. React Native is not the default because it would fork the UI.

## Version policy

- **Go:** current stable; `go.mod` pins the minimum.
- **Node:** active LTS, pinned in `.mise.toml` and CI.
- **Protocol:** integer version, N-2 support window, additive changes first.
- **Schema:** integer version with forward migrations and backups before migration.
- **Dependencies:** prefer stable APIs and active maintainers; avoid dependencies that own the domain model.
- **Cryptography:** pin versions, review advisories, and keep an upgrade path for every primitive.

## Rejected alternatives

| Alternative | Why not |
| --- | --- |
| **Matrix** | Mature, but its homeserver replication and trust model conflict with local-first, participant-held history, and a blind official service |
| **libp2p** | Large identity/routing/transport stack; Toget's relay tree and account model are specific enough that we would fight it |
| **All TypeScript / Node daemon** | Cross-platform service install and native modules are fragile; packaging a Node runtime is heavy |
| **Rust for the node** | Excellent safety and performance, but slower iteration for a small team; a future performance-critical helper can be Rust |
| **Python** | Great for prototypes, weaker for long-running cross-platform services and packaging |
| **Elixir/Phoenix** | Strong realtime story, but a second backend ecosystem and a smaller pool for this project |
| **Next.js** | SSR and a server runtime are unnecessary for an authenticated SPA |
| **GraphQL** | More moving parts than REST + typed events for this data model; harder for third-party clients |
| **gRPC everywhere** | Browser and debugging complexity; Protobuf over QUIC/WSS plus a JSON local API is simpler to adopt |
| **Postgres by default** | Forces every personal node to run a database server |
| **MongoDB** | The data is relational (accounts, devices, memberships, events) and benefits from transactions |
| **Microservices** | Premature; a modular monolith with clear packages can split later if measurements justify it |
| **Kubernetes-first** | Too much operational surface for the MVP; a binary and Compose first |
| **Proprietary BaaS** | Convenient at first, but creates migration risk and weakens the self-hosted promise |

## What would make us revisit a decision

- A platform requirement the node cannot meet in Go.
- QUIC unavailable or unreliable on a meaningful share of networks, making WSS the primary path.
- SQLite becoming a proven bottleneck for the official service.
- A crypto primitive losing its security margin, requiring a versioned migration.
- A protocol requirement that QUIC + Protobuf cannot express.

If a change is needed, the protocol contract and domain packages stay the same; only the adapter or runtime changes. That is what the boundaries are for.

## The short version

**Go for the node and the official service. QUIC plus a WSS fallback, Protobuf on the wire. SQLite first. React for the local UI. Standard cryptography only. One repository, one release train, boring standards.**
