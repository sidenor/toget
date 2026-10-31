# 0009 — Versioned protocol, signed releases, atomic updates

**Status:** accepted; signing, release tooling, and client updates implemented
**Date:** 2026-10-31

## Context

Toget is a network of independently operated nodes. Some run on the official service; some run on a home server that may be offline for weeks. A protocol change, a bad release, or a compromised update channel can break rooms, lock users out, or turn a relay into an attack vector.

The project needs a versioning and release model that a self-hoster can reason about and that the official service cannot abuse.

## Decision

1. **Three version numbers:**
   - **App version:** SemVer, visible to users.
   - **Protocol version:** integer, negotiated in the handshake.
   - **Schema version:** integer, with forward migrations and a backup before an incompatible change.
2. **Capability flags** advertise optional features; peers never assume them.
3. **Support window:** the current and previous two minor app versions (N-2) can log in and exchange messages.
4. **Channels:** `stable` and `beta`. Releases are immutable once published.
5. **Release artifacts** are addressed by device and version: `daemon/<os>-<arch>/v<version>/`. `channels/stable.json` and `channels/beta.json` point at the current version; `manifests/v<version>.json` lists artifacts, hashes, protocol/schema versions, and minimum supported versions.
6. **Signing chain:** an offline root key certifies online release keys; release keys sign version manifests; manifests list artifact hashes. Clients embed the root public key and refuse anything that does not chain to it.
7. **Atomic update:** download to a temporary directory, verify signature and hash, extract a new version directory, switch the launcher/symlink, restart with a delay, run a health check, and roll back automatically on failure. Keep the previous version.
8. **Forced updates** only for protocol-incompatible or security-critical releases, and only for devices that can reach the official service. LAN-only and offline deployments are not forced offline.
9. **Release checks:** checksums, an automatically generated SPDX 2.3 SBOM, signature verification, reproducible builds with a double-build hash check, whole-tree verification before publication, and a smoke test that installs the previous version, updates, and rolls back.
10. **Canary rollout:** 1% → 10% → 50% → 100%. The service is static, so a person or external monitoring must pause a rollout by repointing the channel or yanking the release.

## Consequences

- A self-hoster can stay offline for a while and rejoin within the support window.
- A compromised official service cannot ship an unsigned update; the root key is the trust anchor, not the server.
- Rollback is a first-class path, not an emergency script.
- Maintaining N-2 means protocol work must be additive and tested against older peers.
- Key management for the root and release keys is operational overhead; the root stays offline or in an HSM.
- Immutable artifacts and manifests make caching, mirrors, and third-party verification possible.

## Implementation status

Items 1–7 and 9 are implemented, including signed channel/manifest/index
documents, detached signatures, atomic updates, startup reconciliation, and
rollback. Item 8 is partly implemented: clients prompt for forced releases and
limit postponement to 24 hours, but official-hosted account enforcement depends
on hosting that does not exist yet. Item 10 uses deterministic client-side
buckets; there is no automatic pause because the update service is static.

## Alternatives considered

- **No protocol versioning, "just update":** breaks offline and self-hosted nodes, and makes rollback unsafe.
- **CalVer only:** fine for marketing, but says nothing about protocol compatibility.
- **Forced auto-update for everyone:** simple for the operator, hostile to self-hosters and offline deployments.
- **Unsigned releases with HTTPS only:** trusts the CDN and the official service; a compromise becomes a supply-chain attack.
- **In-place binary replacement:** cheaper to implement, but leaves no clean rollback and can corrupt a running install.

## Review trigger

Revisit if N-2 proves too expensive for the protocol surface, or if a platform's packaging rules require a different update path. The signing chain and the atomic switch should survive any change.
