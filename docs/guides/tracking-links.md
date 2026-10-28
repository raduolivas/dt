---
title: Tracking links
description: Send your customer a live tracking link, embed it in your app, rotate it, and build your own viewer.
---

# Tracking links

Every delivery you create comes with a tracking link for your customer. You send it; DT shows the live map.

## Get the link

`POST /v1/deliveries` returns `tracking` next to the delivery:

```json
{
  "id": "del_01j9z3k8m2q4r6s8t0v2w4x6y8",
  "tracking": {
    "url": "https://track.example.invalid/t/Zr7wT2nKd9sH4yJ6uB3xQa",
    "token": "Zr7wT2nKd9sH4yJ6uB3xQa",
    "expires_at": null
  }
}
```

- `url` is `<tracking base>/t/<token>`. Put it in an SMS, an email or a push notification.
- `token` is the part after `/t/`. It is 22 characters, random and stored only as a hash. Treat it like a
  password for that one delivery.
- `expires_at` is `null` while the delivery is running. Once the delivery is final (`delivered`, `failed` or
  `cancelled`), the link stops working 30 minutes later.
- `tracking` is `null` when the link could not be issued. The delivery is still created; call the rotate
  endpoint (below) to issue one.

## What the customer sees

The customer sees the courier when it makes sense for the delivery's model:

| Model | Courier visible from | Route to the pickup |
|---|---|---|
| `end_to_end` | the courier is assigned (`courier_assigned`) | shown |
| `direct` | the courier has picked up (`picked_up`) | never shown |

Until then the snapshot has `courier: null`. The ETA before pickup is the preparation time plus the drive. If
DT cannot know the preparation time, the page shows "Calculating delivery time…" (`eta_state: "calculating"`).

You always see more than the customer: `GET /v1/deliveries/{id}/tracking` shows the courier whatever the model
and status (see [Partner tracking data](partner-tracking-data.md)).

## Embed it

You can open the link in a browser tab, in an in-app webview or in an iframe. Nothing depends on cookies or on
credentials in the URL after the first exchange, so it works where third-party cookies are blocked. CORS on the
viewer endpoints allows any origin.

If you would rather draw the map yourself, build a viewer on the same endpoints the hosted page uses.

### Build your own viewer

1. **Start a session.** From the customer's browser or webview (never with your secret key):

   ```bash
   curl -sS -X POST https://api.example.invalid/v1/tracking/session \
     -H 'Content-Type: application/json' \
     -d '{"token":"Zr7wT2nKd9sH4yJ6uB3xQa"}'
   ```

   The answer holds `viewer_token` (valid 15 minutes), `expires_at` and the current `snapshot`. Limits:
   10 requests/minute per IP and 30/minute per link, shared by `POST /v1/tracking/session` and
   `POST /v1/tracking/session/refresh` (over the limit: `429 rate_limited`). An unknown, rotated or expired
   token is `401 link_invalid`.

2. **Read snapshots.** Either poll `GET /v1/tracking/snapshot` (30 requests/minute per viewer token) or open
   a stream. Send `Authorization: Bearer <viewer_token>`.

3. **Stream.** Use a WebSocket at `/v1/tracking/stream` (first message `{"type":"auth","token":"<viewer_token>"}`
   within 5 seconds) or, where WebSockets are blocked, Server-Sent Events at `GET /v1/tracking/stream/sse`
   (use `fetch`, not `EventSource`, because the token goes in a header). Both carry the same messages. On SSE
   each is `event: <type>` and `data: <JSON>`:

   | `type` | Meaning |
   |---|---|
   | `snapshot` | `{"type":"snapshot","data":<snapshot>}`. Ignore a snapshot whose `version` is not greater than the last you applied |
   | `status` | the delivery status changed |
   | `ping` | keep-alive, every 25 seconds |
   | `revoked` | the link was rotated or has expired; the server closes the stream. Do not reconnect with the same token |

   A link allows 5 concurrent streams. WebSocket close codes: 4401 (authentication failed, or the link was
   revoked or expired), 1013 (too many streams, try later), 1012 (the server is restarting: reconnect with
   backoff and resync from `GET /v1/tracking/snapshot`).

4. **Renew.** Call `POST /v1/tracking/session/refresh` with the current viewer token before it expires (for
   example every 10 minutes). You do not need the link token again. Refreshes count against the same per-IP and
   per-link limits as session creation, so do not refresh more often than that. On an open WebSocket, send `{"type":"auth","token":"<new viewer_token>"}`. An
   open stream stays open past the token's expiry unless the link is revoked or expires. A refresh after
   rotation or expiry is `401 viewer_token_invalid`.

### The courier signal

`courier.signal` tells you how fresh the position is: `pending` (visible, no fix yet), `live` (last fix at most
30 seconds old), `stale` (older than 30 seconds) and `lost` (older than 5 minutes, or still `pending` after 5
minutes). The server flips these on its own timers, so a silent courier goes `stale` without a new fix.

## Rotate a link

If a link leaks, rotate it:

```bash
curl -sS -X POST https://api.example.invalid/v1/deliveries/del_.../tracking-link/rotate \
  -H "Authorization: Bearer $DT_SECRET_KEY" -H "Idempotency-Key: $(uuidgen)"
```

The answer is the new `tracking` object. The old token stops working at once, and viewers connected through it
are disconnected within a second (their stream receives `revoked`). Send the new `url` to your customer. Rotation
needs scope `deliveries:write`.
