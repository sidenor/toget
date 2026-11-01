# Security Model (Draft)

> Status: this document describes the intended trust boundaries and the
> controls implemented in the current tree. It is not a claim that the first
> release is secure for high-stakes use. The official room home exists in the
> code and has an end-to-end test, but it has not had an independent security
> audit; everything else marked implemented here exists in the code.

Toget's security model follows one rule: **the endpoint is the trust boundary**. An endpoint is a machine that holds keys and can decrypt room content. The official service is never an endpoint.

## Assets

- Account identity, account key, and device keys.
- Room epoch keys and message content.
- Agent credentials, API keys, and local file access.
- Approval decisions and the audit trail.
- The per-device vault master key (MK); on official-hosted accounts, the account master key.
- Recovery credentials, both per-device files and the official-account recovery file.
- Server root keys and release signing keys.
- Metadata: who talks to whom, when, and how often.

## Who can see what

| Party | Message content | Room keys | Metadata | Notes |
| --- | --- | --- | --- | --- |
| User's own endpoint (self-hosted server or own device) | Yes | Yes | Yes | The trust boundary |
| Other room members | Only what is shared in the room | Their epoch keys | Room events | Leaving rotates the epoch |
| Official relay | No | No | Target server, account/device IDs, path, time, size | Sees the relay envelope and signed inner headers |
| Official account host (official-hosted account) | No | No | Account record, device list, room membership, ciphertext sizes | Blind store |
| Official room home | No | No | Room ID, account/device IDs, membership and roles, event type/epoch/sequence/time/size, envelope hashes/signatures, wrapped epoch-key packages, invite policy and timing | Blind ordering and storage. Never receives room name, topic, message text, attachments, or epoch keys. It can withhold or delay events, but cannot forge a member's signature or envelope chain. No attachments yet. |
| Official Agent, explicitly authorized | Scoped only | Scoped only | Scoped only | Visible and revocable authorization |
| Self-hosted account host | Yes | Yes | Yes | It is the user's endpoint |
| Relay run by a third party | No | No | Same as official relay | Same envelope rules |
| Official directory | No | No | Server/account IDs, advertised endpoint, relay route, publish/lookup timing | Verifies manifests; cannot forge or redirect a pinned endpoint |
| TLS-terminating proxy in front of the account host (Cloudflare, tunnel, reverse proxy) | No | No | Client IP, hostname, request paths and timing, account IDs/email in request bodies, OPAQUE messages and sealed blobs | Terminates TLS, so it sees the same HTTP metadata and ciphertext as the host; it cannot forge host identity when the client verifies the signed host manifest. It never sees the password, MK, RK, room keys, or plaintext content |
| Browser push service | No | No | Subscription endpoint, delivery time, encrypted payload size | Payload is content-free and encrypted to the browser; it cannot read the event type |

The official service has no universal key and no backdoor. There is no "compliance access" path to plaintext.

Web Push notifications are content-free by construction. The node sends only
`{"v":1,"type":"approval|message","lang":"en|zh"}` encrypted for one browser
subscription; message text, senders, room names, and approval descriptions
never enter a push payload. The VAPID private key remains in the local
database. See [notifications.md](notifications.md).

## Trust boundaries

```text
 Self-hosted                    Official-hosted
 ┌──────────────────┐           ┌─────────────────────────────┐
 │  User's server   │           │  Official service            │
 │  endpoint        │           │  relay · account host · home │
 │  keys · history  │           │  ciphertext + metadata only  │
 │  Agents          │           └──────────────┬──────────────┘
 └────────┬─────────┘                          │ ciphertext
          │ E2E ciphertext                     │
          └───────────────► network ◄──────────┘
                              │
                    ┌─────────▼──────────┐
                    │  User's devices     │
                    │  endpoint           │
                    │  keys · history     │
                    └────────────────────┘
```

- **Self-hosted:** the user's server is the endpoint. It can decrypt, run Agents, and read history. The official service can only relay and direct.
- **Official-hosted:** the user's devices are the endpoint. The official service stores ciphertext and account metadata; it cannot decrypt.
- **Official-hosted with an official Agent:** the user explicitly authorizes a scope (a room, a data set, a time window). Only that scope is readable, the authorization is visible in the room, and it can be revoked.
- **Mixed rooms:** each participant's hosting choice determines who can read. A self-hosted participant's server sees its own copy; an official-hosted participant's devices see theirs; the official service sees ciphertext for the parts it hosts.

### TLS-terminating proxies and identity pinning

A direct account host address carries an exact certificate pin:
`https://host:7722#fp=<SHA-256 of the origin certificate>`. The client sets
`InsecureSkipVerify`, then compares the SHA-256 of the first presented
certificate with the pin. This binds the connection to one certificate and is
the strongest transport check, but it fails as soon as anything terminates TLS
before the origin. A Cloudflare-proxied host, Cloudflare Tunnel, load balancer,
or ingress controller presents a different certificate to the client.

For those deployments the host prints a second address with a self-certifying
identity anchor:

```text
https://accounts.example.com#id=tog1:s:<base32(SHA-256(host root public key))>
```

The client:

1. Connects with standard TLS 1.3 certificate validation against the system
   (or caller-supplied) root store. The edge certificate must be valid for the
   hostname.
2. Rejects redirects, so the checked origin cannot be replaced by a redirect.
3. Fetches `/host/v1/info` and verifies the signed host manifest with the
   embedded root public key: signature, issue/expiry time, `server_id`, and
   `server_code`.
4. Compares the manifest `server_id` with the expected `#id`. Every account
   request, including registration, login, recovery, and device-signed calls,
   runs this identity check before sending account material; the first check
   fetches `/host/v1/info`, and later calls reuse the verified result for that
   client.
5. Rejects a recovery credential whose host `#id` differs from the live host.

A malicious proxy can therefore deny service, delay traffic, or present a
different valid website, but it cannot make the client accept a different
Toget host identity or substitute its OPAQUE configuration. It cannot read the
password (OPAQUE never sends it), the account master key, recovery key, room
keys, or room content. It does see client IPs, hostnames, paths, request
timing, account IDs and email addresses in request bodies, OPAQUE messages,
and sealed blobs, because it terminates TLS before forwarding the HTTP
request. This is a metadata and availability exposure, not a content
compromise.

The identity anchor is weaker than an exact certificate pin in one respect:
it depends on the public Web PKI being honest for the proxy hostname. That is
the same trust the user already places in a browser for that domain. Use
`#fp=` whenever clients can reach the origin directly, including on a LAN or
through a TLS-passthrough proxy.

A TLS-passthrough proxy does not terminate TLS, so `#fp=` still works and the
proxy cannot inject client-IP headers inside the encrypted stream. Without
PROXY protocol support (not implemented), the host sees the proxy's address
for rate limiting.

Forwarded client-IP headers are trusted only when the direct peer is in
`--host-trusted-proxies`; `CF-Connecting-IP` additionally requires `CF-Ray`.
Untrusted peers cannot choose their rate-limit bucket. Cloudflare's published
ranges are a snapshot and must be refreshed, or the operator can pass current
ranges explicitly. See [deployment.md](deployment.md#put-the-official-host-behind-cloudflare-or-a-tls-terminating-proxy).

### Implemented LAN boundary

Two Toget nodes on the same LAN currently talk over HTTPS on `7717`:

- The node generates a persistent Ed25519 self-signed certificate. The
  invite code pins its SHA-256 fingerprint, so an active attacker without the
  node's private key cannot impersonate the host.
- Every request is signed by the sender's device key over the method, URI,
  timestamp, nonce, and body hash. The verifier rejects timestamps outside a
  five-minute window and replayed nonces within ten minutes.
- Invite redemption verifies the joining device certificate with the account
  public key before creating membership. Room-key bundles are sealed to the
  joining device's encryption key.
- Room payloads remain sealed with the room epoch key; the node transport
  does not unwrap them.
- A remote room is read-only locally except for sending messages, uploading
  attachments, leaving, local archival, and events about Agents the local
  account owns. The home node remains the only log writer, preventing a local
  mutation from forking the append-only hash chain.
- The home node accepts an Agent event from a remote member only when an
  earlier `agent.joined` event bound that Agent ID to the same account, and the
  Agent has not left. A member cannot post status, tool receipts, approvals, or
  Agent-attributed messages for another account's Agent.
- Agent name, runtime, owner, and status travel in the signed room log, so any
  member who can decrypt that part of the history can see which shared Agents
  are present. This is deliberate: Agents are room participants. No separate
  directory entry or cross-node lookup is involved.
- Revoking an Agent appends `agent.left` with reason `revoked` to every room
  before the Agent is marked revoked. A home node that cannot be reached
  blocks the revocation instead of leaving an active projection elsewhere.
- Device pairing is explicit and visible. The new device proves possession of
  the keys it submits; the existing device displays a fingerprint and requires
  local approval. The account private key is sealed to the new device with
  X25519 + HKDF-SHA256 + XChaCha20-Poly1305; the pairing code is a bearer
  secret and is valid for ten minutes.
- Revocation marks the account device and every known room device revoked,
  then rotates the room epoch. New messages no longer decrypt to the revoked
  device. Offline room homes are retried and reported as warnings.
- Paired devices exchange their public account device sets over pinned HTTPS.
  Every record is bound to an account-signed certificate; private keys never
  leave a device. Revocations travel as account-signed statements, are
  permanent once seen, and can be relayed by any device. A revoked device is
  answered read-only so it learns that it was revoked, but its records are
  never merged.
- The account signing key can be rotated. A rotation statement is signed by
  both the old and new key and extends a verified chain; active device
  certificates are re-signed. The new private key is distributed only to
  active siblings as a per-device sealed envelope during account sync, and
  revoked devices never receive it. Room homes advance a member key only from
  a chain anchored at the key they already trust.

Residual risks: the host sees IP addresses, account and device IDs in request
headers, room IDs, and timing. The invite code is a bearer secret shown once;
anyone who obtains it before redemption can join. The pairing code is also a
bearer secret: anyone who obtains it can request to add a device, but the
request must still be approved on an existing device and its fingerprint
compared. Showing either code as a QR does not change that exposure: the QR is
the same secret in another form, so it is shown once in the web UI with no
download action, and CLI PNG files are forced to mode `0600`. A device that holds the account private key can mint certificates
for new devices. Rotating the account key after a compromise stops the old
key from authorizing new devices once peers learn the new generation, but an
attacker who already copied the account key can still use it until every
device and room home has advanced. Revocation only reaches rooms the revoking
node knows about, and account-device sync is eventual: a device that stays
offline keeps its old view until it reconnects, and a revoked device can
appear active to a peer that has not yet received the signed revocation. Room
homes enforce the revocation for room keys as soon as they receive it. The certificate is pinned
but not publicly audited. Remote administration beyond attachments,
self-leave, and local archival is unsupported rather than securely
implemented.

### Implemented LAN discovery boundary

`toget serve` advertises `_toget._tcp.local.` over multicast DNS by default.
The packet carries the full server ID, eight-digit server code, HTTPS port,
interface addresses, protocol version, and certificate fingerprint. It does
not carry the account ID, room IDs, room names, messages, or keys. Disable it
with `--no-mdns` or `"node_mdns": "off"`.

- mDNS is unauthenticated. Any device on the link can announce any ID, code,
  address, or fingerprint, and can observe every announcement.
- A lookup treats advertisements as candidates. It connects to the advertised
  address using the advertised certificate pin, then verifies the signed
  identity manifest and the node manifest. Both must bind the advertised
  server ID and code, and the manifest must carry the same certificate
  fingerprint. A forged advertisement without the server key fails.
- The discovery list (`toget node discover`, the Settings page, and
  `GET /api/v1/node/discover`) is not verified. It is self-reported metadata
  and is labelled as such.
- The eight-digit code is still a routing hint, not authentication. An
  attacker can grind a valid identity with the same code and announce it.
  Compare the full server ID, use an invite code, or pin the identity out of
  band when first-contact impersonation matters.
- mDNS is link-local and does not traverse routers or NAT. It is not a
  substitute for the relay and provides no cross-network discovery.

Residual risk: a passive observer on the local link can inventory Toget
nodes, their codes, endpoints, certificate fingerprints, and announce times.
An active attacker can spoof advertisements, suppress multicast, or direct a
lookup attempt at a host they control; that reveals the querying device's IP
address and the requested code, but the pinned TLS and manifest checks stop a
successful impersonation. A host can disable advertisement and browsing when
the local link is untrusted.

### Implemented business card boundary

A portable card carries an account's signed one-year identity manifest and a
signed 24-hour node manifest as text or a QR code. It is public metadata, not
a secret and not a credential:

- The card contains the server ID, account ID, eight-digit code, HTTPS
  endpoint, TLS certificate fingerprint, optional relay route, and an
  unsigned local label. It contains no room ID, room key, message, device key,
  or account private key.
- Import verifies the identity signature and freshness, the node manifest
  signature (expiry optional so a card can be saved later), the binding
  between both manifests, the account-ID prefix, the HTTPS endpoint, the
  certificate pin, and the relay route target.
- The embedded endpoint is a hint. Before use, lookup opens a fresh pinned TLS
  connection and re-verifies `/toget/v1/identity` and `/toget/v1/info`, so an
  expired or tampered address cannot substitute a different key.
- Saving a card pins the full server ID for its eight-digit code. A different
  server ID from mDNS or the directory is rejected, and conflicting saved
  cards fail closed. This is the strongest first-contact check in the
  implemented product: the short code alone is grindable.

Residual risk: a card is forwardable, cacheable, and photographable. Anyone
who obtains it learns the account ID and advertised network endpoint, and can
try to reach the node. The label is not signed and can be changed by a
forwarder. A card for an identity that later changes endpoints goes stale; it
does not by itself revoke or rotate an identity. Cards are stored locally at
mode `0600`, omitted from encrypted backups, and can be removed, but removal
does not reach copies held by other people.

### Implemented relay and directory boundary

A node behind NAT can register with a relay over outbound pinned HTTPS. The
relay never opens a connection to the node; the node holds a WebSocket tunnel
or long-polls for work and returns its signed results.

- The relay receives an envelope containing the target server ID, the inner
  HTTP method and path, allowlisted signed headers (account ID, device ID,
  timestamp, nonce, signature), and a base64 ciphertext body. It does not
  receive room epoch keys or plaintext.
- The node verifies the original device signature and nonce before executing a
  relayed request, using the same handler as a direct LAN connection. A relay
  cannot forge a request without a device key.
- The relay cannot substitute its own TLS certificate for the node: clients
  pin the node certificate for the direct path and still verify the signed
  node manifest for relayed operations.
- Direct connections are probed first. A relay is used only when the pinned
  direct endpoint does not answer within 2.5 seconds.
- The relay attached to a node can queue a bounded request for up to 55
  seconds and keep the result for 10 minutes. This is at-most-once delivery
  after the node accepts a frame; a client retry can duplicate a
  non-idempotent side effect. Queued tickets are bearer secrets carrying only
  the response the client already had permission to receive.
- Multi-hop routes carry relay IDs in a hop list. A repeated ID or more than
  eight hops is rejected. A child relay signs each relay-to-relay envelope; the
  parent verifies the self-certifying relay ID, the route position, and a
  replay nonce. The relay operator sees the whole path for traffic it forwards.
- Rate limits, queue capacity, in-flight byte accounting, body limits, and
  fixed-window per-sender byte quotas bound resource use. They do not make the
  relay trusted for availability.
- The WebSocket tunnel and HTTPS long poll carry the same signed envelopes and
  the same trust rules. Enabling or switching a relay at runtime updates the
  stored route for new invitations and directory records; it does not grant
  the relay any additional key material.

The directory verifies and stores the account identity manifest and the
signed node manifest. On lookup, the client re-verifies both, checks the
eight-digit code against the identity, checks the node manifest signature and
expiry, requires an HTTPS endpoint, and uses only the relay route signed
inside the node manifest. A compromised directory therefore cannot substitute
an endpoint or a relay route for a server lookup; it can deny lookups, serve a
valid stale record until expiry, or choose which valid relay an `auto` node
uses. The eight-digit code is a routing hint with about 27 bits, not an
authentication factor.

Residual risk: the relay and directory see who connects to whom, when, how
often, and how large requests are. Publishing to the directory exposes the
advertised node endpoint (often a LAN address), server/account IDs, and relay
route. A relay can drop, delay, or replay envelopes, and an offline queue
ticket can be read by anyone who obtains it. None of this reveals room content
or epoch keys.

## Adversaries and threats

### Malicious or compromised relay

A relay forwards encrypted envelopes and sees the outer header.

Controls:

- The inner message is encrypted end to end; the relay never receives epoch keys.
- AAD binds the ciphertext to `room_id`, `epoch`, and `message_id`, so a relay cannot move, replay, or relabel messages.
- The inner request is signed by the sender device; the node verifies it before executing anything the relay forwards.
- Relays are replaceable in configuration: users can choose a different relay or self-host `toget-official`, though existing rooms do not yet switch addresses automatically.
- Rate limits, quotas, and size limits bound abuse.

Residual risk: traffic analysis. A relay can see who sends to whom, when, and how large messages are. v1 does not hide metadata.

### Compromised official account host

An attacker who compromises the official account host gets account records, device lists, room membership, and ciphertext.

Controls:

- Message content and room epoch keys are not stored there.
- The host stores the OPAQUE registration record, `Enc(export key, MK)`,
  `Enc(RK, MK)`, and `Enc(MK, account key bundle)`. It never receives the
  password, MK, RK, or account private key in plaintext.
- Login uses OPAQUE, so the password is not sent to the host. A host
  compromise exposes a registration record that permits an offline dictionary
  attack; OPAQUE's client-side key stretching and a strong password slow it
  down.
- A host session requires an account-signed device certificate and a
  device-signed request; the host cannot mint a session for a device the
  account did not authorize.
- The host cannot forge account signatures or decrypt the sealed blobs it
  stores.
- Users can review and revoke devices from a signed-in device; revoked devices
  lose host sessions.

Residual risk: an attacker with the host database and host key material can
attempt offline password guessing against the OPAQUE record, and can see
account, device, session, and email metadata. The recovery key is
high-entropy and not guessable. Rotating the account key publishes a new
generation and ciphertext; old ciphertext is not retroactively protected.

### Compromised official service (insider or operator)

The official service operates the directory, the default relay, the account host, and the update service.

Controls:

- No universal key; operators cannot decrypt room content.
- Release manifests are signed by a root key that is kept offline or in an HSM; online release keys are certified by it.
- Clients embed the root public key and refuse unsigned manifests.
- The official service can only send protocol requests. It has no remote shell, no arbitrary file read, and no privileged channel into a node.
- The daemon enforces local policy; an official request cannot bypass an approval.
- Audit logs are local and exportable.

### Compromised self-hosted server

If a user's self-hosted server is compromised, its rooms and Agents are exposed.

Controls:

- Full-disk encryption and secure boot are recommended, and documented plainly.
- The account host runs with the minimum privileges it needs.
- Device and Agent credentials are scoped and revocable.
- Backups are encrypted; a lost machine without a backup means lost data, and we say so instead of pretending otherwise.

### Stolen or lost device

Controls:

- Each device has its own key and certificate.
- A device can be revoked from another device or from the account host.
- The signed revocation spreads to reachable sibling devices, so they stop listing the stolen device as active.
- Revocation rotates room epoch keys, so the stolen device cannot read new messages.
- Local caches are encrypted at rest where the OS supports it.
- Switching accounts wipes local keys and caches.

Residual risk: messages already stored on the stolen device may be readable if the device is unlocked or its disk is not encrypted.

### Malicious room member

Controls:

- Rooms are private and invitation-only; there is no public discovery or stranger DM.
- Membership changes rotate the epoch key; a removed member cannot read new messages.
- History is not attached by default; the inviter chooses, and the choice is announced in the room.
- An inviter can only attach history they can see.
- A shared Agent's owner can see what the Agent can see; joining a shared Agent produces a visible warning.

### Malicious or compromised Agent

Controls:

- Each Agent has its own identity and scoped permissions.
- Agents cannot read each other's credentials.
- The daemon applies resource limits and a restricted environment.
- Risky actions require approval and are rate-limited.
- An active run can be stopped from the local UI or CLI, and the Agent can be
  revoked. Room messages cannot stop or control another member's Agent.

### Agent runtime loop

Controls:

- A run starts only from a mention. Agent-authored messages never trigger another run, and runs are serialized per Agent.
- The runtime command is chosen by local configuration, never by room content.
- A connector is trusted local code. It runs with the owner's privileges and is
  not sandboxed by Toget beyond a separate process, a per-Agent workdir, a
  timeout, and process-group termination. A connector that reads untrusted room
  content must enforce its own permission scopes and must not turn message text
  into shell commands.
- The Claude Code prompt labels room messages, files, web pages, and tool results as untrusted and tells the Agent not to follow instructions from them to reveal secrets or exceed its permissions.
- Each Agent gets its own workspace directory with mode `0700`. Commands run without a shell, with a bounded timeout, bounded output, and a killed process group.
- `agent_id` on a message is display metadata. It is not a signature by the Agent, and other members must not treat it as proof of the Agent's consent.
- Session cursors, runtime session IDs, and last errors stay on the endpoint. They are not replicated to the room.
- Live partial output is held only in memory on the Agent owner's node, bounded to 64 KiB per run, and served only to an authenticated member of the room. It is not written to the event log, not replicated, and lost on daemon restart. The final reply event, not partial text, is the audited record.
- A stop cancels the runtime's process group. A finishing run can update a
  session only while its trigger is still active, so a reset or a newer run
  cannot be overwritten by a stale goroutine.
- A room hosted by another node accepts messages and Agent events from the Agent owner. Approval decisions are currently produced by the owner's node; other members see the approval cards only in the timeline and cannot decide them from their own node. The home node enforces Agent ownership for every accepted event.

### Prompt injection

An Agent reads a web page, email, file, or message that tells it to exfiltrate data or spend money.

Controls:

- Tool results and external content are labeled as untrusted in the Agent context.
- Actions that cross a trust boundary require approval: `external.send`, `money.spend`, `destructive`.
- Approval cards show the concrete target, parameters, and amount from the canonical action, not a model-written summary.
- Policies restrict which domains, recipients, tools, amounts, weekdays, and time windows an Agent may use. Deny wins; an explicit allow is a single-use pre-authorization; unmatched risky actions still need a human decision (see [policies.md](policies.md)).
- The timeline records where an instruction came from when the source is known.

### Approval phishing

An Agent asks for approval of a harmless action, then does something else.

Controls:

- Approvals are bound to an `action_digest` over the canonical action.
- The daemon recomputes the digest before execution and refuses mismatches.
- Approvals display the exact command, recipient, amount, and target.
- Approvals expire and are single-use.

### Audit tampering

Controls:

- Events are append-only; corrections are new events.
- Each room's events form a hash chain; exports include the chain and a signed manifest.
- Every participant keeps a copy, so a single host cannot silently rewrite history for everyone.
- A mismatch between copies is detectable and surfaced.

### Local vault and forgotten-password reset

Each device has its own vault master key. The key file is mode `0600`, wrapped with an Argon2id-derived KEK from the account password. An optional recovery key wraps the same master key, so sealed history survives a forgotten-password reset while the node is locked.

Controls:

- A recovery file is a bearer secret bound to the account ID, device ID, server ID, and server fingerprint, with a SHA-256 checksum. The recovery wrap uses AAD over the account and device IDs.
- Creating a new recovery file replaces the previous wrap; the old file stops working. Removing it requires an unlocked vault.
- A locked reset validates the file before changing anything, updates the password verifier, and re-wraps the vault key. If the rewrap fails, the previous verifier is restored.
- A local-administrator reset is available only while the node is unlocked, because the daemon already holds MK. It re-wraps MK without the old password. While locked, the route returns `423`.
- Password mutations are serialized, and password endpoints are rate-limited.
- With no recovery file and the node locked, there is no bypass: OS root can delete the installation but cannot decrypt sealed values without the password or recovery key.
- A reset or password change on one device does not change another device's local vault or verifier.
- Treat recovery files like private keys: offline storage, not email, not a chat app, not a cloud drive without encryption.

Residual risk: access to the unlocked process, the OS user, or the local API token is equivalent to access to sealed local data. Lock the node when leaving and use full-disk encryption on a shared machine.

### Recovery credential compromise (official-hosted)

For official-hosted accounts, the official recovery credential is a bearer
secret: whoever holds it together with a valid email reset link and the account
ID can reset the host password and unwrap the account master key.

Controls:

- It is generated client-side and returned once at registration. The web
  onboarding flow requires the download to complete and be confirmed before
  continuing; the CLI writes it with mode `0600`.
- The filename includes the 14-digit account ID. The file contains the host
  URL (including the `#fp=` or `#id=` anchor when one was supplied), a creation
  time, the 32-byte recovery key, and a SHA-256 checksum over those fields.
- The reset page is embedded in `toget-official`, has no third-party scripts,
  and runs the same Go hosted-client code as a desktop device through Go/WASM.
  It reads the file locally through the File API; the file is never uploaded.
  The page unwraps MK, performs a fresh OPAQUE registration for the new
  password, and sends only the new password wrap, recovery wrap, and OPAQUE
  record.
- The host replaces the old password material atomically and marks the reset
  token used. The page then shows a new recovery file; the old one stops
  working immediately.
- A signed-in device can rotate the file without email; the old file becomes
  void.
- The reset page requires `--host-public-url` to be reachable from the user's
  browser. A loopback-only host cannot serve email links off-machine.

Residual risk: email plus the recovery file is enough to take over the
account. Treat the file like a private key and keep it offline. Losing both the
file and every signed-in device makes the account's encrypted data
unrecoverable.

### Account takeover via email

Email is only a login-recovery channel, not a decryption key.

Controls:

- An email reset restores login but cannot open old history without the recovery credential or a logged-in device.
- The reset page states this plainly before the user proceeds.
- New device enrollment is visible to existing devices; users can revoke it.

### Supply chain and malicious updates

Implemented controls:

- Signed release manifests: an offline root key certifies release keys; release keys sign channels, version manifests, and the release index; manifests list artifact SHA-256 hashes.
- Clients embed the root public key and reject anything that does not chain to it. HTTPS is not trusted as the source of truth.
- Each signed document has an optional detached `.sig` sidecar bound to the document path and SHA-256, so a signature cannot be moved to another document.
- Atomic updates: download, verify signature and hash, extract into a staged directory with path/symlink/device/duplicate checks and size limits, reject incompatible app/protocol/schema versions, switch symlinks, optionally restart, run a health check, and roll back on failure.
- The previous version is kept for rollback. Pending state is reconciled at the next daemon start if the process dies mid-update.
- Periodic checks and the in-app banner default to notify-only. Automatic staging never restarts the daemon; an explicit restart is required. Forced releases can be postponed for at most 24 hours.
- The update service is static and only exposes `channels/`, `manifests/`, `daemon/`, and `sbom/`; it has no directory listing or dotfile access. Mutable channel/index objects get a short cache lifetime; artifacts are immutable.
- `release verify-tree` rejects symlinks, unreferenced files, extra manifests, tampered artifacts or SBOMs, and channels pointing at yanked releases before an operator publishes a tree.
- `release build` produces reproducible archives with fixed source date, trimpath, no CGO, and stable archive metadata, and generates a deterministic SPDX 2.3 SBOM. `--verify` builds twice and compares hashes.
- Release private keys are mode `0600` and never served. The root key is intended to stay offline.
- Dependencies are pinned, scanned, and reviewed; the daemon and protocol are open source and auditable.

Not yet provided:

- A transparency log, threshold signing, or enforced HSM use.
- Compromise detection or an automatic rollout pause; the update service is static and does not collect telemetry.
- Server-side enforcement of a forced-update policy for official-hosted accounts. The browser UI honors the forced level, but a user who controls the binary can postpone or bypass it.

See [updates.md](updates.md) for the operational workflow.

### Directory poisoning

A malicious or compromised directory returns the wrong manifest for a server code.

Controls:

- Manifests are self-certifying: the client checks `server_code == hash(root_pubkey || nonce)`.
- A manifest that does not match is rejected, even if it is signed by the directory. The client checks the signed node manifest and pinned endpoint itself.
- Only the relay route inside the signed node manifest is used; an unsigned directory hint cannot redirect traffic.
- A saved business card is tried before the directory and pins the full `server_id`; a directory answer with a different ID for that code is rejected.
- The 8-digit code is only about 27 bits. An attacker can grind a key pair until the short code matches a target. The code is a routing hint, not an authentication factor; clients compare the full `server_id` with a pinned or QR-provided value when the threat model requires it, and always verify the manifest signature.

### Local attackers and shared machines

Controls:

- The local API binds to `127.0.0.1` or a Unix socket and requires a token.
- The UI checks `Origin` and uses `SameSite=Strict` cookies.
- Account and device private keys are encrypted at rest by the local vault.
  Its random master key is wrapped with an Argon2id-derived key from the
  account password, and the wrapped key file is mode `0600`. A fresh paired
  device asks for the account password and initializes that wrapping before it
  stores the imported key.
- Logs redact known secret patterns.

## Cryptographic choices

- Ed25519 for signatures; X25519 for key agreement.
- HPKE (RFC 9180) with X25519 + HKDF-SHA256 + ChaCha20-Poly1305 for wrapping room keys.
- XChaCha20-Poly1305 or AES-256-GCM for message content.
- Argon2id for password verifiers and vault-key derivation; PAKE for login.
- TLS 1.3 for transport.
- No custom cryptography. Use standard constructions from mature libraries.

## MVP limitations

The first release does **not** provide:

- metadata protection (padding, cover traffic, onion routing);
- a strong OS sandbox for arbitrary Agents;
- formal verification of the audit chain;
- protection against a fully compromised user device;
- enterprise retention, legal hold, or compliance export;
- post-quantum cryptography.

These limitations are stated in the README and in the UI where they matter.

## Responsible disclosure

See [SECURITY.md](../SECURITY.md). Do not open a public issue for a vulnerability.
