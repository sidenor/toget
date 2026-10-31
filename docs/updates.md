# Signed releases and updates

Toget updates are signed documents plus immutable artifacts. The update service
is a static file server; it never holds a private key and cannot choose what a
client installs. A client verifies the channel, the manifest, the artifact
hash, and the installed binary before switching releases.

This document describes what is implemented. Automatic periodic checks,
official-hosted account integration, and forced-update enforcement are not.

## Trust chain

```text
offline root key
  └─ certifies an online release key
       ├─ signs channels/stable.json or channels/beta.json
       └─ signs manifests/v<release>.json
            └─ SHA-256 of daemon/<os>-<arch>/v<release>/<archive>
```

Clients embed the root public key at build time
(`internal/version.ReleaseRootKey`) or read it from `update.root_key`. Every
document is canonical JSON with a domain-separated Ed25519 signature:

- `toget-release-key:v1`
- `toget-release-channel:v1`
- `toget-release-manifest:v1`

The release key certificate has a validity window (90 days by default). The
root private key is offline. Only public certificates and documents are
published. Key files are created with mode `0600`.

## Release tree

```text
releases/
├── channels/stable.json
├── channels/beta.json
├── manifests/v1.1.0.json
├── daemon/linux-amd64/v1.1.0/toget-linux-amd64.tar.gz
├── daemon/darwin-arm64/v1.1.0/toget-darwin-arm64.tar.gz
└── sbom/toget-1.1.0.spdx.json
```

Artifacts are immutable once a manifest is published. A manifest for one
release hashes only that release's `daemon/<os>-<arch>/v<release>/` directory,
so older versions stay in the same tree. The archive must contain the
executable at the manifest's `executable` path (`toget`, or `toget.exe` on
Windows).

## Produce a release

Build the binaries with a release version:

```bash
go build -ldflags "-X github.com/sidenor/toget/internal/version.App=1.1.0 \
  -X github.com/sidenor/toget/internal/version.ReleaseRootKey=$ROOT_PUBLIC_KEY" \
  -o toget ./cmd/toget
```

Package one archive per platform under
`daemon/<os>-<arch>/v<release>/`, then generate keys and documents with
`toget-official`:

```bash
# One-time: create the root key offline, then an online release key.
toget-official release keygen --kind root --out root.key
toget-official release keygen --kind release --out release.key
toget-official release certify --root-key root.key --release-key release.key \
  --out release.cert.json

# Print the base64url public key for the build flag.
toget-official release root-public --root-key root.key --json

# Hash artifacts and sign a manifest.
toget-official release manifest --dir releases --release 1.1.0 \
  --base-url https://releases.example --key release.key --cert release.cert.json \
  --root-key root.key --sbom releases/sbom/toget-1.1.0.spdx.json \
  --changelog "Security and reliability fixes"

# Point a channel at the signed manifest. Start with a small rollout.
toget-official release channel --dir releases --channel stable --release 1.1.0 \
  --base-url https://releases.example --rollout 10 \
  --key release.key --cert release.cert.json --root-key root.key

# Verify exactly what a client would fetch.
toget-official release verify \
  --channel-url https://releases.example/channels/stable.json \
  --root-key root.key --current 1.0.0 --os linux --arch amd64
```

`release manifest` and `release channel` require `--root-key` and verify that
the release certificate is signed by that root before signing anything. Both
commands refuse to overwrite existing files unless `--force` is passed. Do not
use `--force` for an already published release: republishing different bytes
under the same version is rejected by clients that have the old manifest hash.

`--min-app` and `--min-protocol` describe the oldest client allowed to install
this release directly. A client outside that window receives `app_too_old` or
`protocol_too_old` and must first install an intermediate release. `--level`
is `recommended`, `security`, or `forced`; staged rollout applies only to
`recommended`.

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
    "health_url": "http://127.0.0.1:7718/healthz"
  }
}
```

`restart` is local configuration and is never taken from a signed manifest.
Without it, an update is staged and waits for an operator restart. The default
post-restart probe runs the new binary as `<binary> --json version` and checks
the app, protocol, schema, OS, and arch; `health_command` overrides the
arguments. `health_url` is an optional additional HTTP 200 check.

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

- There is no periodic update check or in-app “update available” prompt yet;
  run `toget update check` or schedule it externally.
- `security` and `forced` levels affect eligibility, but the current CLI does
  not enforce a forced restart or a “not now” policy.
- The official update service is a static file server and has no per-device
  authorization, telemetry, or rollout feedback. Rollout bucketing is
  deterministic on the client.
- Delta updates, reproducible builds, and automated SBOM generation are not
  implemented. SBOMs can be referenced and served, but the release process
  must produce them.
- A database schema migration is not rolled back with the binary. The update
  checks the manifest's `schema` before installing, but if the new release
  starts, migrates the database, and then fails its health check, the previous
  binary may reject the newer schema. Run `toget backup` before any
  schema-increasing update; automatic rollback restores the previous release,
  not the database. `update apply` prints a warning when the manifest raises
  the schema.
- Windows and macOS have the same Go update path, but only Linux has been
  exercised end to end.
- The managed root keeps `previous` for rollback but does not automatically
  prune old version directories.
