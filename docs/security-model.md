# Security Model (Draft)

> Status: design draft. This document describes the intended trust boundaries. It is not a claim that the first release is secure for high-stakes use.

Toget's security model follows one rule: **the endpoint is the trust boundary**. An endpoint is a machine that holds keys and can decrypt room content. The official service is never an endpoint.

## Assets

- Account identity, account key, and device keys.
- Room epoch keys and message content.
- Agent credentials, API keys, and local file access.
- Approval decisions and the audit trail.
- The account master key (MK) and recovery credential on official-hosted accounts.
- Server root keys and release signing keys.
- Metadata: who talks to whom, when, and how often.

## Who can see what

| Party | Message content | Room keys | Metadata | Notes |
| --- | --- | --- | --- | --- |
| User's own endpoint (self-hosted server or own device) | Yes | Yes | Yes | The trust boundary |
| Other room members | Only what is shared in the room | Their epoch keys | Room events | Leaving rotates the epoch |
| Official relay | No | No | Target, time, size, sender device | Sees the outer envelope only |
| Official account host (official-hosted account) | No | No | Account record, device list, room membership, ciphertext sizes | Blind store |
| Official room home node | No | No | Room event ordering, member list | Ciphertext only |
| Official Agent, explicitly authorized | Scoped only | Scoped only | Scoped only | Visible and revocable authorization |
| Self-hosted account host | Yes | Yes | Yes | It is the user's endpoint |
| Relay run by a third party | No | No | Same as official relay | Same envelope rules |

The official service has no universal key and no backdoor. There is no "compliance access" path to plaintext.

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

## Adversaries and threats

### Malicious or compromised relay

A relay forwards encrypted envelopes and sees the outer header.

Controls:

- The inner message is encrypted end to end; the relay never receives epoch keys.
- AAD binds the ciphertext to `room_id`, `epoch`, and `message_id`, so a relay cannot move, replay, or relabel messages.
- Outer headers are signed by the sender device; recipients verify the signature.
- Relays are replaceable: users can choose a different parent or self-host one.
- Rate limits, quotas, and size limits bound abuse.

Residual risk: traffic analysis. A relay can see who sends to whom, when, and how large messages are. v1 does not hide metadata.

### Compromised official account host

An attacker who compromises the official account host gets account records, device lists, room membership, and ciphertext.

Controls:

- Message content and room epoch keys are not stored there.
- The official host stores `Enc(KEK(password), MK)` and `Enc(RK, MK)`, never the password, KEK, MK, or RK in plaintext.
- Login uses PAKE, so the password is not sent to the host.
- The host cannot silently add a device: new devices require the account key or an existing device's approval.
- Users can review and revoke devices; revocation rotates room keys.

Residual risk: an attacker with the ciphertext can attempt offline guessing against the password-wrapped MK. Argon2id and a strong password policy slow this down; the recovery key is high-entropy and not guessable.

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
- An Agent can be stopped and revoked from the local UI or the room.

### Prompt injection

An Agent reads a web page, email, file, or message that tells it to exfiltrate data or spend money.

Controls:

- Tool results and external content are labeled as untrusted in the Agent context.
- Actions that cross a trust boundary require approval: `external.send`, `money.spend`, `destructive`.
- Approval cards show the concrete target, parameters, and amount from the canonical action, not a model-written summary.
- Policies restrict which domains, recipients, and tools an Agent may use.
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

### Recovery credential compromise

The recovery credential is a bearer secret: whoever holds it and the account ID can reset the password and unwrap MK.

Controls:

- It is generated client-side and downloaded at registration; registration cannot complete without the download confirmation.
- The filename includes the 14-digit account ID; the file contains a checksum and the official server fingerprint.
- The reset page is static, open source, and uses the browser's File API to read the file locally. The file is never uploaded. WebCrypto unwraps MK and re-wraps it with the new password's KEK; only the new wrapped blob and verifier are sent.
- Treat it like a private key: offline storage, not email, not a chat app, not a cloud drive without encryption.
- Logged-in devices can regenerate it; the old credential becomes void.

### Account takeover via email

Email is only a login-recovery channel, not a decryption key.

Controls:

- An email reset restores login but cannot open old history without the recovery credential or a logged-in device.
- The reset page states this plainly before the user proceeds.
- New device enrollment is visible to existing devices; users can revoke it.

### Supply chain and malicious updates

Controls:

- Signed release manifests: root key certifies release keys; release keys sign version manifests; manifests list artifact hashes.
- Atomic updates: download, verify signature and hash, extract to a new directory, switch the launcher, restart with a delay, health check, roll back on failure.
- The previous version is kept for rollback.
- Reproducible builds are a goal; checksums and SBOMs ship with every release.
- Dependencies are pinned, scanned, and reviewed; the daemon and protocol are open source and auditable.

### Directory poisoning

A malicious or compromised directory returns the wrong manifest for a server code.

Controls:

- Manifests are self-certifying: the client checks `server_code == hash(root_pubkey || nonce)`.
- A manifest that does not match is rejected, even if it is signed by the directory.
- Cached cards and QR codes bypass the directory entirely.
- The 8-digit code is only about 27 bits. An attacker can grind a key pair until the short code matches a target. The code is a routing hint, not an authentication factor; clients verify the full `server_id` fingerprint when the threat model requires it, and always verify the manifest signature.

### Local attackers and shared machines

Controls:

- The local API binds to `127.0.0.1` or a Unix socket and requires a token.
- The UI checks `Origin` and uses `SameSite=Strict` cookies.
- Secrets live in the OS keychain or an encrypted local vault.
- Logs redact known secret patterns.

## Cryptographic choices

- Ed25519 for signatures; X25519 for key agreement.
- HPKE (RFC 9180) with X25519 + HKDF-SHA256 + ChaCha20-Poly1305 for wrapping room keys.
- XChaCha20-Poly1305 or AES-256-GCM for message content.
- Argon2id for password verifiers and key derivation; PAKE for login.
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
