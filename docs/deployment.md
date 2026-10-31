# Deployment (Draft)

> Status: design draft. Installation commands and file paths are the intended shape, not final.
>
> **Implemented today:** `toget serve` provides the local account, vault,
> SQLite log, loopback API/UI, and a same-LAN HTTPS node. `toget-official`
> provides an optional relay and signed-manifest directory. Public account
> hosting, official-hosted accounts, NAT traversal, and packaged installers
> described below are not implemented yet.

Toget is deployed as a **node**: one `toget` binary that can act as a daemon, a self-hosted account host, a relay, or the local CLI. Most users run it on one machine and never think about the rest.

## Modes

| Mode | Where the account lives | Reachability | Who can decrypt |
| --- | --- | --- | --- |
| **Local only** | This machine | LAN | This machine |
| **Self-hosted, public** | This machine (VPS or home server with a public address) | Direct | This machine |
| **Self-hosted behind NAT** | This machine | Outbound to a relay | This machine |
| **Official-hosted** | The official service | The official service | The user's devices |
| **Relay-only** | No account | Public address | Nobody; it forwards ciphertext |
| **Mixed room** | Each member's chosen host | Per member | Each member's endpoint |

The account host is chosen at registration and cannot be moved silently. Moving an account to another host is a future migration flow, not a setting.

## Requirements

- Linux `amd64`/`arm64`/`armv7`, macOS `amd64`/`arm64`, or Windows `amd64`.
- 1 CPU core and 256 MB of RAM for a small node; more if it runs Agents.
- Disk: a few hundred MB for the binary and local data, plus room history and attachments.
- A stable outbound connection. Inbound reachability is optional; a relay covers NAT and firewalls.
- For a public self-hosted account host: a domain is recommended but not required; a static IPv6 address or a working port mapping is enough.

## Ports

| Port | Purpose | Exposure |
| --- | --- | --- |
| `7717` | Node protocol (HTTPS with a pinned Ed25519 certificate) | Reachable by peers; `--no-node` or `--node-listen off` disables it. Outbound relay use needs no inbound port |
| `7718` | Local API and web UI | `127.0.0.1` only |
| `7719` | Relay (HTTPS, pinned certificate) | Public only for `toget-official` or a self-hosted relay |
| `7720` | Directory (HTTPS, pinned certificate) | Public only for `toget-official` or a self-hosted directory |
| `5353/udp` | mDNS discovery (multicast) | Local link only; no router exposure. Enabled by default; `--no-mdns` disables it |

Do not expose `7718` to the network. The local UI is not designed to be a public web app. `7717` is self-authenticated by the certificate pin carried in invite codes; do not put it behind a TLS-terminating proxy, because that changes the certificate the peer pins.

## Install

### Linux

- Binary in `/opt/toget/versions/<version>/`, with `/opt/toget/current` as a symlink.
- `toget` on `PATH` through `/usr/local/bin/toget`.
- State in `/var/lib/toget/{config,identity,data,keys,relay,logs,backups,update}`.
- Service unit in `/etc/systemd/system/toget.service`; runs as a dedicated `toget` user.
- `toget doctor` checks ports, permissions, disk space, clock skew, and connectivity.

### macOS

- Binary and app bundle in `/Applications/Toget.app` or a Homebrew cask.
- State under `~/Library/Application Support/Toget`.
- `launchd` agent for login-time start; secrets in Keychain.
- Signed and notarized for distribution.

### Windows

- Installer under `%ProgramFiles%\Toget`, state under `%ProgramData%\Toget`.
- Windows service registered by the installer; secrets in Credential Manager.
- Signed installer and binaries.

### Container

A container image is published for self-hosted account hosts and relays. Mount a persistent volume for `/var/lib/toget` and publish `7717` only if the node should accept direct connections.

```yaml
services:
  toget:
    image: ghcr.io/sidenor/toget:latest
    restart: unless-stopped
    ports:
      - "7717:7717/tcp"
      - "7717:7717/tcp"
    volumes:
      - toget-data:/var/lib/toget
volumes:
  toget-data:
```

The container is a convenience. A static binary on the host is the reference deployment, because it can use the host's keychain, service manager, and update path.

## Create an account

```bash
toget account create
```

The command asks where the account should live:

- **This machine** — self-hosted. The node prints the 14-digit account ID and starts serving as the account host.
- **Official** — the node registers with the official service and stores the account record there.

For an official-hosted account, registration generates the account master key and the recovery credential. The credential file must be downloaded and confirmed before registration completes:

```text
toget-recovery-48392017-174628.togetkey
```

Store it offline. It is a bearer secret: anyone with the file and the account ID can reset the password and unwrap the account key. It is never uploaded to the official service; the reset page reads it locally in the browser.

Self-hosted accounts do not have a recovery credential. The local administrator can reset the password:

```bash
toget account reset-password
```

or from the local UI at `127.0.0.1:7718`. If the machine is lost and there is no backup, the data is lost. Full-disk encryption and regular backups are strongly recommended.

## Connect from another device

```bash
toget device enroll --account 48392017-174628
```

The new device shows a request; an existing device or the account host approves it. The account key signs the new device certificate, and the current room epoch keys are wrapped to it. The new device then syncs room history up to its access.

Device roles:

- **Persistent node** — always on, callable, stores a copy, can relay.
- **Client App** — phone/tablet; chat and approvals, no relay, not callable.
- **Pure CLI** — no persistent service, for scripts and servers.

## Networking

### Direct

The node advertises its endpoint in its signed manifest. Clients probe that
pinned HTTPS endpoint first and use it when reachable. IPv6 preference, port
mapping (UPnP/NAT-PMP/PCP), and QUIC hole punching are design-only; today a
direct connection works on a LAN, a VPN, or a genuinely reachable public
address. Direct connections are preferred because they are faster and keep
relays out of the path.

### Same-LAN discovery

`toget serve` advertises `_toget._tcp.local.` over multicast DNS by default.
`toget node lookup <code>` tries a saved business card first, then a verified
LAN result, then the directory, so a local network without internet can still
resolve a server code. The advertisement contains the server ID, server code,
endpoint, and TLS fingerprint; it does not contain account or room data.

```bash
toget serve --no-mdns        # disable advertisement and browsing
toget node discover          # list current local advertisements
toget node lookup 12345678   # saved card, verified LAN, directory fallback
```

Discovery requires UDP `5353` and IGMP on the local link. It does not cross
routers, VLANs, or NAT, and many guest Wi-Fi networks block multicast. The
multicast packet is only a hint: lookup fetches and verifies the signed
identity and node manifests over the advertised certificate pin. See
[discovery.md](discovery.md).

### Business cards

A card is a directory-free, copyable introduction. It embeds the account's
signed identity manifest and a 24-hour node manifest, and can be shown as text
or a QR code. The receiver verifies both signatures and then the live node
before using the address.

```bash
toget card show                     # card text
toget card show --qr                # terminal QR code
toget card show --png /tmp/card.png # PNG for print or chat
toget card add '<toget-card:v1:…>'  # verify and save
toget card list
```

Saved cards live in `cards.json` with mode `0600` and are not part of
encrypted backups, so a restore onto a fresh data home does not bring them.
Re-import them if you want the pins. A saved card pins the full server ID for
its eight-digit code. See [cards.md](cards.md).

### Relay

If direct fails, the node uses its configured relay. Run the optional relay and
directory on a public host:

```bash
toget-official --home /var/lib/toget-official \
  --relay-advertise relay.example.com:7719 \
  --directory-advertise directory.example.com:7720
```

Point a node at them, or let it discover the directory's default relay:

```bash
toget serve \
  --node-directory 'https://directory.example.com:7720#fp=<hex>' \
  --node-relay 'https://relay.example.com:7719#fp=<hex>'

toget serve --node-directory '...' --node-relay auto
toget serve --no-relay            # direct only
toget serve --no-mdns             # disable same-LAN discovery
toget node info                   # direct URL, relay state, directory, discovery
```

- The directory's advertised relay is the default for `auto`; a node can also use a friend's relay, a community relay, or its own `toget-official`.
- A relay sees the target server, signed account/device headers, path, timing, size, and ciphertext. It cannot decrypt room content.
- Relays can queue requests for an offline node for 55 seconds and can chain to a parent relay.
- `--no-relay` disables outbound fallback only. Ordinary nodes do not accept relay traffic for others; a separate `toget-official` process is the supported relay.
- Existing rooms keep the home address they were given, so changing a relay may require a fresh invite or lookup.

See [relay.md](relay.md) for quotas, the offline queue, trust boundaries, and
current limitations.

### Public node checklist

- Firewall allows `7717` (UDP and TCP) from the internet.
- `7718` is bound to localhost only.
- The service runs as a non-root user with a read-only binary directory.
- Disk quotas are set for room history and relay queues.
- Logs are rotated.
- The host key is backed up; losing it changes the server code.

## Updates

- The daemon checks the signed release manifest on a schedule.
- Updates download to a temporary directory, verify the signature and hash, extract a new version directory, switch `current`, and restart with a delay.
- A health check runs after restart; failure rolls back to the previous version.
- The previous version is kept until the next successful update.
- Auto-update is opt-in for self-hosted nodes and on by default for official-hosted accounts, where the user can still choose "not now" unless the release is security-critical or protocol-incompatible.

## Backups

What matters, in order:

1. **Account and device keys** — without them, encrypted history is unreadable.
2. **Room history and local data** — `/var/lib/toget/data` or the platform equivalent.
3. **Configuration** — relay parent, ports, Agent policies.
4. **Recovery credential** — for official-hosted accounts, stored offline.

A backup is only useful if restore is tested. `toget backup` writes an encrypted archive; `toget restore` verifies and restores it. Backups never leave the machine unless the user moves them.

## Observability

- Structured logs to stdout and to a rotating local file.
- `toget status` shows rooms, peers, relay path, connected devices, and Agent state.
- Metrics are exposed locally for the user; no telemetry is sent anywhere by default.
- Crash reports are opt-in and never include message content or keys.

## Security checklist for self-hosters

- Full-disk encryption on the account host.
- `7718` bound to localhost or a Unix socket.
- A dedicated non-root service user.
- Automatic security updates for the OS.
- Backups tested at least once.
- Recovery credential stored offline for official-hosted accounts.
- Review the device list after any suspicious login.
- Keep the node within the supported version window (N-2).
