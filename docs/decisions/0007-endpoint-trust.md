# 0007 — The endpoint is the trust boundary

**Status:** accepted (design)
**Date:** 2026-10-31

## Context

Toget can host an account on the official service or on a machine the user controls. A conventional design would make the official server the place where messages are stored and read, and would treat self-hosting as a smaller copy of that server. That design gives the operator the keys, makes "we cannot read your messages" impossible to prove, and turns every feature into a trust request.

The product promise is the opposite: people and Agents share rooms, and the service that moves messages should not be able to read them.

## Decision

1. **An endpoint is a trust boundary that holds keys and can decrypt.** For a self-hosted account, the user's server is the endpoint. For an official-hosted account, the user's devices are the endpoint. The official service is never an endpoint by default.
2. **Messages are end-to-end encrypted** with a per-room epoch key, wrapped to each member endpoint with HPKE. Relays and hosts forward ciphertext.
3. **The official service is a blind store and a relay.** It sees account metadata, membership, ciphertext sizes, and outer envelope headers (who, when, how large), not room content.
4. **No universal key and no backdoor.** There is no operator path to plaintext. The only exception is an official Agent that the user explicitly authorizes for a specific scope, and that authorization is visible and revocable.
5. **History follows participants.** Each participant's machine stores a copy. The official service stores a copy only when it is involved: an official-hosted participant, an official Agent, or an official home node — always as ciphertext.
6. **New-member history is opt-in.** The inviter decides whether to attach none, a recent window, or all history; the default is none, and the room is told what was attached.
7. **Official-hosted accounts use a recovery credential.** The account master key is wrapped by a password-derived KEK and by a high-entropy recovery key. Email resets login; the credential restores old history. The credential file is never uploaded.

## Consequences

- The privacy claim is architectural, not a policy promise.
- Self-hosted and official-hosted accounts can share rooms without the official service seeing the self-hosted side's content.
- Server-side features that need plaintext (search, summarization, moderation) must run on an endpoint or with explicit, scoped authorization. They cannot be assumed.
- Multi-device key management, epoch rotation, and recovery become core, non-optional parts of the protocol.
- Metadata protection is limited in v1: relays see who talks to whom, when, and how large. This is stated plainly.
- Losing all devices and the recovery credential means losing old history. We say so instead of implying a recovery path that does not exist.
- The official service can still be compromised; the blast radius is metadata and ciphertext, not content.

## Alternatives considered

- **Server-readable messages (conventional SaaS):** simpler features, but gives the operator the keys and contradicts the product.
- **Encryption at rest only:** protects against disk theft, not against the operator. Rejected as the primary model.
- **Key escrow with the official service:** enables account recovery without a credential, but creates a universal key and a backdoor. Rejected.
- **No encryption in v1, add it later:** retrofitting E2E changes the protocol, storage, search, and UI. Rejected; encryption is in the first network message.
- **Onion routing or metadata mixing in v1:** valuable, but adds latency and complexity before the core loop works. Deferred.

## Review trigger

Revisit if a required feature cannot be built without server-side plaintext. The answer should be an endpoint-side implementation or a scoped, visible authorization, not a silent weakening of the model.
