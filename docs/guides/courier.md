---
title: Courier sessions, SDK and link
description: Let your couriers report their own location and delivery status from a phone, with the courier SDK in your app or with a link to the DeliveryOnTrack courier page.
---

# Courier sessions, SDK and link

Your couriers can send their location and tap delivery status (picked up, delivered, failed) straight to
DeliveryOnTrack from their phone. You do not have to relay every fix from your own backend. There are two ways:

| Way | You write | The courier uses |
|---|---|---|
| SDK (`@dt/courier`) | the courier screen, in your own web app | your app |
| Link | nothing, only send the link | the DeliveryOnTrack courier page |

Both start the same way: your backend starts a **courier session** with your secret key. If you prefer to relay
GPS from your own courier app, see [Courier locations](courier-locations.md). That still works.

> The courier page is on staging only for now: `courier.staging.deliveryontrack.io`. The API host is
> `api.staging.deliveryontrack.io`. The `@dt/courier` package is not on a public registry yet; ask us for the
> build until it is.

## Before you start

1. Register the courier: `PUT /v1/couriers/{external_id}` returns DT's id (`cou_...`).
2. Assign the courier to a delivery: `POST /v1/deliveries/{id}/assignment`. The courier sees a delivery only once
   it is assigned to them and not final.
3. Use a secret key with scope `couriers:write`.

## Start a session

`POST /v1/courier-sessions` with `{"courier_id":"cou_...","mode":"sdk"}` or `"mode":"link"`. Optional:
`expires_in_seconds`, from 900 to 43200 (default 43200, 12 hours). Send an `Idempotency-Key`: a retry within 24
hours returns the same answer.

A courier has one active session. Starting a new one ends the old one with `end_reason: replaced`.

**SDK mode** answers 201 with `session`, `access_token`, `access_expires_at` and `refresh_token`. Hand the pair to
your courier app over your own authenticated channel.

**Link mode** answers 201 with `session` and `link_url` (with `link_expires_at`). Send the link to the courier
(message, QR code). The code in the link works once and for 15 minutes. The session is `pending_link` until the
courier opens it.

Read a session with `GET /v1/courier-sessions/{id}`. `status` is `pending_link`, `active` or `ended`. An ended
session has an `end_reason`:

| `end_reason` | Meaning |
|---|---|
| `courier` | the courier ended the shift |
| `partner` | you ended it with `DELETE /v1/courier-sessions/{id}` |
| `expired` | the session length ran out |
| `replaced` | you started a new session for the same courier |
| `refresh_reuse` | an old refresh token was used again (see below) |

End a session at any time with `DELETE /v1/courier-sessions/{id}`. The tokens stop working at once.

## The link

Open the courier page from the link. It first shows a **consent screen**: what is collected (the phone's location,
only while a delivery is assigned and the page is open), why, and who sees it. The courier taps **Start shift**.
Only then does the page exchange the link code for tokens and ask the phone for location permission. The page is
in English and Dutch, follows the phone's language, and has large touch targets.

The tokens stay on that phone. If the courier reloads the page or the browser closes the tab, the page resumes the
shift from the stored refresh token. A used link code is never tried again.

## The SDK

```js
import { createCourier } from '@dt/courier';

const courier = createCourier({
  api: 'https://api.staging.deliveryontrack.io/v1',
  tokens, // the pair from POST /v1/courier-sessions
});

courier.on('deliveries', (list) => render(list));
courier.on('state', (s) => showStatus(s)); // gps, network, buffered, pendingTaps, session
courier.on('notice', (n) => showNotice(n)); // reassigned, rejected, ended

// Call start() only after your own consent step.
await courier.start();

await courier.report(delivery.id, 'delivered');
await courier.end();
```

**Consent comes first.** The SDK collects no GPS before `start()`, after the session ends, or while the courier
has no open delivery. You decide when `start()` runs. Call it only after your own consent step has told the courier
what is collected, why, and who sees it. `start()` asks the phone for location permission once, at that moment.

Use `courier.deliveries` for the list. `courier.report(deliveryId, type)` takes `at_pickup`, `pickup_confirmed`,
`delivered` or `failed` (and an optional `{ reason }`). It resolves with `{ outcome, delivery }`. If the courier
taps twice, the SDK sends one event: the same `event_id` goes with every retry, so the transition happens once.

## Token handling

- The **access token** lasts 15 minutes (a 30 second leeway applies on the clock). It is never longer than the
  session: near the end of a shift it is cut at the session's end time.
- The **refresh token** works once. Every refresh returns a new pair; the old refresh token stops working.
- The SDK refreshes before the access token runs out, and only one refresh runs at a time. It stores the new pair
  before it uses it. Keep this in mind if you build your own client: two refreshes at once, or a retry with an old
  token, count as reuse.
- **Reusing an old refresh token ends the session** with `end_reason: refresh_reuse`. This is on purpose: a copied
  token cannot be used quietly next to the real one.
- By default the SDK keeps the pair in `localStorage` (or memory if that is blocked). Pass `storage` to use
  something else. The tokens are secrets: never log them or put them in a URL.
- Stored token hashes only: DeliveryOnTrack keeps no plaintext tokens or link codes.

## Background GPS and the wake lock

A web page cannot track a phone in the background. Browsers pause location updates when the screen is off or the
page is not visible. This is a limit of the browser, not of DeliveryOnTrack.

- The courier page asks the browser to keep the screen on (Screen Wake Lock) while the courier has a delivery. The
  browser drops the lock whenever the page is hidden, and the page takes it again when it is visible. Where the
  browser has no wake lock, the page tells the courier to keep the screen on.
- If the page is hidden, there are gaps in the trail. Your customer sees the courier as `stale` and then `lost` (see
  [Tracking links](tracking-links.md)). When the page is visible again, the SDK refreshes the token,
  reloads the deliveries and sends what it kept.
- The SDK keeps fixes it cannot send (no network) in IndexedDB, so a killed tab loses nothing. Fixes older than 10
  minutes are dropped before sending. Fixes with an accuracy worse than 50 m are never kept.
- If you need tracking with the screen off, relay from a native courier app ([Courier locations](courier-locations.md)).
  Native SDKs come later.

## `next_actions`

Every delivery the courier sees has `next_actions`: the event types the courier can report now, in this order:

`at_pickup`, `pickup_confirmed`, `delivered`, `failed`

DeliveryOnTrack works them out from the delivery's status, its model and the pickup rule, so your courier screen does
not have to know the state machine. Show one button per entry. `arriving` is never offered. `pickup_confirmed` is
offered while the courier can still confirm pickup: not when the pickup rule leaves it to the store, and not after
the courier already confirmed. An empty list means nothing to tap (for example, waiting for the store). The answer to
`POST /v1/courier/deliveries/{id}/events` carries the updated delivery, so the next buttons are in it.

The courier sees only what the job needs: pickup and dropoff place, ready time, an items summary and the pickup
confirmations. No customer name, phone or email, no metadata, no other couriers.

## Errors

Errors are `application/problem+json` with a `code`.

| Status | `code` | What to do |
|---|---|---|
| 400 | `validation_failed` | fix the request |
| 401 | `link_code_invalid` | unknown or expired code: ask for a new link |
| 401 | `link_code_used` | the code was already used: ask for a new link |
| 401 | `refresh_token_invalid` | unknown, used or expired: start a new session |
| 401 | `session_ended` | on `POST /courier/token` only; the session is over |
| 401 | `unauthenticated` | bad, expired or revoked token; the SDK treats any 401 as the end of the shift |
| 404 | `not_found` | the delivery does not exist or is not assigned to this courier |
| 404 | `courier_not_found` | starting a session: no such courier (register it first) |
| 409 | `invalid_transition` | the event is not allowed in the delivery's status |
| 413 | `payload_too_large` | more than 50 fixes or 16 KB |
| 429 | `rate_limited` | wait for `Retry-After`; the SDK does |
| 503 | `tracking_unavailable` | try again shortly |

On the bearer routes (`/courier/deliveries`, `/courier/locations`, delivery events, `/courier/session/end`) an
ended or revoked session always answers `401 unauthenticated`, so the answer does not tell a stranger whether a
session exists. Only `POST /courier/token` says `session_ended`. A courier whose delivery was reassigned gets `404`
on the tap: the SDK drops the tap and sends a `reassigned` notice.

If you revoke a session while the phone is offline with fixes saved, the first call after it reconnects gets `401`.
The SDK discards the saved fixes, stops, and the page shows the ended screen. It does not retry.

## Rate limits

| Route | Limit | Counted per |
|---|---|---|
| `POST /courier/locations` | 1 request per second, burst 5 | session |
| `POST /courier/deliveries/{id}/events` | 10 per minute | session |
| `GET /courier/deliveries` and `POST /courier/session/end` | 30 per minute (shared) | session |
| `POST /courier/token`, refresh token | 10 per minute | session |
| `POST /courier/token`, link code | 20 per minute, failed guesses included | client IP |

A location batch holds at most 50 fixes and 16 KB. The SDK sends a batch every 3 seconds, polls the list every 10
seconds (20 seconds when it is empty), and sends a backlog one batch per second.
