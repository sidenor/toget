# Portable Business Cards (Implemented)

> Status: implemented. A card is a portable introduction, not a credential
> and not a root of trust. The receiver verifies every signature and the live
> node before using it.

A Toget business card lets one person hand another the information needed to
find and verify their node without a directory, an invite code, or a shared
LAN. It can be copied as text or shown as a QR code. The card carries the
server's signed identity, its current endpoint, certificate pin, and optional
relay route; it does not carry room access, messages, or keys.

```bash
toget card show                    # card text and both expiry times
toget card show --qr               # also print a QR code in the terminal
toget card show --png card.png     # also write a PNG QR code
toget card add '<toget-card:v1:…>' # verify and save
toget card add --file card.txt     # read from a file, or paste on stdin
toget card list
toget card remove tog1:s:…
```

The web UI exposes the same card in **Settings → Business card**: a QR code, a
download button, the card text, and an import box that accepts either pasted
text or a QR image (screenshot or photo).

## What is in a card

A card is a gzip-compressed JSON container, base64url-encoded behind a short
version prefix:

```text
toget-card:v1:<base64url(gzip(json))>
```

```json
{
  "format": "toget-card",
  "version": 1,
  "label": "Aiden's node",
  "identity": { "...signed identity manifest..." },
  "node": { "...signed node manifest..." }
}
```

| Part | Lifetime | Contents |
| --- | --- | --- |
| Label | — | Optional display name, at most 128 characters. Not signed; local convenience only. |
| Identity manifest | One year | `server_id`, eight-digit `server_code`, root public key, nonce, protocol versions, signature by the server key. |
| Node manifest | 24 hours | Server ID/code, account ID, HTTPS endpoint, TLS certificate fingerprint, optional relay route, protocol version, issue and expiry times, signature by the server key. |

The two manifests are embedded byte-for-byte as issued. Nothing in the
container is encrypted: anyone who sees a card learns the server ID, account
ID, endpoint, certificate fingerprint, relay route, and label. Treat it as a
public business card, not a secret.

An identity manifest, once signed, is valid for a year. The node manifest is
short-lived so an endpoint or relay change becomes visible quickly. A card
therefore names a stable identity and a possibly stale address.

## What importing verifies

Saving a card verifies it locally before writing anything:

1. The container format, version, size, and encoding are valid.
2. The identity manifest signature is valid under its own root public key and
   not expired.
3. The node manifest signature is valid under that key, even if the 24-hour
   node manifest has expired. The issue time must not be in the future.
4. Both manifests agree on server ID and server code.
5. The account ID is 14 digits and begins with the eight-digit server code.
6. The endpoint is HTTPS, with no userinfo or fragment.
7. The certificate fingerprint is a valid SHA-256 fingerprint, and any relay
   route targets this server ID.

An expired node manifest is accepted at import time so a card printed or
saved yesterday still works. The card is never used on the strength of its
embedded address alone: lookup opens a fresh pinned TLS connection to the
node, fetches `/toget/v1/identity` and `/toget/v1/info`, and re-verifies the
identity, the fresh node manifest, the server code, and the certificate pin.
Only then does it return a record. A card cannot make an attacker's node pass
as the original server without that server's private key.

## Saved cards are explicit pins

Saving a card pins the full server ID for its eight-digit code:

- A saved card is tried first on `toget node lookup <code>`.
- If its endpoint is stale but mDNS or the directory finds the **same** server
  ID, that verified result is used.
- A different server ID for the same code is rejected, even if it has a valid
  signature and a valid directory entry.
- Two saved cards that map one code to different server IDs fail closed until
  one is removed.

This closes the code-grinding gap for people you have exchanged cards with.
Without a card, an eight-digit code is only a routing hint: anyone can grind a
valid identity until it has the same code. With a saved card, the full ID is
the pin.

Cards live in `cards.json` under the Toget data home, written atomically with
mode `0600`. They are public data, so they are deliberately left out of
encrypted backups and can be re-imported. Removing a card deletes the pin and
returns that code to normal mDNS/directory behavior.

## QR codes

`GET /api/v1/card/qr` renders the current card as a PNG with a quiet zone.
`scale` is pixels per module, from 2 to 32 (default 8). The terminal renderer
uses half blocks and works in ordinary UTF-8 terminals. The payload is
compressed so it fits at error-correction level M, with a fallback to L for
unusually large cards.

The QR code contains exactly the same `toget-card:v1:` text. In the web UI,
**Scan QR image** decodes a screenshot or photo locally in the browser and
fills the import box; no image is uploaded. Any other QR reader also works:
scan to text, then paste it into the web UI or `toget card add`. The web UI
does not open a live camera viewfinder; on a phone browser the file picker can
offer the camera. The QR image itself is generated locally by the daemon.

The same renderer and still-image scanner also handle `toget-invite:v1:` room
invites and `toget-pair:v1:` pairing codes. Those QR codes are **not** public
metadata like a card: they contain a bearer secret. The web UI never offers a
download for them and the CLI forces PNG output to mode `0600`. Scanning only
changes the input medium; the invite stays single-use and the pairing code
keeps its ten-minute expiry.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/card` | This node's card; optional `label` query |
| `GET` | `/api/v1/card/qr` | PNG QR code; optional `label` and `scale` |
| `GET` | `/api/v1/cards` | Saved cards, newest first |
| `POST` | `/api/v1/cards` | Verify and save `{ "card": "...", "label": "..." }` |
| `DELETE` | `/api/v1/cards/{id}` | Remove a saved card by full server ID |

`GET /api/v1/node/lookup?code=…` now resolves saved cards first, then mDNS,
then the configured directory. A successful record has
`"source": "card"`, `"mdns"`, or `"directory"` so a client can show where the
verified address came from.

## Limits and open work

- No revocation inside a card. Removing a saved card removes the pin locally,
  but a card someone else already has keeps working until its identity
  manifest expires or the server rotates its identity. Identity rotation is
  not implemented.
- A card is public metadata. It can be forwarded, cached, or photographed.
  Do not put anything private in the label.
- The embedded address can be stale. Lookup falls back to mDNS and then the
  directory by pinned server ID; if the node has moved and no source knows the
  new address, lookup fails.
- There is no contact list sync, avatar, vCard export, multi-account card, or
  live camera viewfinder yet; QR scanning imports a still image.
- A card does not grant room access. Joining still requires an invite and the
  room's membership rules.
- The card format is versioned (`v1`). Future additions must stay decodable by
  older readers or use a new prefix.
