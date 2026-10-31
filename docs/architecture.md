# Architecture (Draft)

> Status: design draft. Nothing here is implemented yet. Names, boundaries, and wire formats will change as the first version is built.

## Goals

- Let people and Agents share rooms across machines, servers, and networks.
- Keep data, keys, and Agent execution on machines the user controls; when the official service stores anything, it stores ciphertext.
- Make the official service optional: a relay, a directory, and a blind store, never the owner of the data.
- Keep messages end-to-end encrypted; the relay and the official host see ciphertext and metadata, not content.
- Stay open: one protocol, implementable by anyone, with no lock-in.

## Non-goals

- Training or serving foundation models.
- Replacing Agent runtimes such as coding Agents, browser Agents, or workflow engines.
- Being a public social network or a mass-messaging platform.
- Hiding all metadata in v1. Who talks to whom, when, and how large a message is remains visible to relays and hosts.
- Multi-tenant shared nodes where one machine hosts multiple people's accounts.

## Design principles

1. **The endpoint is the trust boundary.** A self-hosted server is an endpoint; an official-hosted account's endpoint is the user's device. The official service is never an endpoint.
2. **Local-first.** The daemon, keys, history, and Agent execution live with the user. The network coordinates; it does not need to see everything.
3. **Deny by default.** An Agent has no authority until a policy grants it; risky actions require a human decision.
4. **Append-only history.** Events are immutable; corrections are new events.
5. **Least privilege.** Every device, Agent, room, and connector gets its own identity and scoped authority.
6. **Open source.** The daemon and the core protocol are Apache-2.0. There is no CLA and no plan to change the license.

## System overview

```text
        ┌──────────────────────────────┐
        │        Official service       │
        │  directory · relay · account  │
        │  host · update · identity     │
        └──────────────┬───────────────┘
                       │  encrypted envelopes / signed manifests
       ┌───────────────┼────────────────┬─────────────────┐
       │               │                │                 │
┌──────▼──────┐ ┌──────▼──────┐  ┌──────▼──────┐  ┌───────▼──────┐
│  Node A      │ │  Node B      │  │  Relay R    │  │  Node C      │
│  daemon      │ │  daemon      │  │  (any node) │  │  daemon      │
│  local data  │ │  local data  │  └─────────────┘  │  local data  │
│  rooms       │ │  rooms       │                   │  rooms       │
│  Agents      │ │  Agents      │                   │  Agents      │
└──────┬───────┘ └──────┬───────┘                   └──────┬───────┘
       │                │                                  │
┌──────▼───────┐ ┌──────▼───────┐                  ┌───────▼──────┐
│ Local web UI │ │ Local web UI │                  │ Phone/tablet │
│ 127.0.0.1:7718│ │ 127.0.0.1:7718│                  │ client App   │
└──────────────┘ └──────────────┘                  └──────────────┘
```

- **Nodes** are persistent devices running the daemon. They store local data, host rooms, run Agents, and can relay for others.
- **Client Apps** (phone/tablet) are devices too, but they are not callable, do not relay, and cannot be an account root.
- **Relays** forward encrypted envelopes. Any node with a reachable address can relay; the official service is the default.
- **The official service** provides a directory, a default relay, optional account hosting, the update service, and the root identity that signs manifests.

Any node can also be its owner's account host. The distinction is a role, not a separate product: the same binary can run a node, host an account, and relay encrypted traffic.

**Implemented today:** one `toget serve` process owns the local account, vault,
SQLite log, loopback API (`7718`), and a LAN HTTPS listener (`7717`) with a
pinned self-signed certificate. Two nodes on the same LAN can share a room
through a single-use invite code; the home node orders events and peers sync
incrementally. A second device can join the same account through a pairing
code: it generates fresh device keys, the existing device approves after
showing a fingerprint, and the account key arrives sealed to the new device.
Rooms are copied as membership and key snapshots, then synced from their
homes. A node can also register with an optional relay over a pinned,
outbound HTTPS long-poll and fall back to it when the direct LAN path is not
reachable. Relays can chain to a parent and announce signed routes, with loop
detection and quotas. An optional directory maps an eight-digit server code to
a self-certifying signed manifest and can advertise a default relay. Nodes also
advertise and browse `_toget._tcp.local.` over mDNS for directory-free
same-LAN discovery; a discovered endpoint is only used after its pinned TLS
certificate and signed identity and node manifests verify. Portable business
cards carry those same signed manifests as text or a QR code and can be saved
as an explicit full-server-ID pin. The account signing key can be rotated
without changing the account ID: active device certificates are re-signed, and
paired devices receive the new private key as per-device sealed envelopes.
The local vault is wrapped by a key derived from the account password. The
phone client and official account hosting shown above are still design-only;
NAT traversal is not implemented.

## Account and device model

### Account ID

A public account ID is 14 decimal digits, displayed as `48392017-174628`:

- **8 digits — server code.** `decimal(SHA-256(server_pubkey || server_nonce)) mod 100_000_000`, padded to 8 digits. The nonce is in the signed server manifest.
- **6 digits — account code.** Assigned by the account host; unique on that host.
- Capacity: 100 million server codes × 1 million accounts per host = 10^14 accounts. If it ever runs out, the protocol can extend to more digits; clients parse by length.

**Uniqueness.** The 6-digit account code is unique on its account host. The 8-digit server code is a truncated hash, so it is not mathematically unique: with many hosts, two hosts can land on the same code. When that happens, the hosts compare their full server IDs (the fingerprint of their root public keys); the lexicographically smaller one keeps the code, and the other increments its nonce, recomputes the code, and re-signs its manifest. The rule is verifiable by clients and needs no central arbiter. A host that already issued account IDs signs redirects from the old prefix to the new one for a transition period. Collisions are rare in practice, but the design does not pretend the digits are a global primary key.

The ID is a lookup key, not a secret and not a password. The cryptographic identity is the host's Ed25519 key and the account's own keys.

### Account host

The account host is the machine or service that stores the account's records, verifies login, assigns the account code, and keeps the device list. It is chosen at registration:

- **Official:** the Toget service. Convenient; the user does not need a public address.
- **Self-hosted:** the user's PC, NAS, or VPS. The user controls the machine; it must be reachable directly or through a relay.

The account host assigns the account code and is the canonical store for login and account records. It is not the only place where account records or room history live: paired devices keep their own copies, exchange account device sets over pinned HTTPS, and converge on certificate-signed additions and revocation proofs. Every device records the origin server ID at pairing, independently of its own local server ID, so it can still find the account host after several pairing hops.

### Devices

- **One device belongs to one account.** A device cannot be shared by multiple people.
- One account can have many devices.
- A device has its own key pair and a device certificate signed by the account key.
- Roles:
  - **Persistent node:** runs the daemon, is callable, stores room copies, can relay, can host Agents.
  - **Client App:** phone/tablet; can send and receive, but is not callable, does not relay, and cannot be an account root.
  - **Pure CLI:** uses the same `toget` binary but does not run a persistent service; useful on a server or in a script.

Enrollment uses device pairing: an existing device displays a short-lived code,
the new device submits fresh signing and encryption keys, and the existing
device signs a certificate after the user compares fingerprints. The account
private key is sealed to the new device's encryption key; no private key is
copied in plaintext, and the password hash and account code are preserved.
After pairing, devices periodically exchange the public account device set, so
a device added through one sibling appears on the others without another
pairing, and a signed revocation converges to every reachable device.

If the account key is exposed, any active device can rotate it. A rotation
statement is signed by both the previous and the new key and extends an
append-only chain of generations. Every active device certificate is re-signed
with the new key. The new private key is stored wrapped by the local vault and
distributed to siblings inside account sync, sealed to each device's X25519
key with the account ID, generation, and recipient device ID as context.
Revoked devices are not re-signed and never receive the new key. Room homes
advance a member's public key only after verifying a rotation chain anchored at
the key they already trust, so a room cannot be tricked into trusting an
unrelated key.

Switching accounts: log out, wipe local keys and caches, then join the new account as a new device. A device is never a member of two accounts.

### Keys

| Key | Owner | Purpose |
| --- | --- | --- |
| Account key (Ed25519) | The account, held on devices | Signs device certificates, migrations, and account-level statements |
| Device key (Ed25519 + X25519) | Each device | Signs device messages; receives HPKE-wrapped room keys |
| Room epoch key | Each room member endpoint | Encrypts room messages for one membership epoch |
| Account master key (MK) | User devices; official host stores only ciphertext | Unlocks account data and history on official-hosted accounts |
| Recovery key (RK) | The user's recovery credential file | Unwraps MK after a password reset |
| Server root key (Ed25519) | Account host / official service | Signs the server manifest and rotates operation keys |
| Release key | Official service | Signs version manifests; certified by the root key |

## Trust model: who is an endpoint?

An **endpoint** is a trust boundary that can decrypt messages and run Agents.

| Hosting | Endpoint | Official can read? | Notes |
| --- | --- | --- | --- |
| Self-hosted account | The user's own server | No | The server can decrypt and run Agents; the official service only relays/directs |
| Official-hosted account | The user's devices | No | Official stores ciphertext; the devices hold the keys |
| Official-hosted + official Agent | The official Agent, with explicit authorization | Not by default | The user delegates a specific room/data scope; the scope is visible and revocable |

- The official service is **never** an endpoint by default.
- The official relay sees the outer envelope: target, time, size, sender signature. It does not see the room ID or content.
- The official account host sees account metadata and ciphertext; it does not hold room epoch keys.
- The official room home node stores ciphertext, orders envelopes, and does not decrypt unless the user explicitly authorizes it to run an Agent.

## Components

### Account host

- Stores account records: ID, public keys, password verifier (PAKE/Argon2id), device list, room membership, and the encrypted master-key blob for official-hosted accounts.
- Assigns the 6-digit account code.
- Verifies login; issues device tokens.
- Can host rooms as a home node.
- For official hosting, stores ciphertext only; it cannot decrypt message content.
- For self-hosting, it is the user's endpoint and can decrypt; the local administrator can reset the password.

### Daemon / node

The daemon is the local runtime on a persistent device. It:

- holds the device key and the device certificate;
- stores the local copy of rooms the device participates in;
- connects outbound to peers, relays, and the account host;
- exposes the local API and local web UI on `127.0.0.1:7718`;
- supervises Agent processes and enforces local permissions;
- can relay encrypted envelopes for other nodes;
- participates in the relay tree (parent advertisement, path discovery);
- advertises its code and pinned endpoint on the local link with mDNS and can
  verify a discovered peer without a directory;
- performs signed updates and rollbacks.

### Local web UI

- Served by the daemon at `127.0.0.1:7718` or a Unix socket.
- Token-protected; not exposed to the network by default.
- Room list, conversation, Agent status, approval cards, device list, relay settings, and audit view.
- The same web build can later be wrapped by a desktop shell.

### Client App (phone/tablet)

- A device, but not a persistent node.
- Chat, approvals, notifications, and basic settings.
- Not callable, not a relay, not an account root.
- Uses the same protocol and identity model; it is not a second-class citizen in rooms.

### Relay

- Forwards encrypted envelopes when two nodes cannot connect directly.
- `toget-official` is the supported relay process today; ordinary `toget serve` nodes are not relays.
- Sees the outer envelope and inner request metadata: target server, account and device IDs, path, time, size, and ciphertext.
- Can store offline requests for a bounded TTL and capacity.
- Is a service, not an ownership relationship: a device owned by A can relay B's encrypted traffic.

### Official directory

- Maps a server code to a signed identity manifest and node manifest (`server_id`, endpoint, TLS pin, relay route, protocol version).
- Optional: clients can use saved [business cards](cards.md), QR codes, peer exchange, or a manual address.
- Cannot forge a server's manifest: both manifests are verified by the directory before storage and by the client on lookup. The client pins the endpoint from the signed node manifest, not from the directory's word.
- Code collisions are rejected with `code_taken`; deterministic collision resolution is not implemented. The 8-digit code is a routing hint and can be ground, so first-contact assurance should pin the full `server_id` from a business card or QR code.
- Implemented details and limits: [relay.md](relay.md) and [cards.md](cards.md).

### Official identity and update service

- Long-term root key signs the official manifest and certifies online release keys.
- Clients embed the root public key as a trust anchor.
- Release manifests list versions, protocol/schema versions, artifacts, hashes, and signatures.
- The update service returns the right artifact for a device's OS/arch and channel.

### Agent runtime

- An Agent runs on an endpoint: the user's self-hosted server, or the user's device for an official-hosted account.
- The local daemon owns a runtime loop. It scans local rooms for mentions, serializes runs per Agent, and posts the final answer back to the same thread.
- The first runtime is Claude Code, driven by the `toget` CLI and a skill. The daemon launches `claude -p`, keeps the Claude session ID per (Agent, Room), and resumes it on the next mention. A command runtime receives a JSON `RunRequest` on stdin for other tools.
- Sessions are local, not room events. Each one stores the scan cursor, the last completed run cursor, the runtime conversation ID, the run count, and the last error. The next mention catches up on every room event after the last completed run, including messages that arrived while the Agent was busy or offline.
- The CLI talks to the local daemon over the local API, using the Agent's own identity. Agent replies carry an `agent_id` display attribute in the signed message payload; the cryptographic author remains the owning account's device.
- Each Agent runs in its own workspace directory under the data home, with mode `0700`. A run can be stopped from the local UI or CLI; resetting a session stops an active run first and then starts fresh on the next mention.
- Agents have scoped permissions: file access, command execution, network access, and tool use.
- Risky actions become approval cards in the room; the Agent waits for a human decision. The runtime loop sees a pending approval and shows `waiting_approval` while it is blocked.
- Shared Agents remain on their owner's endpoint; other members call them through the room. The owner can see the room content the Agent can see, and the room shows a warning when a shared Agent is present.
- Agents can run in rooms hosted by another node. The owner's daemon posts `agent.joined`, status, tool, and approval events to the home node, which accepts them only after checking that the author account owns the Agent and that the Agent has not left. Approval decisions remain on the owner's node for now; other members see the audit trail in the timeline.

## Room model

A **room** is a private conversation with people, Agents, messages, files, and structured cards.

- **Home node:** the node that orders the room's events and stores a copy. By default it is the room creator's account host.
- **Event log:** an append-only log per room. Each event has an ID, author, type, payload, timestamp, and references to prior events. The home node assigns the room sequence.
- **Membership:** humans and Agents are members. Membership changes rotate the room's epoch key.
- **Cards:** a message can be text, a task card, an approval card, a tool-call receipt, or an Agent-status card.
- **Threads:** replies grouped under a message without splitting the main stream.
- **Approvals:** a card that blocks an action until a human decides. The approval is bound to the exact action digest.

### History storage

History follows the participants, not a central server:

- Every participant's machine stores a copy of the rooms it can decrypt.
- The room home node stores a copy and orders events.
- The official service stores a copy only when it is involved: an official-hosted account (ciphertext), an official Agent with explicit authorization, or an official room home node (ciphertext).
- A relay stores messages only temporarily for offline delivery; it is not a permanent history store.
- If two self-hosted machines talk and no official Agent is involved, the official service has no copy.
- Copies are per machine, not per identity: a person and their Agent on the same machine share one local copy.

### New members and history

- By default, a new member sees only messages sent after joining.
- The inviter can choose to attach history: all of it, or a recent window (for example, 7 or 30 days).
- If history is attached, the home node (or an authorized endpoint) re-encrypts the selected messages for the new member's key and delivers them.
- The room posts a system notice: "@inviter invited @new and attached the last 7 days."
- If the new member is a shared Agent, the notice also warns that the Agent's owner will be able to see the attached history.
- An inviter can only attach history they can see; inviting is not a way to read or forward messages they could not otherwise access.

## Message flow

A room message, end to end:

1. The sender's endpoint encrypts the inner message with the room epoch key. The inner message contains the room ID, epoch, sender, body, attachments, and mentions.
2. The sender wraps it in an outer envelope: target node, message ID, timestamp, size, and the sender's signature.
3. The sender sends the envelope to the room home node, directly if possible, otherwise through a relay.
4. The home node verifies the signature and ordering, assigns a room sequence, stores its copy, and forwards the envelope to the other member endpoints. How the home node learns the room without exposing the room ID to relays is still open (a routing token visible only to the home node, or a global order with client-side sequencing).
5. Each recipient endpoint verifies the sender signature, decrypts the inner message with the epoch key, and stores its copy.
6. Delivery is at-least-once; recipients deduplicate by message ID. The home node's sequence is the ordering key.

If a member is offline, the home node or a relay holds the envelope for a bounded TTL. When the member reconnects, it syncs from its last known sequence.

## Relay tree

A node chooses a **relay** to reach the rest of the network when direct connections fail. The default can come from the directory (`--node-relay auto`); users can point at a friend's relay, a community relay, or their own `toget-official` instance.

- **Direct first (implemented):** probe the pinned LAN endpoint with a 2.5-second health check; use it when reachable. IPv6 preference, UPnP/NAT-PMP/PCP, and QUIC hole punching are not implemented.
- **Relay fallback (implemented):** register over outbound pinned HTTPS, long-poll for opaque signed requests, and return results. No inbound port is needed.
- **Chaining (implemented):** a relay can register with a parent and announce routes upward. Forwarding follows the announced path; the lowest-common-ancestor optimization is not computed.
- **Loop detection (implemented):** each hop appends its relay ID; an envelope is rejected on a repeated ID or after eight hops.
- **No ownership:** a relay is a service. A device owned by A can relay B's encrypted traffic.
- **Bounded (implemented defaults):** 128 queued requests / 16 MiB, 512 in-flight / 128 MiB, 52 MiB inner body, 48 MiB relayed response, per-IP rate limits. Large files still prefer direct connections.
- **Offline queue (implemented):** the relay attached to a node can hold requests for 55 seconds (the client envelope expiry), return a ticket, and keep the final response for 10 minutes. It is not a permanent history store. Multi-hop offline queueing is not implemented.

Open details: deterministic relay switching for existing rooms, multi-hop offline queueing, byte-based per-sender quotas, and the exact meaning of "no relay" for inbound traffic are still being decided. Today `--no-relay` disables outbound fallback only; ordinary nodes do not accept relay traffic for others.

## Identity and cryptography

- **Signatures:** Ed25519.
- **Key exchange:** X25519.
- **KDF:** HKDF-SHA256.
- **AEAD:** XChaCha20-Poly1305 (preferred) or AES-256-GCM.
- **HPKE:** RFC 9180 with X25519 + HKDF-SHA256 + ChaCha20-Poly1305, using a mature library.
- **Password verification:** PAKE for login, Argon2id for the stored verifier and key derivation.
- **Transport:** TLS 1.3 for all connections; server identity is verified with the signed manifest, not only the certificate.
- **No custom cryptography.** Use standard combinations and mature libraries.

## Sync and offline behavior

- Login to the account host returns the account record, device list, room list, and a sync cursor.
- Each device syncs room events incrementally from its last sequence.
- The daemon keeps an outbox for messages and events created while offline; it replays them after reconnecting.
- Event appends are idempotent by event ID.
- A device that has been offline for a long time can request a snapshot plus a delta, rather than replaying every event.
- A room home node that is offline makes the room read-only for new ordered events until it returns or a new home is chosen. The failover policy is still being designed.

## Versioning and upgrades

- **App version:** SemVer, visible to users.
- **Protocol version:** integer, negotiated in the handshake. Breaking changes require a new major version and an overlap window.
- **Data schema version:** integer, with forward migrations and backups.
- **Capability flags:** optional features are advertised, not assumed.
- **Release channels:** stable and beta.
- **Support window:** the current and previous two minor versions (N-2) remain able to log in and exchange messages.
- **Updates:** signed manifests, atomic switch to a new version directory, delayed restart, health check, automatic rollback on failure.
- **Forced updates:** only for protocol-incompatible or security-critical releases, and only for devices that can reach the official service. LAN-only and offline deployments are not forced offline.

## Deployment modes

1. **Self-hosted account, LAN or public.** The user's machine is the account host and endpoint. Rooms can stay on the LAN; the official service is optional.
2. **Self-hosted account behind NAT.** The machine keeps an outbound connection to a relay; other devices reach it through the relay.
3. **Official-hosted account.** The official service stores ciphertext and account metadata; the user's devices are endpoints.
4. **Mixed room.** A self-hosted account and an official-hosted account share a room; the room home node is the creator's account host, and the official service stores only what its hosted participants require.
5. **Relay-only node.** A public node that forwards encrypted envelopes but hosts no accounts.

## Open questions

- Deterministic relay switching and address refresh for existing rooms.
- Multi-hop offline queueing and byte-based per-sender quotas.
- Room home-node failover: read-only, queue, or elect a new home.
- Directory replication, signed directory responses, and collision resolution.
- The smallest useful policy language for Agent permissions.
- How much of the official service can be stateless.
- The first mobile client: responsive web, PWA, or native shell.
