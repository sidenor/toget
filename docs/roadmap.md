# Roadmap (Draft)

> Dates are targets, not promises. A milestone is done only when its definition of done is met.

Toget is at the very beginning. The order below is chosen so that each milestone is useful on its own and does not have to be redesigned for the next one.

## M0 — Design and scaffold

**Goal:** make the project buildable and the decisions explicit.

- [x] Philosophy, product, architecture, protocol, security model, deployment, tech stack, and repository layout drafts
- [x] Go module, `cmd/toget`, `cmd/toget-official`, health check
- [x] Protobuf package `toget/v1` with the first messages
- [x] OpenAPI skeleton for the local API
- [ ] React app shell with routing and a design-system baseline
- [x] `Taskfile` with `dev`, `test`, `generate`, `build`
- [ ] CI: Go tests, web build, lint, cross-platform build
- [x] ADRs for the account model, endpoint trust, mesh protocol, and versioning

**Done when:** `task dev` starts a local node and the web UI; `task test` passes on a clean checkout; CI is green on Linux, macOS, and Windows builds.

## M1 — One machine, one room

**Goal:** prove the local loop without a network.

- Account creation on the local machine (self-hosted mode), 14-digit ID
- Device identity and the signed server manifest
- SQLite storage for accounts, rooms, events, and the outbox
- Local web UI: create a room, send messages, see the timeline
- CLI: `toget account create`, `toget room create`, `toget send`, `toget messages`
- Local API with a session token and origin checks
- Append-only event log with per-room ordering
- Encrypted local storage at rest

**Done when:** a user can install the binary, create an account, create a room, and exchange messages with themselves on two local clients, entirely offline.

## M2 — Two nodes on a LAN

**Goal:** prove the room and identity model across machines.

- Node-to-node handshake over QUIC on the LAN
- Device enrollment approved by an existing device
- Invite a second account into a room; membership event and epoch rotation
- End-to-end envelope: outer/inner split, HPKE key wrapping, AEAD, AAD binding
- Room home node: ordering, replication, per-participant copies
- History sync with cursors and idempotent replay
- New-member history choice: none / recent / all, with a system notice
- Direct connection only; discovery by mDNS or a manual address; no relay yet

**Done when:** two people on two machines in the same LAN share a room, see each other's messages, and a new member joins without seeing old messages by default. A packet capture on either machine shows no plaintext room content on the wire.

## M3 — Agents in the room

**Goal:** make the room useful for real work.

- Agent identity and registration on a device
- Claude Code as the first runtime, driven by `toget` CLI and a skill
- Session per (Agent, Room) with catch-up on missed messages
- Mention-based invocation; no invocation without a mention
- Tool receipts in the timeline: `tool.requested`, `tool.result`, `tool.failed`
- Permission scopes and approval gates for `external.send`, `money.spend`, `destructive`
- Action digest binding, expiry, single-use approvals
- Agent status cards: idle, running, waiting for approval
- Shared Agent: owner visibility warning on join

**Done when:** a user asks an Agent in a room to do a task, the Agent proposes a risky action, the user approves it in the conversation, and the result appears in the same thread with a complete audit trail.

## M4 — Across networks

**Goal:** make rooms work outside a LAN without exposing anyone's machine.

- Relay protocol: parent advertisement, route discovery, forwarding, offline queue
- QUIC hole punching and NAT traversal; WSS fallback
- Official directory and signed server manifests
- Official relay as the default parent
- Relay selection and "no relay" settings
- Rate limits, quotas, and backpressure
- Multi-hop path calculation and loop detection

**Done when:** two users behind different NATs share a room through a relay, the relay cannot read the messages, and a direct connection is used automatically when the network allows it.

## M5 — Official hosting and recovery

**Goal:** make the official service a real, optional convenience.

- Official-hosted accounts: blind ciphertext storage, MK wrapping, PAKE login
- Recovery credential: generation, forced download, local reset flow
- Account reset page: static, open source, no third-party scripts, local file read
- Device management UI: list, rename, revoke
- Official room home node (ciphertext only)
- Signed release manifests, update service, atomic update and rollback
- Stable and beta channels

**Done when:** a user can register on the official service, lose their password, reset it with the recovery credential, and still open old history — while the official service never has a key to read it.

## M6 — Daily use

**Goal:** make it pleasant enough to use every day.

- PWA install and mobile approval flow
- Notifications with content-free payloads
- Policy language v1: domains, recipients, amounts, time windows
- Connector interface for non-Claude Agent runtimes
- Audit export with hash-chain proof
- Desktop shell with tray and auto-start
- English and Chinese UI
- Backup and restore flow with a tested restore

**Done when:** a user can install Toget on a laptop and a phone, run Agents from both, approve actions on the phone, and export a verifiable audit trail.

## Later

Only after the core is useful:

- Organizations: SSO (OIDC, then SAML), retention policies, SCIM, admin console
- Hosted billing
- More Agent runtimes and a connector SDK
- Public relay network and community infrastructure
- Reproducible builds and independent security review

## Sequencing rules

1. **End-to-end encryption from the first network message.** It is not a later layer; retrofitting it would change the protocol, storage, and UI.
2. **One loop first.** Do not add a second client or a second Agent runtime until the first loop is reliable.
3. **Local-first is never a demo mode.** Every feature must work on a self-hosted node before it depends on the official service.
4. **No protocol break without a version.** Additive changes first; N-2 support.
5. **No billing before retention.** Charge only after users return without being asked.
6. **No enterprise features before team features.** SSO is not a substitute for a useful room.
7. **Docs ship with the code.** A milestone is not done while the docs describe a previous design.

## Near-term task list

1. Scaffold the Go module, `cmd/toget`, and the health check.
2. Write `proto/toget/v1` for session, account, room, and message.
3. Define the SQLite schema for accounts, devices, rooms, events, and outbox.
4. Build the local API: account, rooms, messages, events.
5. Build the local web UI: room list, conversation, composer.
6. Implement the two-node handshake and a LAN room.
7. Implement the E2E envelope and test it with a packet capture.
8. Implement the Agent registration and the first approval card.
9. Write the first end-to-end test: create account → room → message → Agent → approval → result.
