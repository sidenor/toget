# Relay and Directory (Implemented)

> Status: implemented for messages, attachments, pairing, and remote-room
> operations. This document describes what exists in the code today, including
> its limits. It is not a claim that the public infrastructure is production
> grade.

Toget works without any public service on a LAN. The optional relay and
directory make rooms reachable across networks. Both are built from the same
packages as the node; `toget-official` only composes them into one process.

```text
node A ──pinned HTTPS──► relay ◄──pinned HTTPS── node B
   │                       ▲
   │                       │ signed route announcements
   └──────── direct LAN ───┘ (tried first when both are on one network)
```

The directory is not in the data path. It answers "which signed manifest
belongs to server code 12345678?" and "what is the default relay?".

Same-LAN nodes can also find each other without a directory over multicast
DNS. See [discovery.md](discovery.md); mDNS is link-local and is not NAT
traversal.

## Running the services

The official process runs a relay on `7719` and a directory on `7720` by
default:

```bash
toget-official --home /var/lib/toget-official
# Relay:     https://203.0.113.10:7719#fp=...
# Directory: https://203.0.113.10:7720#fp=...
```

Run only one of them with `--no-relay` or `--no-directory`. Advertise a
public name with `--relay-advertise relay.example.com:7719` and
`--directory-advertise directory.example.com:7720`. A child relay can chain
upward with `--relay-parent '<pinned relay URL>'`.

A normal node opts in:

```bash
toget serve \
  --node-directory 'https://directory.example.com:7720#fp=<hex>' \
  --node-relay 'https://relay.example.com:7719#fp=<hex>'
```

`--node-relay auto` asks the directory for its default relay. `--no-relay`
keeps direct connections only. The same settings live in `config.json` as
`node_directory`, `node_relay`, and `node_relay: "off"`.

Nodes do not relay traffic for other people by default. Running
`toget-official` (or a future relay-only mode) is how a community relay is
hosted.

## Addresses and direct-first fallback

A relayed node address is a normal pinned node URL with a relay route in the
fragment:

```text
https://192.168.1.8:7717#fp=<node-cert-pin>&relay=https%3A%2F%2Frelay.example%3A7719%2Frelay%2Fv1%2Fnode%2F<server-id>%23fp%3D<relay-cert-pin>
```

Invites, pairing codes, directory results, and room home addresses carry this
composite address. Before each operation the client probes the direct
`/toget/v1/health` endpoint with a 2.5-second timeout:

- direct reachable → use the direct pinned connection;
- direct unreachable → use the relay route;
- the result is cached for 60 seconds (30 seconds after a request failure);
- after a direct request fails, the next operation probes again; an operation
  is not retried over the relay after an ambiguous failure, to avoid applying
  a write twice.

The direct URL and its certificate pin are still required and verified even
when only the relay is reachable. The relay cannot substitute its own TLS
certificate for the node's or change the node's manifest.

## Relay trust boundary

The relay forwards opaque, signed inner HTTP requests:

1. The sending device signs the inner method, URI, timestamp, nonce, and body
   hash with its device key.
2. The envelope carries only allowlisted inner headers (`Accept`,
   `Content-Type`, and the Toget signature headers) plus a base64 body.
3. The home node verifies the device signature and nonce exactly as it would
   on a direct LAN connection, then executes the request through its normal
   HTTP handler.
4. The relay returns the inner response, with `X-Toget-Relayed-By` added.

The relay never receives room epoch keys and cannot decrypt room payloads or
attachments. It can see the target server ID, the sending account and device
IDs, request paths, timing, sizes, and the TLS endpoints of both sides. It
can deny, delay, drop, or replay a request; the node's signature and nonce
checks stop replay from becoming a second successful write.

The relay does not get a certificate for the node and cannot impersonate one:
clients pin the node certificate until they fall back, and relayed requests
are still addressed to the node's own identity.

## Offline queue

When a directly registered node has no live polling session, the relay can
store a bounded number of requests:

- The client sets an envelope expiry of 55 seconds. The relay clips its own
  30-minute default to that value.
- The relay answers `202 Accepted` with a random ticket; the client polls
  `/relay/v1/responses/<ticket>` until the result is ready or the ticket
  expires.
- The relay delivers queued frames on the node's next poll. A frame is marked
  delivered after one poll; it is not redelivered within the queue lifetime.
- The final response is kept for 10 minutes by default.
- The ticket is a bearer secret. It contains no room key; responses are the
  same signed-body ciphertext the client would have received directly.

This is **at-most-once delivery after the node accepts a frame**, not
exactly-once. If the node processes a frame and crashes before posting the
result, the client times out and may retry the operation. Room writes use the
usual home-node sequence conflict handling, but a non-idempotent side effect
inside a request could run twice across separate client retries. The short
55-second queue lifetime bounds how long a client can give up and still have
the write applied later.

Multi-hop offline queueing is limited: only the relay directly attached to
the node stores frames. A request that reached a parent relay and was routed
downward is waited on synchronously, so a node that is offline behind a child
relay returns `node_offline` upward rather than queueing at the parent.

## Multi-hop relays and loops

A relay registers with an optional parent. It announces its directly
registered nodes and routes learned below it every 20 seconds, or immediately
when a node registers. Announcements contain a `Via` path starting at the
announcing relay. The parent accepts the route only if:

- the first hop is the authenticated announcing relay;
- no hop is repeated and the parent's own ID is absent;
- the target is not directly registered (a direct path always wins);
- the target is not a relay ID and the path is within eight hops.

Every forwarded envelope appends the relay ID to `Hops`. A relay rejects an
envelope that already contains its own ID or has reached eight hops, and
answers `508 Loop Detected`. Routes expire after twice the relay session TTL
and are removed when the path or session dies.

## Limits and quotas

The defaults are deliberately conservative; they are not yet a billing or
fair-share system.

| Limit | Default |
| --- | --- |
| Relay queue | 128 requests / 16 MiB total |
| Per offline node | 16 requests / 4 MiB |
| In-flight relay requests | 512 / 128 MiB total |
| Registration rate | 30 per minute per IP, burst 30 |
| Forward rate | 600 per minute per IP, burst 200 |
| Relay body | 52 MiB decoded inner body |
| Node response through relay | 48 MiB |
| Relay session without polling | 90 seconds |
| Queued request lifetime | 55 seconds (client envelope), 10 minute result retention |

The relay rejects over-capacity work with `queue_full`, `relay_busy`, or
`relay_full` and a `Retry-After` header instead of silently dropping it.

## Directory trust boundary

A directory record contains two signed statements:

- the account's **identity manifest**, which binds the root public key to the
  server ID and eight-digit server code;
- the node's **node manifest**, signed by that server key, which carries the
  current HTTPS endpoint, TLS pin, optional relay route, issue time, and a
  24-hour expiry.

The directory verifies both signatures and the identity binding before storing
a record, and verifies them again before returning it. A stale manifest
(`issued_at` older than the stored one) and a code owned by another server are
rejected. Records are persisted atomically in `directory-records.json` with
mode `0600`.

A client that looks up a code verifies the same signatures locally. A
compromised directory therefore cannot forge a server identity or redirect a
lookup to an attacker endpoint: the client checks the signed endpoint and TLS
pin. It can deny service, return a valid but stale record until expiry, or
advertise a different (still signed) relay URL. For `relay: "auto"`, a
compromised directory can choose which valid relay the node uses, which is a
traffic-analysis and availability risk, not a content-confidentiality risk.

The eight-digit code is a routing hint, not an authentication factor. It has
about 27 bits and can be ground by an attacker; first-contact authentication
should use a QR code or a pinned full `server_id`. Publish requests are
unauthenticated apart from the signed manifests and are rate-limited per IP.
A directory is a single process in this implementation: no replication, no
consensus, and no signed directory responses. Clients must be configured with
a pinned directory URL obtained out of band.

Publication exposes the node's advertised endpoint (often a LAN address), its
server/account IDs, and its relay route to the directory operator. Do not
publish a node whose endpoint you consider secret. Lookups are keyed by the
short code and are visible to the directory operator.

## What is not implemented

- QUIC, UDP hole punching, UPnP/NAT-PMP/PCP, and any other NAT traversal.
  Two nodes behind different NATs work only through a relay.
- Standalone relay-only mode for ordinary nodes; `toget-official` is the
  supported relay process today.
- Runtime relay switching for existing rooms. A room stores the home address
  it was given; changing a relay can require a fresh invite or directory
  lookup, and remote members do not yet refresh stored addresses.
- Relay-to-relay authenticated forwarding beyond the parent session token;
  forwarding requests are rate-limited but not independently signed.
- Byte-based per-sender quotas, billing, abuse reputation, and directory
  replication or high availability.
- Hiding metadata from relays or the directory; v1 does not do traffic
  analysis resistance.
