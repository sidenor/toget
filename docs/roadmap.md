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

- [x] Node-to-node handshake over pinned HTTPS on the LAN, with HTTP/3 over QUIC as an alternate transport
- [x] Device enrollment approved by an existing device, with sealed account-key transfer and room-key snapshots
- [x] Account record sync between paired devices: certificate-verified device sets, account-signed revocation proofs, and read-only revoked-device discovery
- [x] Invite a second account into a room; membership event and epoch rotation
- [x] End-to-end envelope: sealed payloads, X25519 + HKDF key wrapping, AEAD, AAD binding (HPKE migration optional)
- [x] Room home node: ordering, replication, per-participant copies, single retry on sequence conflict
- [x] History sync with cursors and idempotent replay
- [x] New-member history choice: none / recent (current epoch) / all, with a system notice
- [x] Direct connection by default; address and pin carried in the invite code (relay fallback added in M4)
- [x] mDNS advertisement and directory-free, signature-verified server-code lookup on one LAN
- [x] Room-invite and device-pairing codes as terminal or PNG QR codes, with still-image scanning in the web UI

**Status:** the LAN milestone works for message rooms. Remote-room
attachments, remote leave/archive, device enrollment, and account-record sync
are done; invite and pairing codes can be shown as QR codes and scanned from a
still image; HTTP/3 over QUIC is available as an alternate transport. Relay
fallback is implemented in M4. A packet capture still shows metadata (account/device
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
- [x] Streaming partial output in the local API and web UI
- [x] Stable connector protocol v1, Go SDK, example connector, and `toget connector check`

**Status:** the runtime loop is implemented. The daemon wakes an Agent on a
mention, serializes runs, catches up after the last completed run, resumes the
Claude Code session, shows bounded partial output while it works, posts the
reply to the thread, and shows idle, running, waiting, and offline status. A
configurable command runtime speaks the stable [connector protocol
v1](connectors.md), with a Go SDK, a working example, and a dry-run command.
Partial output is transient and local to the Agent owner's node; it is not part
of the event log or visible to remote room members. See
[agent-runtime.md](agent-runtime.md).

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
- [x] WebSocket transport for relay sessions, with HTTPS long-poll fallback
- [x] Standalone relay-only mode: `toget relay`
- [x] Runtime relay switching from Settings and the local API
- [x] Signed relay-to-relay forwarding and per-sender byte quotas
- [x] HTTP/3 over QUIC for node and relay transports, with HTTPS fallback and `--no-quic`
- [x] Optional gateway port mapping (PCP, NAT-PMP, UPnP), STUN address fallback, and public `alt=` candidates
- [ ] UDP hole punching and relay-assisted NAT traversal; IPv6 preference

**Status:** cross-network rooms work through a relay: encrypted messages,
attachments, pairing, and remote-room operations all travel as opaque signed
requests. The relay cannot read room content. Clients try a direct pinned
connection first and fall back to the relay; auto relay discovery, multi-hop
routing, loop detection, quotas, a 55-second offline queue, signed
relay-to-relay hops, and a WebSocket tunnel with long-poll fallback are
implemented and tested. A relay can run without an account or database, and a
node can switch relays at runtime. A node can also ask a cooperating gateway
for a PCP, NAT-PMP, or UPnP port mapping and publish the public address as an
alternate; UDP hole punching is still absent, so symmetric NAT, CGNAT, and
networks without a supported mapping protocol still require a relay. Same-LAN mDNS discovery
and portable QR business cards are implemented; neither removes the relay
requirement across NAT. See [relay.md](relay.md), [discovery.md](discovery.md),
and [cards.md](cards.md).

**Done when:** two users behind different NATs share a room through a relay, the relay cannot read the messages, and a direct connection is used automatically when the network allows it.

## M5 — Official hosting and recovery

**Goal:** make the official service a real, optional convenience.

- [x] Self-hosted per-device recovery credential: generation, locked-password reset, unlocked local reset, password change
- [x] Recovery-file checksum, account/device/server binding, rate limiting, verifier/wrap rollback
- [ ] Official-hosted accounts: blind ciphertext storage, MK wrapping, PAKE login
- [ ] Official recovery credential: forced download at registration
- [ ] Account reset page: static, open source, no third-party scripts, local file read
- [x] Device management UI: list, rename, revoke (local and paired devices)
- [ ] Official room home node (ciphertext only)
- [x] Signed release manifests, update service, atomic update and rollback
- [x] Stable and beta channels
- [x] Periodic update checks, in-app banner, notify/auto/off modes, bounded postponement for forced releases
- [x] Signed release index with `latest` and yank, detached `.sig` sidecars, release-tree verification
- [x] Reproducible release builds with double-build verification, automatic SPDX 2.3 SBOMs, and an append-only publish script
- [x] `toget service install` for systemd, launchd, and Windows services
- [ ] Forced-update enforcement for official-hosted accounts

**Status:** self-hosted local-vault recovery is implemented; the official hosted
account flow, forced-download registration, and browser reset page are not.
Signed release keys, manifests, channels, a signed release index, a static
update service, atomic install, startup reconciliation, CLI rollback, periodic
checks, and an in-app update prompt are implemented. The forced level is
honored by the UI but is not server-enforced for official-hosted accounts,
which do not exist yet. See [account-recovery.md](account-recovery.md) and
[updates.md](updates.md).

**Done when:** a user can register on the official service, lose their password, reset it with the recovery credential, and still open old history — while the official service never has a key to read it.

## M6 — Daily use

**Goal:** make it pleasant enough to use every day.

- [x] PWA install and mobile approval flow
- [x] Notifications with content-free payloads
- [x] Policy language v1: domains, recipients, amounts, time windows
- [x] Connector interface for non-Claude Agent runtimes: v1 protocol, Go SDK, example, and `toget connector check`
- [x] Audit export with hash-chain proof
- [ ] Desktop shell with tray and auto-start
- [x] English and Chinese UI
- [x] Backup and restore flow with a tested restore

**Status:** policy language v1 is implemented end to end: room and per-Agent
rules, deny-wins precedence, allow-as-preauthorization, weekday and time
windows, API endpoints, and a web rule editor/evaluator. The connector
contract and bilingual UI are also done. Content-free Web Push is implemented
with locally generated VAPID keys, per-browser subscriptions, event toggles,
and a global quiet-hours switch. A Tauri 2 desktop scaffold is under
`desktop/`, but it has not been built or tested because this host lacks Rust and
WebKitGTK; the desktop shell is not done.
See [policies.md](policies.md) and [notifications.md](notifications.md).

**Done when:** a user can install Toget on a laptop and a phone, run Agents from both, approve actions on the phone, and export a verifiable audit trail.

## Later

Only after the core is useful:

- Organizations: SSO (OIDC, then SAML), retention policies, SCIM, admin console
- Hosted billing
- More Agent runtimes and connector templates
- Public relay network and community infrastructure
- Independent security review

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
