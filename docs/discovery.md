# LAN Discovery (Implemented)

> Status: implemented for `toget serve`. Discovery is optional metadata; a
> connection is still authenticated by the node's pinned TLS certificate and
> signed manifests.

Toget nodes on the same local network can find each other without a directory
or an internet connection. A node advertises a small DNS-SD service:

```text
_toget._tcp.local.
```

`toget serve` enables discovery by default. Use `--no-mdns` or set
`"node_mdns": "off"` in `config.json` to turn it off. An explicit
`"node_mdns": "on"` is also accepted.

## What is advertised

Each advertisement contains:

| Field | Meaning |
| --- | --- |
| `id` | Full server ID (`tog1:s:…`) |
| `code` | Eight-digit server code |
| `fp` | SHA-256 fingerprint of the node's self-signed TLS certificate |
| `proto` | Node protocol version |
| SRV target and port | The node's HTTPS listener (normally `7717`) |
| A/AAAA records | Unicast addresses on the sending interface |

The advertisement does **not** contain the account ID, room IDs, room names,
messages, keys, or the identity manifest. Account data stays out of the
multicast packet. The server ID, server code, endpoint, and certificate
fingerprint are visible to anyone who can listen on the local link.

Nodes advertise on every interface that is up and multicast-capable, including
loopback where the platform permits it. A VPN interface is included if it is a
normal multicast interface. Docker bridges and other virtual links may also be
included; this is one reason discovery is easy to disable.

mDNS uses UDP port `5353`. No additional TCP port is opened.

## Lookup flow

`toget node lookup <server-code>` and `GET /api/v1/node/lookup` resolve in
this order: a saved [business card](cards.md), then the local network, then
the configured directory.

For the LAN step:

1. Send a PTR query for `_toget._tcp.local.` and inspect the local cache.
2. Collect advertisements matching the eight-digit code.
3. For each candidate, connect to the advertised address with the advertised
   certificate pin.
4. Fetch `/toget/v1/identity` and `/toget/v1/info` over that pinned TLS
   connection.
5. Verify the account's signed identity manifest and the node manifest signed
   by its root key. Check that both bind the advertised server ID and code,
   that the manifest's certificate fingerprint equals the advertised pin, and
   that the endpoint is HTTPS.
6. Return the verified record with `"source": "mdns"`.

A saved card is tried before mDNS: its embedded endpoint is verified live the
same way and returns `"source": "card"`. If the card's endpoint is stale, mDNS
and the directory may still resolve the same server ID; a different server ID
for a code that has a saved card is rejected. If no LAN node answers within
about 1.2 seconds, lookup falls back to the configured directory. If several
different server IDs verify for one code, lookup fails instead of silently
choosing one. A node with no directory can still resolve a peer from a saved
card or a locally visible advertisement.

The `/toget/v1/identity` endpoint serves the same signed identity manifest that
is published to a directory. It is public because the directory already
exposes it; serving it directly is what makes directory-free LAN lookup
possible.

## Trust boundary

An mDNS advertisement is unauthenticated. Anyone on the link can send packets
claiming any server ID, code, endpoint, or pin. Treat the multicast cache as a
list of candidates, never as an address book:

- the certificate pin bootstraps a TLS connection to the claimed endpoint;
- `/toget/v1/identity` proves that the identity key matches the server ID and
  code;
- `/toget/v1/info` is verified with that key and binds the endpoint and pin;
- only then is a peer used.

An attacker can still advertise a different, valid Toget identity that happens
to use the same eight-digit code. The code is a routing hint with about 27
bits, not an authentication factor. First contact remains vulnerable to code
grinding in both mDNS and directory lookup; compare the full server ID, use an
invite code, a [business card](cards.md), or a previously pinned identity when
impersonation matters.

The advertisement list shown by `toget node discover`, the web Settings page,
and `GET /api/v1/node/discover` is not verified. It is intentionally marked as
local advertisements: verifying every candidate would contact every host on
the network and can be used to probe which machines run Toget. Lookup verifies
only the code the user asked for.

## Commands and API

```bash
toget serve --no-mdns                 # disable advertisement and browsing
toget node discover                   # list current LAN advertisements
toget node discover --json
toget card show --qr                  # directory-free business card
toget node lookup 12345678            # saved card, LAN, then directory
toget node info                       # discovery state and nearby count
```

`GET /api/v1/node/discover` returns:

```json
{
  "enabled": true,
  "peers": [
    {
      "server_id": "tog1:s:…",
      "server_code": "12345678",
      "protocol": 1,
      "tls_fingerprint": "<64 hex characters>",
      "port": 7717,
      "addresses": ["192.168.1.8"],
      "last_seen": "2026-10-31T13:00:00Z"
    }
  ]
}
```

The Settings page shows the discovery state and a nearby-node list with the
same caveat. All advertisement records expire after 120 seconds; a stopped
node disappears from the cache within that window, and a goodbye packet with
TTL zero removes it sooner.

## Firewall and network notes

- Allow UDP `5353` inbound on the local network, and allow IGMP if the network
  filters it. Many desktop firewalls and guest Wi-Fi networks block multicast.
- mDNS is link-local. It does not cross routers or VLANs without a multicast
  reflector, and it does not discover nodes over the internet.
- This is not NAT traversal. Two nodes behind different NATs still need the
  relay; see [relay.md](relay.md).
- If multicast is unavailable at startup, the node logs a warning and keeps
  serving direct and relay connections. It retries periodically.

## What is not implemented

- Conflict resolution for two nodes claiming the same service instance.
- Known-answer suppression, unicast-response (QU) questions, and full RFC 6762
  probing/defense behavior. The implementation is a small responder/browser,
  not a general-purpose mDNS stack.
- Address changes propagate only after the next announcement or cache expiry;
  a stale address may be tried once before lookup fails or falls back.
- Discovery across VLANs, subnets, or NAT; multicast DNS reflection; and any
  form of internet-wide discovery.
- Hiding that a node runs Toget, its server code, or its endpoint from other
  devices on the same link.
- Automatic trust of discovered peers. Discovery never skips the pinned TLS
  and signed-manifest checks.
