# Signed releases and updates

Toget updates are signed documents plus immutable artifacts. The update service
is a static file server; it never holds a private key and cannot choose what a
client installs. A client verifies the channel, the manifest, the artifact
hash, and the installed binary before switching releases.

This document describes the implemented release and update workflow:
automatic periodic checks, the local update API and web banner, the signed
release index, yank handling, SBOM and reproducible-build tooling, and the
append-only publishing script. The remaining limits are listed at the end.

## Trust chain

```text
offline root key
  └─ certifies an online release key
       ├─ signs channels/stable.json or channels/beta.json
       ├─ signs manifests/v<release>.json
       └─ signs manifests/index.json
            └─ SHA-256 of daemon/<os>-<arch>/v<release>/<archive>
```

Clients embed the root public key at build time
(`internal/version.ReleaseRootKey`) or read it from `update.root_key`. Every
document is canonical JSON with a domain-separated Ed25519 signature:

- `toget-release-key:v1`
- `toget-release-channel:v1`
- `toget-release-manifest:v1`
- `toget-release-index:v1`
- `toget-release-detached:v1`

The release key certificate has a validity window (90 days by default). The
root private key is offline. Only public certificates and documents are
published. Key files are created with mode `0600`.

Channel pointers are validated against the current certificate validity
window. Manifests and indexes are validated at their RFC3339 `published_at`
time, so an archived release remains verifiable after its signing certificate
expires. The signing tools refuse to sign a document outside the certificate
window.

`manifest`, `channel`, and `index` write a detached `.sig` sidecar by default.
The sidecar is self-describing:

```json
{
  "format": "toget-release-signature",
  "version": 1,
  "document": "manifests/v1.1.0.json",
  "sha256": "...",
  "key_id": "...",
  "signature": "..."
}
```

The sidecar signature covers the canonical sidecar JSON, including the
document path and document hash. Moving a sidecar to another document does not
verify. `--no-sig` omits it for a client that only needs the inline signature.

## Release tree

```text
releases/
├── channels/stable.json
├── channels/stable.json.sig
├── channels/beta.json
├── channels/beta.json.sig
├── manifests/index.json
├── manifests/index.json.sig
├── manifests/v1.1.0.json
├── manifests/v1.1.0.json.sig
├── daemon/linux-amd64/v1.1.0/toget-linux-amd64.tar.gz
├── daemon/darwin-arm64/v1.1.0/toget-darwin-arm64.tar.gz
└── sbom/toget-1.1.0.spdx.json
```

Artifacts are immutable once a manifest is published. A manifest for one
release hashes only that release's `daemon/<os>-<arch>/v<release>/` directory,
so older versions stay in the same tree. The archive must contain the
executable at the manifest's `executable` path (`toget`, or `toget.exe` on
Windows).

## Build a release

`release build` produces deterministic archives and, unless `--no-sbom` is
passed, an SPDX 2.3 SBOM:

```bash
toget-official release build \
  --out dist/releases \
  --release 1.1.0 \
  --platforms linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64 \
  --root-public-key "$ROOT_PUBLIC_KEY" \
  --epoch 1767225600 \
  --verify
```

The build uses `-trimpath`, `-buildvcs=false`, `-mod=readonly`,
`CGO_ENABLED=0`, `GOFLAGS=`, `GOWORK=off`, and a fixed `SOURCE_DATE_EPOCH`.
Archive headers, ordering, compression settings, and the executable build ID
are fixed. `--verify` builds each platform twice and requires identical
archive hashes. `--epoch` defaults to `SOURCE_DATE_EPOCH`, then to the commit
time; pass it explicitly for a reproducible release.

A release key is still needed to sign the result:

```bash
# One-time: create the root key offline, then an online release key.
toget-official release keygen --kind root --out root.key
toget-official release keygen --kind release --out release.key
toget-official release certify --root-key root.key --release-key release.key \
  --out release.cert.json

# Print the base64url public key for the build flag.
toget-official release root-public --root-key root.key --json

# Hash artifacts and sign a manifest. This also generates an SBOM if --sbom
# and --no-sbom are both omitted.
toget-official release manifest --dir dist/releases --release 1.1.0 \
  --base-url https://releases.example --key release.key --cert release.cert.json \
  --root-key root.key \
  --changelog "Security and reliability fixes"

# Point a channel at the signed manifest. Start with a small rollout.
toget-official release channel --dir dist/releases --channel stable --release 1.1.0 \
  --base-url https://releases.example --rollout 10 \
  --key release.key --cert release.cert.json --root-key root.key

# Build the signed catalog after all manifests exist.
toget-official release index --dir dist/releases \
  --key release.key --cert release.cert.json --root-key root.key
```

`release manifest`, `release channel`, and `release index` require
`--root-key` and verify that the release certificate is signed by that root
before signing anything. They refuse to overwrite existing files unless
`--force` is passed. Do not use `--force` for an already published release:
republishing different manifest bytes under the same version is rejected by
clients that have the old manifest hash.

`--min-app` and `--min-protocol` describe the oldest client allowed to install
this release directly. A client outside that window receives `app_too_old` or
`protocol_too_old` and must first install an intermediate release. `--level`
is `recommended`, `security`, or `forced`; staged rollout applies only to
`recommended`.

## SBOM

`release manifest` generates `<dir>/sbom/toget-<release>.spdx.json` when no
`--sbom` is given. `release sbom` regenerates it independently:

```bash
toget-official release sbom --dir dist/releases --release 1.1.0 \
  --out dist/releases/sbom/toget-1.1.0.spdx.json \
  --module-dir .
```

The document is deterministic SPDX 2.3 JSON built from the Go module graph. It
lists the main module and dependencies as packages with `purl` external
references, `DESCRIBES` and `DEPENDS_ON` relationships, and `NOASSERTION` for
fields the build does not know. Its namespace is derived from the app,
version, platform, and module digest. The manifest records the SBOM URL, size,
and SHA-256; the static update service serves it from `sbom/`.

## Release index, latest, and yank

`manifests/index.json` is a signed, newest-first catalog of every published
release. Each entry records the manifest file name, manifest SHA-256, level,
publication time, and optional yank metadata. `latest` is the newest release
that is not yanked. Rebuilding the index preserves existing yank metadata and
refuses to accept a changed manifest for an already published release.

```bash
toget-official release latest --dir dist/releases --root-key root.key
toget-official release latest --dir dist/releases --root-key root.key --all --json
```

To withdraw a bad release:

```bash
toget-official release yank --dir dist/releases --release 1.1.0 \
  --reason "regression in message sync" --replace-with 1.0.0 \
  --key release.key --cert release.cert.json --root-key root.key
```

Yank marks the release in the index and, if `--replace-with` is given,
repoints every channel that still offers it. Without `--replace-with`, the
command refuses while a channel points at the release. The index is written
before the channels so an interrupted yank leaves a visible withdrawn release
rather than a channel the catalog still recommends. `--dry-run` prints the
intended changes. Republishing a yanked release is not supported; publish a
new version.

The client update path still follows a channel and its manifest. The index is
the signed catalog used by operators, mirrors, offline verification, and
future discovery; it is served by the same static service.

## Verify a release tree

Before publishing anything, verify the whole tree offline:

```bash
toget-official release verify-tree --dir dist/releases --root-key root.key
toget-official release verify-tree --dir dist/releases --root-key root.key --json
```

`verify-tree` checks:

- the index signature and sidecar, every indexed manifest, and the manifest
  SHA-256 recorded in the index;
- every manifest signature, certificate, sidecar, level, and publication time;
- every artifact and SBOM hash and size;
- every channel signature, sidecar, manifest target, release name, and that
  the target is indexed and not yanked;
- that the tree contains no symlinks, special files, unreferenced files, extra
  manifests, unmatched sidecars, or top-level files outside `channels/`,
  `manifests/`, `daemon/`, and `sbom/`.

`--skip-artifacts` verifies metadata without hashing large archives. A tree
that fails this command must not be published. The same checks protect against
accidentally uploading a private key, a log, or an editor file alongside a
release.

## Publish a release tree

`scripts/publish-release.sh` verifies the tree and uploads it append-only:

```bash
scripts/publish-release.sh \
  --tree dist/releases \
  --target r2:toget-releases/toget \
  --root-key root.key \
  --dry-run
```

Use `--backend rclone` for object stores where `rclone --immutable` enforces
that existing artifacts cannot be replaced, or `--backend aws` for S3. The
script uploads immutable artifacts first, then manifests, then channels, and
writes the mutable index last. It never deletes remote objects. Cache headers
are one year and `immutable` for artifacts, manifests, and SBOMs; 60 seconds
and `must-revalidate` for channels and the index. A partial upload is safe to
rerun: immutable objects already present are skipped.

The AWS backend cannot enforce immutability the way `rclone --immutable` can,
so it prints a warning. Use bucket versioning and object-lock policy where
available.

## Serve the release tree

Run the static update service alone:

```bash
toget-official --no-relay --no-directory \
  --update-dir /srv/toget-releases \
  --update-listen 127.0.0.1:7721
```

It serves only `channels/`, `manifests/`, `daemon/`, and `sbom/`; it has no
directory listing or dotfiles, and supports GET, HEAD, ranges, and conditional
requests. A non-loopback listener requires `--update-tls-cert` and
`--update-tls-key` unless `--update-insecure-http` is explicitly set. In
production, terminate TLS at a reverse proxy or CDN and keep this service on
loopback.

The service can run with the relay and directory in the same process by
omitting `--no-relay` and `--no-directory`.

## Configure a node

Updates are off until the root, channel URL, and trust anchor are configured.
In `config.json`:

```json
{
  "update": {
    "root": "/opt/toget",
    "channel": "stable",
    "channel_url": "https://releases.example/channels/stable.json",
    "root_key": "BASE64URL_ROOT_PUBLIC_KEY",
    "restart": ["systemctl", "restart", "toget"],
    "health_url": "http://127.0.0.1:7718/healthz",
    "mode": "notify",
    "check_interval": "6h"
  }
}
```

`restart` is local configuration and is never taken from a signed manifest.
Without it, an update is staged and waits for an operator restart. The default
post-restart probe runs the new binary as `<binary> --json version` and checks
the app, protocol, schema, OS, and arch; `health_command` overrides the
arguments. `health_url` is an optional additional HTTP 200 check.

`update.mode` is one of:

| Mode | Behavior |
| --- | --- |
| `notify` (default) | Check on the configured interval and show the in-app banner. Never downloads automatically. |
| `auto` | Check and stage an eligible release in the background. It does **not** restart; the operator still confirms the restart. |
| `off` | Do not schedule checks. Manual `toget update check` still works. |

`update.check_interval` defaults to `6h` and must be at least `5m`. The first
check waits ten seconds after daemon start. The monitor persists its last
result, dismissal, and staged release so the banner survives a restart.

Automatic checks never restart the daemon. Staging and restart are separate
steps so an update cannot interrupt an active Agent run unless the operator
chooses it. The local UI offers “Restart now” when `update.restart` is
configured and the platform supports a detached restart. Windows stages the
update but requires a manual service restart.

## Client commands

```bash
toget update check
toget update status
toget update apply
toget update finish
toget update rollback
```

- `check` verifies the channel and manifest, selects this OS/arch, enforces
  app/protocol/schema compatibility and the rollout bucket, and prints the
  available release.
- `apply` downloads the artifact, verifies its SHA-256, extracts it to a
  staging directory, verifies the executable, runs a preflight health check,
  installs a new version directory, writes a pending record, switches
  `previous` and `current`, and optionally restarts.
- `finish` confirms a staged update after a restart. If a restart command is
  configured, it runs it and checks that the new binary is healthy. A failure
  rolls back automatically.
- `rollback` discards a pending update, or switches `current` back to the
  retained previous release.

Flags override config: `--update-root`, `--channel`, `--channel-url`,
`--root-key`, `--restart`, `--health-command`, `--health-url`, `--no-restart`,
`--force` (ignore rollout), and `--allow-downgrade`. `--allow-downgrade` does
not bypass protocol or schema downgrade checks.

## Update API and web UI

The local API exposes the monitor to the web UI:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/update` | Current release, last check, banner state, staged update, previous release |
| `POST` | `/api/v1/update/check` | Run a check; `{"force": true}` ignores rollout |
| `POST` | `/api/v1/update/apply` | Stage a release; `{"release": "1.1.0"}` |
| `POST` | `/api/v1/update/dismiss` | Hide a release for a bounded period |
| `DELETE` | `/api/v1/update/dismiss` | Show a dismissed release again |
| `POST` | `/api/v1/update/rollback` | Discard a pending update or return to the previous release |
| `POST` | `/api/v1/update/restart` | Run the configured local restart command |

The web UI shows a banner for an available, security, or forced release, with
the changelog and level. A staged update shows “restart required”; Settings
shows the installed version, channel, mode, interval, last check, restart
configuration, pending state, and rollback controls. When updates are not
configured, Settings shows the missing configuration keys instead of a banner.

A “later” dismissal lasts 24 hours by default and at most seven days. A forced
release can be postponed for at most 24 hours; after that the banner returns.
This is a client-side honesty mechanism, not a security boundary: a user who
controls the binary can always ignore it. Enforcement tied to official-hosted
accounts is not implemented.

## Managed layout

```text
<root>/
├── versions/v1.0.0/
├── versions/v1.1.0/.toget-release.json
├── current -> versions/v1.1.0
├── previous -> versions/v1.0.0
├── state.json
├── tmp/
└── .update.lock
```

The first update adopts the running executable into `versions/v<current>` so
there is a rollback target. If the running executable cannot be inspected,
`apply` proceeds without a rollback target and prints a warning. Files are
extracted under a staging directory first; `.toget-release.json` records the
release, protocol, schema, executable hash, manifest hash, archive hash, and
channel. `state.json` records `current`, `previous`, the rollout ID, and any
pending update.

A daemon startup calls reconciliation when `update.root` is configured. If the
running binary is the pending release, the update is finalized. If an older
binary started, the links and state are restored to the previous release. When
nothing is pending, missing or mismatched `current`/`previous` links are
repaired from `state.json`; this covers a crash after saving state but before
switching links during adoption, rollback, or automatic rollback. A failed
reconciliation is logged and never prevents the daemon from starting.

## Install as a system service

`toget service` writes a service definition that runs `toget serve` with the
current `TOGET_HOME`:

```bash
# Linux, per-user systemd unit (default). Enable lingering for a headless host.
toget service install
sudo loginctl enable-linger "$USER"

# Linux, system-wide unit (run as root).
sudo toget service install --scope system

# Preview the unit and activation commands without writing anything.
toget service install --dry-run

# Write the definition but do not enable or start it.
toget service install --no-start

toget service status
sudo toget service uninstall --scope system
```

On macOS the same commands write a LaunchAgent (`~/Library/LaunchAgents`) or,
with `--scope system`, a LaunchDaemon (`/Library/LaunchDaemons`). On Windows,
`toget service install` registers an automatically started Windows service
with restart-on-failure recovery; `--scope user` is not supported there.
`--unit-dir` overrides the definition directory for testing or unusual
layouts. Existing definitions are never overwritten without `--force`.

The service manager only starts the process; it does not install the binary,
create an OS user, or configure a reverse proxy. Place the binary at a stable
path (for example `/opt/toget/versions/current/toget`) before installing the
unit. A packaged installer and signed macOS/Windows packages are still future
work.

## Failure and recovery

- Signature, hash, archive, platform, or compatibility failure: nothing is
  switched.
- Failure before the restart: staged files are discarded or the previous
  release is restored.
- Failure after the restart: the previous release is restored and, when a
  restart command exists, restarted and health-checked.
- Crash between saving state and switching links: the next daemon startup
  completes the saved transition from `state.json`. Pending updates use the
  running binary to decide whether to finalize or roll back.
- Corrupt links or version directories: `toget update status` reports issues
  rather than guessing. A malformed `state.json` makes update commands fail
  instead. Inspect `<root>` and restore the previous release manually if
  automatic recovery is impossible.

The lock file prevents two updates from running at once.

## Limits

- The official update service is a static file server and has no per-device
  authorization, telemetry, or rollout feedback. Rollout bucketing is
  deterministic on the client, and pausing a rollout is an operator action:
  repoint the channel or yank the release.
- `security` and `forced` levels affect the banner and the maximum dismissal
  period, but there is no server-side enforcement. Official-hosted accounts do
  not yet require a forced update.
- Delta updates are not implemented. Every update downloads a full archive.
- A database schema migration is not rolled back with the binary. The update
  checks the manifest's `schema` before installing, but if the new release
  starts, migrates the database, and then fails its health check, the previous
  binary may reject the newer schema. Run `toget backup` before any
  schema-increasing update; automatic rollback restores the previous release,
  not the database. `update apply` prints a warning when the manifest raises
  the schema.
- Linux is the only platform exercised end to end. macOS and Windows use the
  same Go update path and are cross-built in CI, but real-machine service and
  update testing is still pending.
- Automatic restart is unavailable on Windows; staged updates need a manual
  service restart.
- The managed root keeps `previous` for rollback but does not automatically
  prune old version directories.
- The release tree verifier and publishing script do not manage CDN
  invalidation. Mutable channel and index objects use a short cache lifetime.
