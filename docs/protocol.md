# Protocol (Draft)

> Status: design draft plus a working LAN implementation. Nothing here is
> frozen. The versioning rules are the part we intend to keep stable.

Toget has one protocol with three planes:

| Plane | Transport | Encoding | Used by |
| --- | --- | --- | --- |
| **Node protocol** | HTTPS on `7717` with a pinned self-signed certificate (QUIC is planned) | JSON envelopes | Nodes on the same LAN |
| **Local API** | HTTP on `127.0.0.1:7718` or a Unix socket | JSON (OpenAPI) | Local web UI, CLI, Agents on the same machine |
| **Directory API** | HTTPS on `443` | JSON + signed manifests (design only) | Clients resolving a server code |

End-to-end encryption sits above the node protocol: relays and hosts forward ciphertext, not content.

## What is implemented today

The LAN path is real and covered by tests in `internal/node` and
`internal/room`:

1. A host creates a room and a single-use invite. The local API returns a
   portable code: `toget-invite:v1:` followed by base64url JSON carrying the
   secret, node URL, server ID, room ID, and room name. The URL fragment pins
   the SHA-256 fingerprint of the node's persistent Ed25519 TLS certificate.
2. The joining node verifies the pin, posts a signed redeem request with its
   account and device public keys and device certificate, and receives a join
   bundle: room metadata, members, devices, wrapped room-epoch keys, and the
   events the inviter chose to share.
3. Requests are signed with the device key over the method, request URI,
   timestamp, nonce, and body hash. The node allows five minutes of clock
   skew and keeps a ten-minute nonce cache.
4. A remote sender prepares a sealed, signed event at the next sequence,
   posts it to the home node, and installs the accepted event locally. A
   sequence conflict returns `409` with the current head and the sender
   retries once after syncing.
5. Remote rooms are read-only locally except for sending messages. Local-only
   mutations are rejected so the home node remains the single log writer.

Still design-only: QUIC, Protobuf framing, relays, the directory, NAT
traversal, multi-device account sync, attachment upload into remote rooms,
and remote room administration. The rest of this document describes the
target design; sections marked “implemented today” above use HTTPS + JSON
rather than the future framing.

## Identifiers

| ID | Format | Notes |
| --- | --- | --- |
| Account ID | 14 decimal digits: 8-digit server code + 6-digit account code | Displayed `48392017-174628`. A lookup key, not a secret. |
| Server code | 8 decimal digits | `decimal(SHA-256(server_pubkey \|\| server_nonce)) mod 100_000_000`, zero-padded; the prefix of account IDs. |
| Server ID | Full fingerprint of the root public key | `tog1:s:<base32(SHA-256(root_pubkey))>`; the canonical server identity and the value used for collision resolution and pinning. |
| Device ID | 128-bit random, displayed as base32 | Stable for the life of the device. |
| Room ID | 128-bit random, displayed as base32 | Opaque to relays. |
| Message / event ID | UUIDv7 or 128-bit random + timestamp | Unique per producer; used for deduplication. |

Clients parse the account ID by its total length, so more digits can be added later without a new format.

**Collision rule.** The server code is a truncated hash, not an allocation. If two manifests claim the same code, the server with the lexicographically smaller `server_id` keeps it; the other increments `server_nonce`, recomputes the code, and re-signs its manifest. A server that already issued IDs signs redirects from the old prefix for a transition period. Directories and clients can verify the rule; no central arbiter is required.

## Discovery

1. The client extracts the server code from the account ID.
2. It asks the directory for the signed manifest of that server code, or uses a cached card, a QR code, peer exchange, mDNS on a LAN, or a manually entered address.
3. It verifies the manifest signature with the server root key and checks that `server_code == hash(root_pubkey || nonce)`.
4. It connects to one of the advertised endpoints, directly or through a relay.

The directory is a convenience, not a root of trust. It cannot forge a server's manifest, but the 8-digit code is short enough to grind; pin the full `server_id` from a QR code or cached card when first-contact impersonation matters.

### Server manifest

```json
{
  "server_id": "tog1:s:JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP",
  "server_code": "48392017",
  "root_pubkey": "ed25519:...",
  "server_nonce": "base64:...",
  "endpoints": [
    {"transport": "quic", "addr": "[2001:db8::1]:7717"},
    {"transport": "wss", "addr": "wss://node.example.com/toget"}
  ],
  "relay_parent": "48392018",
  "protocol_versions": [1],
  "issued_at": "2026-10-30T00:00:00Z",
  "expires_at": "2027-10-30T00:00:00Z",
  "signature": "ed25519:..."
}
```

`server_code` is the 8-digit short code derived from `root_pubkey` and `server_nonce`. `server_id` is the full fingerprint of `root_pubkey` and is the canonical identity used for collision resolution and pinning.

Manifests are short-lived and refreshable. A server can rotate endpoints without changing its code, because the code is derived from the root key and the nonce, not from the address.

## Transport and handshake

**Implemented today (LAN):** TLS 1.3 over TCP on `7717`, with a persistent
Ed25519 self-signed certificate. Peers do not use the public CA system; the
invite code carries an exact SHA-256 certificate pin in the URL fragment, and
the client aborts if the presented certificate does not match. The node
serves:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/toget/v1/health` | Liveness |
| `GET` | `/toget/v1/info` | Signed node manifest |
| `GET` | `/toget/v1/manifest` | Same manifest, explicit name |
| `POST` | `/toget/v1/invites/redeem` | Redeem an invite, return a join bundle |
| `POST` | `/toget/v1/rooms/sync` | Pull events after a cursor |
| `POST` | `/toget/v1/rooms/events` | Submit a sealed event for home-node ordering |

Every request except health carries `X-Toget-Timestamp`, `X-Toget-Nonce`,
`X-Toget-Signature`, `X-Toget-Account`, and `X-Toget-Device`. The signature
is Ed25519 over:

```text
toget-node-request-v1
METHOD
RequestURI
timestamp
nonce
hex(sha256(body))
```

**Target transport:** QUIC with TLS 1.3, ALPN `toget/1`. One control stream
for session messages, one stream per request, and datagrams reserved for
presence and typing. WSS over TCP `443` is the fallback for networks that
block UDP. IPv6, UPnP/NAT-PMP/PCP, and QUIC hole punching are attempted
before a relay is used.

Target handshake:

1. TLS 1.3 with certificate verification. The server also proves possession of the root key by signing the handshake transcript.
2. `hello` carries the protocol version, supported capabilities, device ID, and device certificate.
3. The responder returns its version, capabilities, server manifest, and a session nonce.
4. Both sides select the highest mutually supported protocol version. Unknown capabilities are ignored.
5. The session is bound to the device key; a reconnect can resume with a session ticket.

A protocol downgrade is refused. If the versions do not overlap, the connection closes with `protocol_incompatible`.

## Framing

Protobuf messages are sent as one message per QUIC stream (or one binary WSS frame), with a 4-byte length prefix on stream transports. Fields are additive: unknown fields must be preserved when forwarding and ignored when reading.

The `.proto` files live in `proto/` and are the source of truth for the node protocol. Generated Go and TypeScript code is checked in but never edited by hand.

## Node protocol messages

### Session

- `hello` — version, capabilities, device certificate.
- `auth` — proves device ownership; for login, carries the PAKE message.
- `ping` / `pong` — liveness.
- `goaway` — planned shutdown or redirect to another endpoint.

### Account

- `account.register` — create an account on this host; returns the 14-digit ID and the account record.
- `account.login` — PAKE exchange; returns a session token and the sync cursor.
- `account.devices` — list, rename, or revoke devices.
- `account.rotate` — rotate the account key; old device certificates are re-signed or revoked.

### Devices

- `device.enroll` — a new device joins the account with a certificate signed by the account key.
- `device.revoke` — remove a device; its room keys stop being wrapped to it.
- `device.presence` — online/offline hint for the local UI.

### Rooms

- `room.create` — create a room; the creator's account host becomes the home node by default.
- `room.invite` — invite a person or Agent, with an optional history attachment choice.
- `room.join` / `room.leave` — membership changes; triggers an epoch rotation.
- `room.members` — current membership with roles and device endpoints.
- `room.settings` — name, avatar, notification defaults, Agent permissions.
- `room.event` — append an event; the home node assigns the room sequence.
- `room.sync` — request events after a cursor, or a snapshot plus a delta.
- `room.history.attach` — deliver selected history to a new member (re-encrypted, see below).

### Messages

- `message.send` — send an encrypted envelope to the home node.
- `message.ack` — confirm receipt or storage.
- `message.deliver` — push an envelope to a member endpoint.
- `message.read` — read cursor for unread counts.

### Relay

- `relay.advertise` — announce a parent relay and its reachable address.
- `relay.route` — ask for a path to a target server code.
- `relay.forward` — forward an opaque envelope.
- `relay.queued` / `relay.delivered` — offline queue status.
- `relay.stats` — quota usage and backpressure hints.

### Agents

- `agent.register` — register an Agent identity on a device.
- `agent.status` — idle, running, waiting for approval, offline.
- `agent.invoke` — ask an Agent to act on a room event.
- `agent.permission.request` — ask for a scoped permission.
- `agent.result` — stream output or a final result back into the room.

### Approvals

- `approval.request` — create a blocking approval card.
- `approval.decide` — grant or deny; includes the action digest.
- `approval.expire` — automatic expiry.

## End-to-end envelope

Every room message has two layers.

### Outer envelope (visible to relays and hosts)

| Field | Purpose |
| --- | --- |
| `version` | Envelope format version |
| `message_id` | Unique ID, used for dedupe |
| `target` | The home node or recipient endpoint; no room ID |
| `sent_at` | Sender clock, bounded by a replay window |
| `size` | Ciphertext length |
| `sender_device` | Sender's device ID |
| `sender_sig` | Ed25519 signature over the outer header |

The outer envelope deliberately does not carry the room ID, the sender's account ID, or the message type. The home node still has to assign a per-room sequence; how it learns the room without exposing the room ID to relays is an open question (a per-room routing token visible only to the home node, or a global order with client-side sequencing).

### Inner message (encrypted)

| Field | Purpose |
| --- | --- |
| `room_id` | Room identifier |
| `epoch` | Room key epoch |
| `sender` | Account ID and device ID |
| `type` | `message`, `card`, `approval`, `tool.receipt`, `system`, ... |
| `body` | Text or structured payload |
| `attachments` | Object IDs and per-file keys |
| `mentions` | Account IDs or Agent IDs |
| `reply_to` | Optional parent message |
| `created_at` | Sender timestamp |

### Encryption

**Implemented today:** payloads are sealed with the room epoch key using
XChaCha20-Poly1305. A room key is wrapped per device with an ephemeral X25519
key, HKDF-SHA256, and XChaCha20-Poly1305. The wrapped-key AAD binds
`toget:room-key-wrap:v1`, room ID, epoch, and device ID. The vault seals the
local copy of each epoch key. Unshared epochs are transferred as opaque chain
anchors so sequence numbers and hashes stay verifiable without revealing
history.

- The inner message is encrypted with the room epoch key using XChaCha20-Poly1305 (or AES-256-GCM where hardware acceleration matters).
- The room epoch key is 256 bits, generated by the room creator's endpoint and re-generated on every membership change.
- The epoch key is wrapped to each member endpoint with HPKE (RFC 9180, X25519 + HKDF-SHA256 + ChaCha20-Poly1305).
- The AAD binds the outer header, `room_id`, `epoch`, and `message_id`, so a relay cannot move ciphertext between rooms or replay it as a different message.
- Attachments are encrypted with a per-file key; the key travels in the inner message, and the ciphertext lives wherever the sender chooses (local node, object storage, or direct transfer).

## Room events

Events are appended by the home node and replicated to members.

| Type | Meaning |
| --- | --- |
| `room.created` | Room exists; initial epoch |
| `room.member_invited` | Invitation sent, with history choice |
| `room.member_joined` | Member accepted; epoch rotated |
| `room.member_left` | Member left or was removed; epoch rotated |
| `room.epoch_rotated` | New epoch key wrapped to current members |
| `room.history_attached` | Selected history delivered to a new member |
| `message.posted` | A message or card |
| `message.edited` | A new event referencing the original (history is append-only) |
| `approval.requested` | A blocking approval card |
| `approval.granted` / `approval.rejected` / `approval.expired` | Decision |
| `tool.requested` / `tool.result` / `tool.failed` | Agent tool receipts |
| `agent.status` | Agent presence in the room |
| `agent.joined` / `agent.left` | Agent membership |
| `room.settings_changed` | Name, permissions, notification defaults |

Each event carries the room sequence assigned by the home node. Clients treat the sequence as the ordering key and the event ID as the deduplication key.

## History sync

- Each device keeps a per-room cursor: the last sequence it has stored.
- On reconnect, it sends `room.sync { after: cursor }`; the home node returns the delta.
- A device far behind can ask for a snapshot plus a delta instead of replaying every event.
- Every participant stores its own copy; sync repairs gaps, it does not centralize history.
- If the home node is unreachable, the room is read-only for new ordered events until it returns or a new home is chosen. The failover policy is still open.

## New-member history

The inviter chooses one of:

- **No history (default).** The new member sees messages from the join event onward.
- **Recent.** The current epoch only; older epochs remain opaque.
- **Everything.** Every epoch the room still has keys for.

**Implemented today:** history is shared at epoch granularity. The join
bundle wraps the selected epoch keys to the new device and includes sealed
events for all epochs; unshared epochs travel as opaque chain anchors. A
`room.history_attached` event and a system notice record what was shared.
Time-window selection (for example “last 7 days”) is design-only.

The inviter can only attach history they can see. Inviting is not a way to forward messages the inviter could not access.

## Relay protocol

- Every node advertises a parent relay. The default parent is the official service.
- To reach a target, a node asks its parent for a route. The parent either answers with a path through the relay tree or forwards the request upward.
- The sender and receiver find the lowest common ancestor; only the hops on that path carry the envelope.
- Relays forward opaque envelopes and see only the outer header.
- Offline messages are queued for a bounded TTL and capacity; the sender is told whether an envelope was delivered or queued.
- Relays enforce per-device quotas and return `relay.quota_exceeded` rather than silently dropping messages.

Parent-table merge rules, maximum hop count, and the exact meaning of "no relay" are still open.

## Local API

The local API is how the local web UI, the CLI, and Agents talk to the daemon.

- HTTP JSON with an OpenAPI 3.1 description in `api/openapi.yaml`.
- Bound to `127.0.0.1:7718` or a Unix socket; never exposed to the network by default.
- A per-session token is written to a local file readable only by the user; the web UI receives it at load time.
- Origin checks and `SameSite=Strict` cookies for browser requests.
- Endpoints mirror the node protocol conceptually: rooms, messages, agents, approvals, devices, relay, audit.
- The CLI is a thin client over this API, so scripts and Agents use the same surface as the UI.

## Agent protocol

- An Agent is registered on a device with its own identity and scoped permissions.
- The first runtime is Claude Code, driven by `toget` CLI commands and a skill.
- A session is created per (Agent, Room); it catches up on missed messages when invoked.
- An Agent is not invoked unless mentioned or addressed, unless a room policy says otherwise.
- Tool calls and results are reported as `tool.*` events, so the work is visible in the timeline.
- Risky actions wait for an approval; the Agent is suspended until the decision arrives.

## Approval binding

An approval authorizes exactly one action:

- The Agent computes a canonical `action_digest` over the tool, target, parameters, amount, and recipient.
- `approval.request` includes the digest and a human-readable description generated from the same canonical form.
- `approval.granted` includes the same digest.
- The daemon recomputes the digest before execution and refuses on mismatch.
- Approvals are single-use, expire after a bounded time, and cannot be reused for a retry with different parameters.

## Errors

| Code | Meaning |
| --- | --- |
| `protocol_incompatible` | No overlapping protocol version |
| `auth_failed` | PAKE or device proof failed |
| `device_revoked` | Device is no longer valid |
| `not_a_member` | Caller is not a room member |
| `epoch_unknown` | Message references an epoch the caller does not hold |
| `relay_quota_exceeded` | Relay quota or rate limit hit |
| `home_unreachable` | Room home node is offline |
| `approval_required` | The action needs a human decision |
| `approval_mismatch` | Action digest differs from the approved one |
| `payload_too_large` | Message or attachment exceeds the limit |
| `version_unsupported` | Release is outside the support window |

Errors are structured (`code`, `message`, `details`) and stable across minor versions.

## Versioning and compatibility

- **Protocol version:** integer, negotiated in the handshake.
- **Capabilities:** optional features are advertised as flags; peers never assume them.
- **Support window:** the current and previous two minor app versions (N-2) remain compatible.
- **Additive changes:** new message types, new fields, and new event types are allowed within a protocol version.
- **Breaking changes:** require a new protocol version and an overlap period of at least six months.
- **Unknown data:** unknown fields and event types are preserved when forwarding and ignored when reading.
- **Forced updates:** only for protocol-incompatible or security-critical releases, and only for devices that can reach the official service.

## Security requirements

- TLS 1.3 everywhere; no plaintext node traffic.
- Servers prove possession of their root key during the handshake, not just a valid certificate.
- Relays and hosts never receive room keys or plaintext.
- Message IDs and timestamps are checked against a replay window; AAD binds ciphertext to its room and epoch.
- The local API requires a token and is not reachable from the network by default.
- Rate limits apply per device, per account, and per IP; payload size limits are enforced before decryption.
- Agent output and tool results are untrusted input; they are validated against schemas and never evaluated as code.
- The protocol has no remote shell, no arbitrary file read, and no privileged "official" channel.

## Open questions

- Exact Protobuf package layout and stream multiplexing details.
- Room ordering when the outer envelope hides the room ID: per-room routing token vs. global order.
- Relay parent-table merge rules, maximum hops, and "no relay" semantics.
- Home-node failover behavior.
- Whether new-member history uses re-encryption, old-epoch grants, or both.
- Push notification delivery for offline mobile clients.
- The first stable wire format for Agent streaming output.
