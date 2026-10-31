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
5. Remote rooms are read-only locally except for messages, attachments, and
   self-leave. Other local-only mutations are rejected so the home node
   remains the single log writer.
6. Attachments in remote rooms are sealed with the current room-epoch key on
   the sender, then uploaded as opaque ciphertext to the home node. Metadata
   travels in the signed request URI. Other devices sync the metadata and
   download ciphertext on first open, verifying the SHA-256 and size of the
   decrypted plaintext.
7. A remote member can leave: the device prepares and signs `room.member_left`
   at the next sequence, the home node verifies that the author is leaving
   themselves, appends the event, marks the member left, and rotates the epoch.
   The departing device installs the event without rotating its own copy and
   archives the room locally.

8. A second device can recover the account by pairing with an existing
   device. The new device submits its public keys and a one-time secret; the
   existing device shows a fingerprint, and after the user approves, signs a
   device certificate and returns the account key sealed to the new device.
   Room snapshots carry membership and epoch keys (not events), and the new
   device syncs event history from each room's home.

Both `toget-invite:v1:` and `toget-pair:v1:` are display-agnostic strings. The
CLI can render the same string as a terminal or PNG QR code, and the web UI can
decode it from a still image locally. The wire format and security rules do not
change: a QR code is another way to carry the same single-use invite or
short-lived pairing secret, not a new credential.

Still design-only: QUIC, Protobuf framing, NAT traversal or hole punching,
and WebSocket transport. Relays and the directory are implemented with HTTPS +
JSON rather than the future Protobuf framing; see [relay.md](relay.md) for
their wire format, trust boundaries, and limits. LAN discovery is implemented
with a small mDNS responder and browser; see [discovery.md](discovery.md).
Portable business cards carry signed manifests as text or QR codes; see
[cards.md](cards.md). The rest of this document describes the target design;
sections marked “implemented today” use HTTPS + JSON rather than the future
framing.

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

**Collision rule (design).** The server code is a truncated hash, not an allocation. If two manifests claim the same code, the server with the lexicographically smaller `server_id` keeps it; the other increments `server_nonce`, recomputes the code, and re-signs its manifest. A server that already issued IDs signs redirects from the old prefix for a transition period. Directories and clients can verify the rule; no central arbiter is required.

**Collision rule (implemented).** The directory rejects a second server claiming an existing code with `code_taken`; deterministic nonce migration and redirects are not implemented yet. The full `server_id` in an invite or pinned card remains authoritative.

## Discovery

1. The client extracts the server code from the account ID.
2. It looks for a signed manifest through the local mDNS cache, the configured directory, a cached card, a QR code, peer exchange, or a manually entered address.
3. It verifies the manifest signature with the server root key and checks that `server_code == hash(root_pubkey || nonce)`.
4. It connects to one of the advertised endpoints, directly or through a relay.

Implemented today: `toget node lookup <code>` first tries a saved [business card](cards.md), then verifies a node advertising that code on the local network, then asks the configured directory. It re-verifies the identity manifest and the signed node manifest locally in every case. A card or LAN result is fetched from `/toget/v1/identity` and `/toget/v1/info` over the pinned certificate. Every result includes the node's HTTPS endpoint, TLS pin, and signed relay route; none of the card, mDNS, or directory is trusted alone. A saved card pins the full server ID for its code, so a different server presenting the same eight-digit code is rejected.

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
| `GET` | `/toget/v1/identity` | Signed account identity manifest, used for directory-free LAN verification |
| `POST` | `/toget/v1/invites/redeem` | Redeem an invite, return a join bundle |
| `POST` | `/toget/v1/rooms/sync` | Pull events after a cursor |
| `POST` | `/toget/v1/rooms/events` | Submit a sealed event for home-node ordering |
| `POST` | `/toget/v1/rooms/leave` | Submit a signed self-leave event; home rotates the epoch |
| `POST` | `/toget/v1/rooms/attachments` | Upload sealed attachment ciphertext; metadata in the signed query |
| `GET` | `/toget/v1/attachments/{id}` | Download sealed attachment ciphertext for a room member |
| `POST` | `/toget/v1/pair/request` | Submit a pairing request with fresh device keys |
| `POST` | `/toget/v1/pair/complete` | Poll an approved pairing and receive the sealed account bundle |
| `POST` | `/toget/v1/account/sync` | Exchange public account device records with a sibling device |
| `POST` | `/toget/v1/rooms/devices` | Register this device in a room with its account-signed certificate |
| `POST` | `/toget/v1/rooms/devices/revoke` | Ask a room home to revoke a device and rotate the epoch |

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

**Target transport (design):** QUIC with TLS 1.3, ALPN `toget/1`. One control
stream for session messages, one stream per request, and datagrams reserved for
presence and typing. WSS over TCP `443` is the fallback for networks that block
UDP. IPv6, UPnP/NAT-PMP/PCP, and QUIC hole punching would be attempted before a
relay is used.

**Implemented transport:** pinned HTTPS + JSON on TCP `7717`, HTTP/2 where
available. If the direct health probe fails, the node client uses an outbound
HTTPS long-poll tunnel to a relay. No UDP, port mapping, or hole punching is
implemented.

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
| `agent_id` | Optional display attribution to a local Agent owned by the sender |
| `reply_to` | Optional parent message |
| `created_at` | Sender timestamp |

`agent_id` is routing and display metadata, not a new author identity: the
payload is still signed by the owning account's device. A receiving endpoint
must not treat it as proof that the Agent itself approved or authored the
content.

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

### Remote attachments

- The sender seals the file with the current room-epoch key, then uploads the
  ciphertext to the home node. The home node never sees the plaintext or the
  key and only stores bytes plus membership-visible metadata.
- Metadata (`name`, `mime`, `size`, `sha256`, `epoch`) is carried in the signed
  query string, so the home node can bind it to the uploading device without
  decrypting the body.
- Peers receive attachment metadata in join and sync bundles. A file is
  downloaded lazily on first open and cached locally.
- After decryption the reader verifies the plaintext size and SHA-256 from the
  event metadata. A mismatch is treated as a failed download, not a warning.

### Remote leave and archive

- Leaving is a self-service action: a device signs `room.member_left` naming
  its own account. The home node rejects a leave event whose signature, device,
  or author identity does not match.
- The home node appends the event, marks the member left, and rotates the room
  epoch. Future sync requests from the departed device are rejected.
- `room.archived` is a room-wide decision by the owner on the home node and
  propagates to members through sync.
- A member can also archive a remote room locally for themselves. This writes
  no event and is not overwritten by sync unless the home node itself archives
  the room.

### Device pairing

**Implemented today for LAN nodes.** Pairing is how a second device joins an
account without copying a data directory or private keys.

1. The existing device creates a `toget-pair:v1:` code: base64url JSON carrying
   the pairing ID, a 256-bit secret, the node URL with its pinned TLS
   fingerprint, the server ID, account ID and name, device name, and expiry.
   The code is a bearer secret and is valid for ten minutes.
2. The new device generates a fresh Ed25519 signing key and X25519 encryption
   key, then posts a signed `pair.request` to `/toget/v1/pair/request`. The
   request binds the pairing ID, secret, account ID, device ID, device name,
   and both public keys. The approving node stores it in memory; requests
   from a second key pair against the same session are rejected.
3. The existing device displays the requesting device name and signing-key
   fingerprint. The user compares it with the fingerprint shown on the new
   device and approves through the local API.
4. Approval signs a one-year device certificate with the account key, adds the
   device row to the account, registers the device with locally hosted rooms,
   and refreshes certificates that are close to expiry.
5. The new device polls `/toget/v1/pair/complete`. Once approved, it receives
   the account public record (ID, code, name, public key, Argon2id password
   hash), the account Ed25519 private key sealed to its X25519 key, its own
   certificate, sibling device certificates, and page(s) of room snapshots.
6. The sealed box uses ephemeral X25519, HKDF-SHA256 with info
   `toget:sealbox:v1|<context>`, and XChaCha20-Poly1305. The context binds the
   pairing ID, device ID, and account ID, so a sealed key cannot be replayed
   into another pairing.
7. A room snapshot contains room metadata, members, the device registry, and
   every epoch key the approving device can unwrap, re-wrapped to the new
   device. It contains no events: the new device fetches those through the
   normal signed sync path, preserving hash-chain and signature verification.
   Bundles are paginated (`has_more`, `next_offset`) and capped by room count
   and byte size.
8. The new device imports the account atomically, installs room snapshots,
   and syncs each room from its home. If the new device is not yet in a room's
   device set, the home verifies its account-signed certificate, adds it, and
   wraps the room epoch keys to it on the first sync.
9. The new device keeps its own local server identity. The account ID, account
   code, account key, password hash, room roles, and room homes are preserved;
   rooms created on the old device remain hosted there.

The pairing code may be shown as a QR code in the terminal, a PNG, or the web
UI. It is still the same bearer secret, valid for the same ten minutes and
shown once; scanning only changes how the string reaches the new device.

Pairing sessions live only in memory and are lost on restart. Account import
is one transaction; room imports are idempotent, and transient page failures
are retried until the join deadline, so an account is never half-imported.
Room copy failures are warnings rather than a rollback. If the process dies
after the account import but before every room arrives, there is no resume
flow yet: the remaining rooms stay absent until a future pairing or sync path
covers them.

**Revocation.** Revoking another device marks it revoked on the local account
and, for every room this node knows about, marks the room device revoked and
rotates the epoch so it cannot read later messages. Remote homes are asked to
do the same through a signed endpoint. If a home is offline, the local
revocation still stands and the room revocation is reported as a warning.
Any device that already holds the account private key can still mint a new
certificate for itself; a compromised device therefore compromises the
account key until account-key rotation exists. Rooms a node has never joined
cannot be revoked from it, because it does not know they exist.

### Account record sync

**Implemented today for paired devices.** Each device keeps a public copy of
the account's device set and exchanges it with reachable siblings over the
same pinned HTTPS transport:

- Every 30 seconds, and immediately after pairing, account import, a
  revocation, or a merge that changed the set, a device sends its full record
  set to up to eight active siblings (rotating when an account has more).
- If no sibling address is known, the device resolves the account host (the
  server code is the first eight digits of the account ID) through a saved
  card, mDNS, or the directory, verifies that the result has the account's
  full origin server ID and account ID, and uses it as a bootstrap peer. The
  reply teaches both sides the current sibling addresses. The origin server ID
  is recorded when the account is created, carried in every pairing bundle,
  and never changes, so a device paired through several hops still knows which
  server is the account host.
- A record contains the device ID, name, signing and encryption public keys,
  account-signed certificate, last known node URL, role, and creation time.
  Private keys are never included. The local device's URL is replaced with its
  live address on export, so peers learn a new address after a restart or
  network change.
- The receiver verifies every certificate with the account public key and
  requires it to bind the record's ID and keys. It verifies signatures without
  checking certificate expiry: a device that has been offline for a year must
  still be able to sync a renewed certificate.
- Records add unknown devices and update a name, role, renewed certificate, or
  reachability hint. Keys on an existing device are immutable. A local device
  record is never overwritten by a peer, and a node URL is only a hint;
  identity still comes from the certificate.
- A revoked device carries an account-signed revocation statement
  (`toget:account-device-revocation:v1`) naming the account, device, and time.
  Revocation is monotonic: any device may relay the proof, the earliest signed
  time wins, and no later record can un-revoke it.
- A revoked device may still call a sibling. The responder returns its records
  read-only and does not merge anything from the revoked device, so the
  revoked device discovers its own revocation even if it was offline when the
  user revoked it.
- Revocations are applied to every room this device knows about and propagated
  to remote room homes, which mark the device revoked and rotate the epoch.
  Failures are warnings and are retried by later account-sync rounds.

Account sync is eventual, not a consensus protocol. A peer that has been
offline still shows the old state until it reconnects, and a device revoked
while offline can present a still-valid certificate to a peer that has not
learned the revocation. Room homes are the enforcement point for room keys,
and the signed revocation proof converges to every reachable device.
Account-key rotation is still required to recover from a compromised account
key.

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

Implemented today over HTTPS + JSON:

- A node registers with a relay using its server key and both signed manifests, then long-polls for opaque envelopes. The relay never connects to the node.
- A relay may register with a parent and announce routes upward. Announcements carry a `Via` path; the first hop must be the authenticated announcing relay, a direct registration always wins, and a path with a repeated relay or more than eight hops is rejected.
- Forwarding follows the announced route. The lowest-common-ancestor optimization is not computed; a relay with no local route sends the envelope upward.
- Each forwarded envelope appends the relay's ID to `Hops`. A relay that sees its own ID, or eight existing hops, returns `508`.
- The relay attached to an offline node can queue up to 128 requests / 16 MiB for 55 seconds and return a ticket. Results are retained for 10 minutes. This is at-most-once after the node accepts a frame.
- Relays enforce per-IP rates, per-node queue limits, in-flight request and byte limits, and body size limits, returning `queue_full`, `relay_busy`, `relay_full`, or `rate_limited` with `Retry-After`.
- The relay sees the target server ID, inner path, signed account/device headers, timing, and ciphertext. It cannot read room content.

See [relay.md](relay.md) for the exact wire format and numbers. Open: deterministic relay switching for existing rooms, multi-hop offline queueing, signed forwarding between relays, byte-based per-sender quotas, and the exact meaning of "no relay". Today `--no-relay` disables outbound fallback; ordinary nodes do not relay for others.

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
- An Agent is not invoked unless mentioned or addressed, unless a room policy says otherwise. A mention is either an explicit Agent ID in `mentions` or `@Name` in the message text, matched case-insensitively at a word boundary.
- Agent-authored messages (payload `agent_id` set) never trigger another run. Runs are serialized per Agent; at most one mention is claimed per scan.
- A run receives the trigger, the events since the last completed run, the room and Agent metadata, the resumed runtime session, and the per-Agent workspace. The runtime returns reply text, an optional format, and an optional runtime session ID.
- The scan cursor advances before the run, so a crash cannot repeat a side effect. The completed-run cursor advances only after a successful reply; a failed mention remains in the next catch-up context. A restart marks an interrupted run as interrupted instead of silently replaying it.
- Tool calls and results are reported as `tool.*` events, so the work is visible in the timeline.
- Risky actions wait for an approval; the Agent is suspended until the decision arrives.
- Session cursors are local state and are never sent to other members. `GET /api/v1/agents/{id}/sessions` and `DELETE /api/v1/agents/{id}/sessions/{roomID}` inspect and reset them. `POST /api/v1/agents/{id}/stop` cancels the active runtime process; the mention is not marked complete, so it stays in the next catch-up context.
- A room hosted by another node accepts only `message.posted` from remote members. Agent status, approval, and tool events therefore remain a home-node capability; remote-room Agent execution is not supported yet.

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
- Deterministic relay switching, multi-hop offline queueing, and byte-based per-sender quotas.
- "No relay" semantics for inbound traffic.
- Directory replication, signed directory responses, and collision migration.
- Home-node failover behavior.
- Whether new-member history uses re-encryption, old-epoch grants, or both.
- Push notification delivery for offline mobile clients.
- The first stable wire format for Agent streaming output.
