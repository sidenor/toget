# Content-free notifications

Toget can notify a browser when a room message arrives or an Agent needs an
approval. The notification is designed so that neither the browser's push
service nor a lock screen learns what happened. This document describes the
implemented flow and its limits.

## Goals

- Tell the user that something needs attention without exposing message text,
  senders, room names, approval descriptions, amounts, or file names.
- Keep the Web Push application-server key on the user's node.
- Work on a phone without a native app; the existing PWA is the client.
- Give the user per-browser and global controls, including quiet hours.
- Fail closed when a browser can no longer be reached.

## Push payload

An application-side notification is reduced to three fields:

```json
{"v":1,"type":"approval","lang":"zh"}
```

`type` is either `approval` or `message`; `lang` is `en` or `zh`. The payload
never contains a room ID, event ID, account, Agent name, message excerpt, or
approval digest. The service worker chooses generic copy in the subscriber's
language and opens the relevant view when the notification is tapped.

The payload is encrypted for one browser subscription with RFC 8291
`aes128gcm` before it leaves the node. The browser's push service sees an
opaque body, a delivery time, and its size. The notification copy exists only
inside the browser.

## Key handling

On first use, the node generates a P-256 VAPID key pair with `crypto/rand` and
stores it in the local `settings` table under `notifications.vapid`. The private
key never enters a push payload, the web UI, logs, or a backup manifest. The
public key is returned by `GET /api/v1/notifications` so the browser can call
`PushManager.subscribe` with it. A backup includes the settings table, so a
restored node keeps the same VAPID identity; browsers may still need to
re-register if their subscription expired.

Each browser subscription stores its endpoint and its own `p256dh`/`auth`
keys in `push_subscriptions`. These are capabilities for that browser. The API
view exposes a device label, the push-service hostname, preferences, and
delivery timestamps, but never the endpoint or the browser keys.

## Delivery policy

- A locally authored human message does not notify the same account's
  browsers. Agent replies authored by that account do notify, because they are
  asynchronous work the user should see.
- Events older than two minutes are ignored. This prevents a sync catch-up
  from producing a burst of stale notifications.
- Each subscription is limited to one message notification per 20 seconds and
  one approval notification per 5 seconds. The queue holds 32 events; excess
  events are dropped rather than blocking a room write.
- Approvals use high urgency; messages use normal urgency. Both use a one-day
  push-service TTL and a stable `Topic` so an undelivered alert can be replaced
  by a newer one.
- A `404` or `410` response removes the subscription. Other failures are
  recorded on the subscription and retried on a later event.
- Global quiet hours are stored in the database and checked before every
  delivery. Per-browser mutes and event toggles are checked as well.
- The test button bypasses quiet hours and rate limits for one browser; it is
  an explicit user action, not a room event.

## Requirements and limits

- Web Push requires a secure context: HTTPS or a loopback origin. A node
  reached over plain HTTP from a phone cannot register a subscription.
- On iPhone and iPad, the PWA must be installed to the Home Screen before the
  browser exposes Web Push.
- Browser permission is controlled by the browser. If it is denied, Toget
  cannot re-enable it; the user must change the site setting.
- Delivery is best effort. A push service may delay, collapse, or drop a
  notification. The room and approval remain the source of truth.
- The push service still learns that this browser receives notifications from
  this node, the approximate time, and the encrypted body size. Toget does not
  hide that metadata in this version.
- Notifications do not carry message content by design, so there are no inline
  reply actions, message previews, or approval actions in the notification.
  Tapping opens the app.
- The node does not currently rotate VAPID keys. If the key is intentionally
  reset, existing browser subscriptions must be registered again.

## API

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/notifications` | VAPID public key, subscriptions, quiet-hours state |
| `POST` | `/api/v1/notifications/subscriptions` | Register or refresh a browser |
| `PATCH` | `/api/v1/notifications/subscriptions/{id}` | Enable/disable events or mute one browser |
| `DELETE` | `/api/v1/notifications/subscriptions/{id}` | Remove a browser |
| `POST` | `/api/v1/notifications/subscriptions/{id}/test` | Send one test push |
| `POST` | `/api/v1/notifications/mute` | Pause every browser for 0–168 hours |

All routes require an unlocked local session and the local API token, and all
answers carry `Cache-Control: no-store`.

## Tests

- `internal/notify` round-trips an encrypted payload and decrypts it with a
  browser key, proving the body is content-free and RFC 8291 compatible.
- The same package tests VAPID JWT signing, endpoint validation, rate limits,
  quiet hours, self-message suppression, Agent-reply delivery, and removal of
  gone endpoints.
- `internal/api` exercises subscription CRUD, preference changes, mute,
  test delivery, validation failures, and the locked-session boundary.
- `internal/storage` covers preference-preserving upserts and delivery
  timestamps.
