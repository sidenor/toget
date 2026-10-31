# Roadmap (Draft)

> Dates are targets, not promises. A milestone is done only when its definition of done is met.

Toget is at the very beginning. The order below is chosen so that each milestone is useful on its own and does not have to be redesigned for the next one.

## M0 — Design and scaffold

**Goal:** make the project buildable and the decisions explicit.

- [x] Philosophy, product, architecture, protocol, security model, deployment, tech stack, and repository layout drafts
- [x] Go module, `cmd/toget`, `cmd/toget-official`, health check
- [x] Protobuf package `toget/v1` with the first messages
- [x] OpenAPI skeleton for the local API
- [x] React app shell with routing and a design-system baseline
- [x] `Taskfile` with `dev`, `test`, `generate`, `build`
- [ ] CI: Go tests, web build, lint, cross-platform build
- [x] ADRs for the account model, endpoint trust, mesh protocol, and versioning

**Done when:** `task dev` starts a local node and the web UI; `task test` passes on a clean checkout; CI is green on Linux, macOS, and Windows builds.

## M1 — One machine, one room

**Goal:** prove the local loop without a network.

- [x] Account creation on the local machine (self-hosted mode), 14-digit ID
- [x] Device identity and the signed server manifest
- [x] SQLite storage for accounts, rooms, events, and the outbox
- [x] Local web UI: create a room, send messages, see the timeline
- [x] CLI: `toget account create`, `toget room create`, `toget send`, `toget messages`
- [x] Local API with a session token and origin checks
- [x] Append-only event log with per-room ordering
- [x] Encrypted local storage at rest

**Done when:** a user can install the binary, create an account, create a room, and exchange messages with themselves on two local clients, entirely offline.

## M2 — Two nodes on a LAN

**Goal:** prove the room and identity model across machines.

- [x] Node-to-node handshake over pinned HTTPS on the LAN (QUIC is still planned)
- [x] Device enrollment approved by an existing device, with sealed account-key transfer and room-key snapshots
- [x] Invite a second account into a room; membership event and epoch rotation
- [x] End-to-end envelope: sealed payloads, X25519 + HKDF key wrapping, AEAD, AAD binding (HPKE migration optional)
- [x] Room home node: ordering, replication, per-participant copies, single retry on sequence conflict
- [x] History sync with cursors and idempotent replay
- [x] New-member history choice: none / recent (current epoch) / all, with a system notice
- [x] Direct connection by default; address and pin carried in the invite code (relay fallback added in M4)
- [x] mDNS advertisement and directory-free, signature-verified server-code lookup on one LAN

**Status:** the LAN milestone works for message rooms. Remote-room
attachments, remote leave/archive, and device enrollment are done; QUIC is
not. Relay fallback is implemented in M4. A packet capture still shows metadata (account/device
IDs in signed request headers) and sealed payloads; it does not show message
plaintext.

**Done when:** two people on two machines in the same LAN share a room, see each other's messages, and a new member joins without seeing old messages by default. A packet capture on either machine shows no plaintext room content on the wire.

## M3 — Agents in the room

**Goal:** make the room useful for real work.

- [x] Agent identity and registration on a device
- [x] Claude Code as the first runtime, driven by `toget` CLI and a skill
- [x] Session per (Agent, Room) with catch-up on missed messages
- [x] Mention-based invocation; no invocation without a mention
- [x] Tool receipts in the timeline: `tool.requested`, `tool.result`, `tool.failed`
- [x] Permission scopes and approval gates for external/shell/write/native actions
- [x] Action digest binding, expiry, single-use approvals
- [x] Agent status cards: idle, running, waiting for approval
- [x] Stop an active run from the CLI or web UI
- [x] Shared Agent: owner visibility warning on join

**Status:** the runtime loop is implemented. The daemon wakes an Agent on a
mention, serializes runs, catches up after the last completed run, resumes the
Claude Code session, posts the reply to the thread, and shows idle, running,
waiting, and offline status. A configurable command runtime is available for
other tools. Remaining: streaming partial output, remote-room agents, and a
stable connector SDK. See [agent-runtime.md](agent-runtime.md).

**Done when:** a user asks an Agent in a room to do a task, the Agent proposes a risky action, the user approves it in the conversation, and the result appears in the same thread with a complete audit trail.

## M4 — Across networks

**Goal:** make rooms work outside a LAN without exposing anyone's machine.

- [x] Relay protocol: registration, parent advertisement, route discovery, forwarding, short offline queue
- [x] Official directory and signed, self-certifying server manifests
- [x] Official relay as the default parent (directory `relay: "auto"`)
- [x] Relay selection and "no relay" settings (`--node-relay`, `--node-directory`, config)
- [x] Rate limits, quotas, size limits, and backpressure
- [x] Multi-hop path calculation and loop detection
- [x] Portable business cards as text or QR, with signed manifests and full-ID pinning
- [ ] QUIC hole punching, UDP port mapping, and general NAT traversal
- [ ] WebSocket transport (the implemented fallback is HTTPS long-poll)
- [ ] Standalone relay-only mode for ordinary nodes

**Status:** cross-network rooms work through a relay: encrypted messages,
attachments, pairing, and remote-room operations all travel as opaque signed
requests. The relay cannot read room content. Clients try a direct pinned
connection first and fall back to the relay; auto relay discovery, multi-hop
routing, loop detection, quotas, and a 55-second offline queue are implemented
and tested. NAT traversal is still absent, so a relay is required whenever the
LAN/VPN path is not reachable. Same-LAN mDNS discovery and portable QR
business cards are implemented; neither removes the relay requirement across
NAT. See [relay.md](relay.md), [discovery.md](discovery.md), and
[cards.md](cards.md).

**Done when:** two users behind different NATs share a room through a relay, the relay cannot read the messages, and a direct connection is used automatically when the network allows it.

## M5 — Official hosting and recovery

**Goal:** make the official service a real, optional convenience.

- Official-hosted accounts: blind ciphertext storage, MK wrapping, PAKE login
- Recovery credential: generation, forced download, local reset flow
- Account reset page: static, open source, no third-party scripts, local file read
- [x] Device management UI: list, rename, revoke (local and paired devices)
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
