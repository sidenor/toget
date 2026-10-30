# 0006 — Account model: 14-digit ID, official or self-hosted host

**Status:** accepted (design)
**Date:** 2026-10-31

## Context

Toget needs a public account identifier that works without email, phone numbers, or a central directory lookup for every message. It also needs to let an account live on the official service or on a machine the user controls, without splitting the product into two incompatible modes.

Earlier drafts assumed email or OAuth identities and a single server. That made self-hosting a second-class deployment and gave the official service a permanent identity monopoly.

## Decision

1. **A public account ID is 14 decimal digits**, displayed as `48392017-174628`:
   - 8 digits: server code = `decimal(SHA-256(server_pubkey || server_nonce)) mod 100_000_000`, zero-padded.
   - 6 digits: account code assigned by the account host, unique on that host.
2. **The account host is chosen at registration**: the official service or a machine the user controls. It stores account records, verifies login, assigns the account code, and keeps the device list.
3. **One device belongs to one account.** A device cannot be shared by multiple people. Switching accounts requires logging out and wiping local keys and caches.
4. **One account can have many devices.** Devices are persistent nodes, client Apps, or pure CLI instances.
5. **Login always reaches the account host**, directly or through a relay. There is no "logged in, but the data is elsewhere" state.
6. **The account host is the source of truth for account records and membership**, not necessarily the only place history is stored.
7. **The ID is a lookup key, not a secret.** Cryptographic identity is the account key and the host's signed manifest.

## Consequences

- A user can share a short numeric ID that works across official and self-hosted accounts.
- The server code is self-certifying: a directory cannot impersonate a host.
- Self-hosting keeps the same identity and protocol; there is no separate "self-hosted edition".
- The official service can assign account codes without being able to read content.
- Device enrollment, revocation, and multi-device key wrapping become core protocol features.
- Losing the account host's root key changes its server code; the host key must be backed up.
- Moving an account to another host is a migration, not a setting; it needs its own flow.

## Alternatives considered

- **UUID or random string IDs:** no routing information, so every lookup needs the directory and the ID leaks nothing about the host. Rejected because a self-certifying server code is more useful offline and in QR codes.
- **Email or phone as the primary identity:** familiar, but ties the account to a provider and makes self-hosting awkward. Email remains a login-recovery channel only.
- **Domain-based IDs (`user@example.com`):** natural for organizations, but requires DNS control and is unfriendly for individuals.
- **One ID per device:** simpler enrollment, but breaks the "one account, many devices" model and makes rooms address devices instead of people.
- **Multi-account devices:** convenient, but weakens the trust boundary and complicates key storage. Rejected for v1.
- **Official-only accounts:** simpler, but makes self-hosting a demo mode and concentrates identity in one operator.

## Review trigger

Revisit if 10^14 addresses become insufficient (unlikely), or if device-sharing becomes a real user need that cannot be met by separate accounts. Any change must preserve the self-certifying server code and the one-device-one-account rule for v1.
