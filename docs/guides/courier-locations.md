---
title: Courier locations
description: Relay your couriers' GPS fixes to DT, the limits that apply, and what to tell your couriers.
---

# Courier locations

DT draws the courier on the map from GPS fixes. In M3 you relay the fixes your own courier app collects. (DT's
own courier SDK and link come later.)

## Before you send: register and assign

1. Register the courier once: `PUT /v1/couriers/{external_id}` returns DT's id (`cou_...`).
2. Assign the courier to the delivery: `POST /v1/deliveries/{id}/assignment` with `{"courier_id":"cou_..."}`.
3. Send fixes to `POST /v1/couriers/{cou_id}/locations`.

A fix is accepted only while the courier has at least one assigned delivery that is not final. Fixes sent
outside that window are counted as `dropped`.

### Direct deliveries: assign before pickup

In the `direct` model your customer sees the courier only from `picked_up`, and assignment does not change the
delivery's status. You may assign any time before pickup, and you should: a Direct delivery that reaches
`picked_up` with no courier assigned shows `courier: null` for its whole life. Fixes are accepted from the
moment of assignment, so you see the courier early in `GET /v1/deliveries/{id}/tracking`, and your customer
sees it as soon as the delivery is picked up.

## Send fixes

```bash
curl -sS -X POST https://api.example.invalid/v1/couriers/cou_01j9z3k8m2q4r6s8t0v2w4x6y8/locations \
  -H "Authorization: Bearer $DT_SECRET_KEY" -H 'Content-Type: application/json' \
  -d '{"fixes":[{"t":"2026-10-22T14:15:03Z","lat":-23.5612,"lng":-46.6557,"accuracy_m":6.5,"speed_mps":4.8,"heading_deg":84}]}'
```

The answer is `{"accepted": 1, "dropped": 0}`. Requires scope `couriers:write`.

| Field | Rule |
|---|---|
| `t` | when the fix was taken (RFC 3339) |
| `lat`, `lng` | WGS 84 degrees |
| `accuracy_m` | 0 to 10000, required |
| `speed_mps` | optional, 0 to 100 |
| `heading_deg` | optional, 0 up to (not including) 360 |

### Limits

- A batch holds at most 50 fixes and 16 KB (`413 payload_too_large` above that).
- Rate limit: 1 request per second per courier, with a burst of 5, counted within your tenant and
  environment (your test and live couriers never share a bucket). On top of that your tenant has a ceiling of
  500 requests per second (burst 1000). These limits are separate from your API key's own limit, so relaying
  does not eat into the budget for your other calls. Over a limit you get `429 rate_limited` with `Retry-After`.
- Send one request per second per courier, batching the fixes since the last one. If your courier app buffers
  fixes while offline, send them in batches of up to 50 once the connection returns.

### What is dropped, and what is kept

- A fix with `t` more than 5 seconds after DT received it is rejected, so keep your servers' clocks right.
- A fix older than 2 minutes at receipt is stored in the trail but does not move the live position.
- An implausible fix is dropped and counted in `dropped`: faster than 200 km/h from the previous fix, or a jump
  of more than 1 km in under 10 seconds.
- The relay takes no `Idempotency-Key`. A fix is identified by the courier and its `t`, so sending the same fix
  again is harmless.

## Tell your couriers they are tracked

A courier's location is the courier's personal data, and you are the controller of it. Under DT's partner
terms and data processing agreement (DPA), you must inform your couriers that their location is tracked while
they are on a delivery, and tell them what for (live tracking for the customer, and security and resolving
delivery issues). DT only processes the fixes you send, for the retention period in
[Partner tracking data](partner-tracking-data.md). Put the notice in your courier app and your courier
contracts. DT stores no more about a courier than the `external_id` you chose, a display name and a vehicle
type; the link to the person stays in your own records.
