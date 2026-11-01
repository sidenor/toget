# Deployment

> Status: the self-hosted node path and the official account host are
> implemented. Packaged installers and the container image described below are
> still planned; the binary, service installation, and update workflow are
> usable today.
>
> **Implemented today:** `toget serve` provides the local account, vault,
> SQLite log, loopback API/UI, and a same-LAN HTTPS node. `toget service`
> writes and activates a systemd, launchd, or Windows service definition.
> `toget-official` provides an optional account host, relay, signed-manifest
> directory, and static signed update service, plus key, manifest, index, yank,
> SBOM, reproducible-build, and release-tree verification commands. The account
> host stores OPAQUE password records and ciphertext account-key bundles, sends
> verification and recovery email, and serves the embedded browser reset page.
> The daemon checks for updates on a schedule, the web UI shows an update banner
> and settings, and `toget update` checks, stages, finishes, and rolls back
> managed releases. Optional gateway port mapping (PCP, NAT-PMP, UPnP) is
> implemented; UDP hole punching and packaged
> installers are not implemented yet. The official room home is implemented on
> the same service and stores its state in the same host database.

Toget is deployed as a **node**: one `toget` binary that can act as a daemon, a self-hosted account host, a relay, or the local CLI. Most users run it on one machine and never think about the rest.

## Modes

| Mode | Where the account lives | Reachability | Who can decrypt |
| --- | --- | --- | --- |
| **Local only** | This machine | LAN | This machine |
| **Self-hosted, public** | This machine (VPS or home server with a public address) | Direct | This machine |
| **Self-hosted behind NAT** | This machine | Outbound to a relay | This machine |
| **Official-hosted** | The official service | The official service for account login; rooms use a device or self-hosted node | The user's devices |
| **Relay-only** | No account | Public address | Nobody; it forwards ciphertext |
| **Mixed room** | Each member's chosen host | Per member | Each member's endpoint |

The account host is chosen at registration and cannot be moved silently. Moving an account to another host is a future migration flow, not a setting.

## Requirements

- Linux `amd64`/`arm64`, macOS `amd64`/`arm64`, or Windows `amd64`. Linux
  `armv7` currently fails to build because an upstream Argon2 dependency
  assumes a 64-bit `int`; do not deploy it until that dependency is fixed.
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
| `7721` | Signed update service | Loopback by default; public only behind TLS or a reverse proxy |
| `7722` | Official account host (HTTPS + reset page) | Public only for `toget-official`; the reset page requires a browser-reachable HTTPS origin. For Cloudflare, expose 443/8443 or use a Tunnel; 7722 is not a standard proxied port |
| `5353/udp` | mDNS discovery (multicast) | Local link only; no router exposure. Enabled by default; `--no-mdns` disables it |

Do not expose `7718` to the network. The local UI is not designed to be a public web app. `7717` is self-authenticated by the certificate pin carried in invite codes; do not put it behind a TLS-terminating proxy, because that changes the certificate the peer pins.

## Install

### Linux

The reference layout keeps the binary under `/opt/toget` and state under
`/var/lib/toget`:

```bash
sudo install -d -m 0755 /opt/toget/versions/v1.1.0
sudo install -m 0755 toget /opt/toget/versions/v1.1.0/toget
sudo ln -sfn versions/v1.1.0 /opt/toget/current
sudo install -d -m 0700 /var/lib/toget

# Write and start a system-wide unit. The unit runs `toget serve` and sets
# TOGET_HOME=/var/lib/toget.
sudo /opt/toget/current/toget --home /var/lib/toget service install \
  --scope system --executable /opt/toget/current/toget

# Or install a per-user unit and keep it running after logout.
/opt/toget/current/toget service install
sudo loginctl enable-linger "$USER"
```

`toget service install --dry-run` prints the generated unit and activation
commands without touching the system. The system unit has no `User=` line, so
it runs as root; use a custom unit if you need a dedicated service user.
`toget service status` and `toget service uninstall` manage the same unit.

### macOS

`toget service install` writes a LaunchAgent to
`~/Library/LaunchAgents/dev.toget.node.plist` (or a LaunchDaemon under
`/Library/LaunchDaemons` with `--scope system`), loads it at login or boot, and
restarts it if it exits unsuccessfully. State defaults to the per-user data
directory; use `--home` consistently when installing and invoking the CLI.
A signed, notarized `.app` or Homebrew cask is still planned.

### Windows

`toget service install` registers a Windows service named `Toget`, starts it
automatically with delayed start, and configures restart-on-failure recovery.
The binary must remain at the path passed to `--executable`. The service runs
`toget serve --name <name> --home <home>`; `--scope user` is not supported.
Automatic update restart is unavailable on Windows, so a staged update needs a
manual `Restart-Service Toget`. A signed MSI is still planned.

### Container

A container image is planned; none is published yet. When it exists, mount a
persistent volume for the Toget home and publish `7717` only if the node
should accept direct connections.

```yaml
# Planned compose shape; the image does not exist yet.
services:
  toget:
    image: ghcr.io/sidenor/toget:latest
    restart: unless-stopped
    ports:
      - "7717:7717/tcp"
      - "7717:7717/udp"
    volumes:
      - toget-data:/var/lib/toget
volumes:
  toget-data:
```

A static binary on the host is the reference deployment, because it can use
the host's keychain, service manager, and update path.

## Create an account

```bash
toget account create
```

The command asks where the account should live:

- **This machine** — self-hosted. The node prints the 14-digit account ID and starts serving as the account host.
- **Official** — the node registers with the official service and stores the account record there. Supply the host URL explicitly:

```bash
toget account create --host 'https://accounts.example.com#fp=<hex>' \
  --name Aiden --email aiden@example.com --out ~/offline/toget-account.togetkey
```

For an official-hosted account, registration generates the account master key (MK) and the official recovery key (RK). The host receives only ciphertext: the password export wrap, the recovery wrap, and the account-key bundle sealed under MK. It returns the recovery file once:

```text
toget-account-recovery-48392017174628.togetkey
```

The CLI writes it with mode `0600`; the web onboarding flow requires the
download to finish and a confirmation before continuing. Store it offline. It
is a bearer secret: anyone with the file and the account ID can reset the
password and unwrap the account key. It is never uploaded to the official
service; the embedded reset page reads it locally in the browser and sends
only the new wrapped values. See
[account-recovery.md](account-recovery.md#official-hosted-accounts).

Self-hosted accounts use a per-device recovery credential for a forgotten password while the node is locked. Create it while the node is unlocked and store it offline:

```bash
toget account recovery create --out ~/offline/toget-recovery.togetkey
```

Reset with the file:

```bash
toget account reset-password --recovery ~/offline/toget-recovery.togetkey
```

If the password is forgotten but the node is still unlocked, reset locally without the old password:

```bash
toget account reset-password --local
```

The same flows are available from the local UI at `127.0.0.1:7718`: the lock screen accepts a recovery file, and Settings has password change, local reset, and recovery-file management. If the machine is locked, the password is forgotten, and no recovery file exists, the sealed local history cannot be opened. If the machine is lost and there is no backup, the data is lost. Full-disk encryption, locking the node, and regular tested backups are strongly recommended. See [account-recovery.md](account-recovery.md).

## Run the official account host

`toget-official` can run the account host together with the relay and
directory, or by itself:

```bash
toget-official --home /var/lib/toget-official \
  --host-listen 0.0.0.0:7722 \
  --host-public-url https://accounts.example.com \
  --host-tls-cert /etc/toget/host/fullchain.pem \
  --host-tls-key /etc/toget/host/privkey.pem \
  --host-smtp-addr smtp.example.com:587 \
  --host-smtp-user toget --host-smtp-from accounts@example.com \
  --no-relay --no-directory

# The SMTP password is read from the environment, not the command line.
export TOGET_HOST_SMTP_PASSWORD='...'
```

- `--host-public-url` is the browser-facing HTTPS origin used in verification
  and reset links, and is also the origin of the proxy identity URL described
  below. Without it, the host falls back to the listen address, which only
  works for loopback testing; email recovery needs a public origin.
- The account host always serves TLS. With no certificate flags it creates a
  self-signed certificate under `<home>/host/`; supply a real certificate for
  a public host. Startup always prints `Accounts:` with the exact origin
  certificate fingerprint for direct connections. When `--host-public-url` is
  set it also prints `Proxy:` with the self-certifying host ID for connections
  through a TLS-terminating proxy. Either can be given to clients; use the one
  that matches how the client reaches the host.
- The database defaults to `<home>/host/accounts.db`; TLS defaults to
  `<home>/host/tls.crt` and `tls.key`. Back up the database and host key, but
  note that neither contains account master keys.
- The embedded reset page is served at `/reset/` from the binary. Use
  `--host-reset-dir` only to override it during development. It needs no
  third-party scripts and the host serves it under a strict CSP.
- `--host-smtp-*` enables email verification and recovery. Without SMTP the
  host still supports password login, device sessions, and key rotation, and
  serves the reset page, but users cannot receive verification or reset links.
- `--host-room-member-limit` and `--host-room-device-limit` cap one official
  room (defaults 5000 active members and 20000 devices). A join past either cap
  returns `409 room_full` or `409 device_limit`.
- `--no-host` turns the account host off for a relay/directory-only
  deployment.

The host stores OPAQUE registration records, sealed blobs, device records,
sessions, and encrypted email metadata. It cannot read account keys, room
content, or the plaintext password. It can also be the home of official rooms:
it orders sealed events and stores keys wrapped per device, but it cannot read
room names, topics, or message text, and it is not a plaintext archive of
history.

### Put the official host behind Cloudflare or a TLS-terminating proxy

A direct host address (`https://host:7722#fp=<hex>`) pins the exact TLS
certificate of the origin. That is the strongest check, but it cannot survive
a proxy that terminates TLS before the request reaches the origin: the client
sees the proxy's certificate, not the origin's. For Cloudflare, Cloudflare
Tunnel, or another TLS-terminating reverse proxy, give clients the printed
`Proxy:` address instead:

```text
https://accounts.example.com#id=tog1:s:...
```

The client validates the proxy's publicly trusted certificate with normal Web
PKI, fetches `/host/v1/info`, verifies the signed host manifest, and checks
that the manifest's `server_id` equals the pinned `#id`. It performs this check
before sending account material, and rejects redirects. A proxy therefore
cannot impersonate the host or substitute its own OPAQUE configuration. It can
still observe connection metadata and deny service; it cannot read the
password, account master key, room keys, or room content. See
[security-model.md](security-model.md#tls-terminating-proxies-and-identity-pinning).

`#fp=` and `#sha256=` still mean an exact certificate pin; `#id=` means a host
identity pin. Exactly one anchor is allowed. A direct `#fp=` address remains
preferable whenever clients can reach the origin without TLS termination,
because it removes the proxy from the trust path.

#### Cloudflare Tunnel (recommended when the origin has no reachable port)

Cloudflare Tunnel makes an outbound connection from the server, so the origin
does not need a public address or an open inbound port. Run the host on
loopback and point the tunnel at its HTTPS listener:

```bash
toget-official --home /var/lib/toget-official \
  --host-listen 127.0.0.1:7722 \
  --host-public-url https://accounts.example.com \
  --host-trusted-proxies 127.0.0.1/32,::1/128 \
  --host-smtp-addr smtp.example.com:587 \
  --host-smtp-user toget --host-smtp-from accounts@example.com \
  --no-relay --no-directory
```

A minimal `~/.cloudflared/config.yml`:

```yaml
tunnel: <tunnel-id>
credentials-file: /etc/cloudflared/<tunnel-id>.json
ingress:
  - hostname: accounts.example.com
    service: https://127.0.0.1:7722
    originRequest:
      noTLSVerify: true
  - service: http_status:404
```

- The `service` URL must be `https://`, because the host only speaks TLS.
  `noTLSVerify` tells cloudflared to accept the origin's self-signed
  certificate. If the origin uses a Cloudflare Origin CA certificate, keep
  `noTLSVerify: true` or configure `caPool` with the origin CA; either way the
  client-facing certificate is the public one at Cloudflare.
- If the public URL is HTTPS on 443, the port can be omitted; the host
  advertises `accounts.example.com:443` in its signed endpoints while still
  listening on `127.0.0.1:7722`. Include `:8443` (or another port) when the
  public URL uses a non-default port.
- `--host-trusted-proxies 127.0.0.1/32,::1/128` is required for correct client
  IPs and rate limiting: cloudflared connects from loopback, and the host then
  honors Cloudflare's `CF-Connecting-IP` (which requires `CF-Ray`) and
  `X-Forwarded-For`. Do not add Cloudflare's public ranges when using a
  Tunnel; the direct peer is loopback.
- Give users the startup `Proxy:` URL. The `Accounts:` URL points at loopback
  or the machine's outbound address and will not work for remote clients.

#### Orange cloud with an origin on a standard HTTPS port

Cloudflare's reverse proxy only forwards a fixed set of HTTPS ports: `443`,
`2053`, `2083`, `2087`, `2096`, and `8443`. Port `7722` is not one of them, so
an orange-cloud DNS record alone cannot reach a host listening on `7722`.
Choose one of:

- **Run the host on 443 or 8443.** For 443, keep `--host-listen 0.0.0.0:443`
  and `--host-public-url https://accounts.example.com`. For 8443, include the
  port in both the listen address and the public URL:
  `--host-listen 0.0.0.0:8443 --host-public-url https://accounts.example.com:8443`.
  Set the Cloudflare DNS record to proxied (orange cloud), and set SSL/TLS to
  **Full (strict)** with a Cloudflare Origin CA certificate installed at the
  origin. Use **Full** only if the origin certificate cannot be validated;
  **Flexible** is wrong because Cloudflare would speak plaintext HTTP to a
  TLS-only listener.
- **Use a Cloudflare Origin Rule** to rewrite the destination port to `7722`.
  This requires a plan that includes Origin Rules and still requires the origin
  to be reachable from Cloudflare on that port. Confirm the rule rewrites the
  port, not just the hostname, and keep the public URL on a standard port.
- **Use a Tunnel** as above. It avoids publishing the origin entirely and is
  the safer default for a censored or NATed network.

When Cloudflare connects directly to the origin, add
`--host-trust-cloudflare` so the published edge ranges are trusted for
client-IP headers. The built-in list is a snapshot; refresh it periodically
from <https://www.cloudflare.com/ips/> or pass the current ranges with
`--host-trusted-proxies`. If the ranges change and the flag is stale, rate
limits may key on a Cloudflare edge address instead of the real client; this
does not expose account material, but it makes limits less accurate.

If the origin accepts direct connections as well, restrict it with a host
firewall to Cloudflare's published ranges (or to the tunnel only). Forwarded
headers are ignored unless the direct peer is in the configured trusted set,
so an attacker who reaches the origin directly cannot spoof a client IP.

For a TLS passthrough proxy (for example nginx `stream` with SNI routing), the
origin certificate reaches the client unchanged and `#fp=` still works. Such a
proxy cannot add client-IP headers inside the TLS stream; without PROXY
protocol support (not implemented), rate limiting will see the proxy's address.

#### Cloudflare settings that matter

- **SSL/TLS:** Full (strict) with an Origin CA certificate, or Tunnel.
- **Caching:** bypass cache for `accounts.example.com/host/v1/*` and
  `/reset/*`. API responses are dynamic and the reset page must match the
  binary.
- **Rocket Loader, Email Obfuscation, Bot Fight Mode, Browser Integrity
  Check:** disable these for the host paths. They can rewrite HTML or challenge
  CLI and browser API requests; Toget's own rate limits and authentication are
  the intended controls.
- **Managed Challenge / WAF:** do not challenge `/host/v1/*`; CLI clients
  cannot solve an interactive challenge. If a WAF is required, scope rules to
  paths other than the account API.
- **Cloudflare Access:** do not put Access in front of `/host/v1/*` or
  `/reset/*`. Toget clients do not send Access service-token headers, so an
  Access policy would block registration and login. If the site uses Access
  for other paths, add a bypass policy for those two prefixes. Access is not a
  substitute for Toget account authentication.
- **Timeouts:** the default 100-second origin timeout is ample; OPAQUE and
  sealed-blob requests are small and short-lived.
- **Request size:** leave the default limit; registration carries a few small
  sealed blobs, not attachments.

## Connect from another device

```bash
toget device enroll --account 48392017-174628
```

The new device shows a request; an existing device or the account host approves it. The account key signs the new device certificate, and the current room epoch keys are wrapped to it. The new device then syncs room history up to its access.

For an official-hosted account, a new device authenticates with the account password instead of an enrollment approval:

```bash
toget account login --host 'https://accounts.example.com#fp=<hex>' \
  --account-id 48392017174628
```

The OPAQUE exchange unwraps MK, MK opens the account-key bundle, and the device
publishes its account-signed certificate to the host. This initializes the
device's own local vault; the host never stores the password. See
[account-recovery.md](account-recovery.md#sign-in-on-another-device).

Device roles:

- **Persistent node** — always on, callable, stores a copy, can relay.
- **Client App** — phone/tablet; chat and approvals, no relay, not callable.
- **Pure CLI** — no persistent service, for scripts and servers.

## Networking

### Direct

The node advertises its endpoint in its signed manifest. Clients probe that
pinned endpoint first and use it when reachable. The node serves HTTPS on TCP
and HTTP/3 over QUIC on the same port over UDP; clients select HTTP/3 only
after a successful health probe and fall back to HTTPS without retrying an
ambiguous request. UDP `7717` must be reachable for HTTP/3. Set `--no-quic`
when UDP is filtered or a middlebox breaks HTTP/3.

A node behind a gateway can request TCP and UDP port mappings:

```bash
toget serve --node-nat auto
toget serve --node-nat auto --node-stun stun.example.net:3478
toget serve --node-nat auto --node-nat-gateway 192.168.1.1
```

The same settings can live in `config.json` as `node_nat`, `node_stun`, and
`node_nat_gateway`; `node_quic: "off"` mirrors `--no-quic`. `--node-nat auto`
tries PCP, then NAT-PMP (UDP only), then UPnP IGD. The node
renews the mappings before their lifetime expires, removes them on shutdown,
and publishes the public `host:port` in its signed manifest as
`external_endpoint`. Clients receive it as an `alt=` fragment parameter: they
probe the LAN endpoint first and then the public alternate, so LAN traffic
stays local and the relay remains the last fallback. `--node-stun` supplies a
public address when the gateway reports a mapping without one (some UPnP
devices); STUN is never used for the mapping itself. `--node-nat-gateway`
overrides default-gateway discovery.

Port mapping only works when the router implements one of those protocols and
the WAN address is directly reachable. Symmetric NAT, CGNAT, and networks that
block UPnP still need the relay. UDP hole punching is not implemented.

Direct connections are preferred because they are faster and keep relays out
of the path.

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

# or a relay without the official bundle, account, or database
toget relay --listen 0.0.0.0:7719 --advertise relay.example.com:7719
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

- The directory's advertised relay is the default for `auto`; a node can also use a friend's relay, a community relay, or its own `toget relay`.
- A relay sees the target server, signed account/device headers, path, timing, size, and ciphertext. It cannot decrypt room content.
- Relays can queue requests for an offline node for 55 seconds and can chain to a parent relay. Each relay-to-relay hop is signed by the forwarding relay.
- Nodes prefer a WebSocket tunnel to the relay and fall back to HTTPS long-poll. `--no-websocket` on a relay forces the fallback.
- `--no-relay` disables outbound fallback only. Ordinary nodes do not accept relay traffic for others; run `toget relay` or `toget-official`.
- The relay can be changed at runtime from Settings or `POST /api/v1/node/relay`; new invites and directory lookups use the new route. Existing rooms keep the home address they were given, so remote members may need a fresh invite or lookup after a change.

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

Signed releases, a static update service, atomic installation, rollback,
stable/beta channels, periodic checks, and an in-app update prompt are
implemented. The full workflow is in [updates.md](updates.md); the short
version:

```bash
toget update check
toget update apply
toget update finish    # after a restart, or use update.restart
toget update rollback
```

Configure `update.root`, `update.channel_url`, and `update.root_key` (or the
matching flags). `update.mode` defaults to `notify`, which checks every six
hours and shows a banner in the local web UI without downloading anything.
`auto` checks and stages an eligible release in the background but still
waits for an explicit restart; `off` disables scheduled checks. Set
`update.check_interval` to a Go duration of at least `5m`.

`update.restart` is a local command such as
`["systemctl", "restart", "toget"]`; without it, an update is staged and waits
for an operator restart. The daemon reconciles a pending update at startup.

Updates download to a temporary directory, verify the release signature and
artifact hash, extract a staged version directory, run a health check, switch
`current`, optionally restart, and roll back automatically on failure. The
previous version is retained for `update rollback`. The UI shows the available
release, level, changelog, staging state, and rollback controls. Forced
releases can be postponed for at most 24 hours in the UI. The official account
host does not enforce a release level; a user who controls the binary can
still postpone or bypass it.

Release operators can generate reproducible archives and SBOMs with
`toget-official release build`, sign manifests/channels/indexes, yank a bad
release, verify the complete tree with `release verify-tree`, and publish it
append-only with `scripts/publish-release.sh`. See
[updates.md](updates.md#build-a-release) for the release workflow.

## Backups

What matters, in order:

1. **Account and device keys** — without them, encrypted history is unreadable.
2. **Room history and local data** — `/var/lib/toget/data` or the platform equivalent.
3. **Configuration** — relay parent, ports, Agent policies.
4. **Recovery credentials** — the per-device file resets a forgotten local password while the node is locked; the official recovery file restores an official-hosted account through the reset page. They are different files; store both offline if the account uses official hosting.

A backup is only useful if restore is tested. `toget backup` writes an
encrypted archive, and Settings → Backup and restore can create, download,
verify, and stage a restore from the browser. `toget restore` remains the
offline path.

The web restore flow is deliberately two-step: the uploaded archive is checked
entry by entry, then staged. The running node keeps serving until it restarts;
the new process applies the staged restore before opening the database, moves
the previous database, vault key, and attachments into a timestamped
`pre-restore-*` directory, and removes the staged marker. A corrupt staged
archive is quarantined under `restore/failed-*` instead of causing a boot loop.
Backups never leave the machine unless the user moves them.

## Notifications

Settings → Notifications registers the current browser for Web Push. The node
generates its own VAPID P-256 key pair on first use and keeps the private key
in the local database; no notification content is sent to a third-party
service. Payloads contain only `{"v":1,"type":"approval|message","lang":"en|zh"}`.
Message text, senders, room names, and approval descriptions never enter a
push payload. Each browser can disable message or approval events, and the
quiet-hours switch pauses every browser until its deadline.

Web Push requires HTTPS or a loopback origin. On iPhone and iPad, install the
PWA to the Home Screen first. If the browser permission is denied, notification
delivery cannot be repaired from Toget; allow notifications in the browser's
site settings and register the browser again. Delivery failures are recorded
per subscription, and a push service answering `404` or `410` causes the stale
subscription to be removed.

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
- Recovery credential stored offline for every device you cannot afford to lose access to.
- Review the device list after any suspicious login.
- Keep the node within the supported version window (N-2).
