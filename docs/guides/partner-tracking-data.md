---
title: Partner tracking data
description: Read the live view, the courier trail and the proof points, and how long DT keeps them.
---

# Partner tracking data

You see more than your customer does. These endpoints use your secret key and need scope `deliveries:read`.

## Live view

`GET /v1/deliveries/{id}/tracking` returns what the customer's viewer shows, without the visibility filter:
the courier's `id`, your `external_id`, position, heading, speed, `position_at` and `signal` are present as soon as
a courier is assigned, whatever the delivery model and status. It is available in every state, and after a final
state it returns the last values. Use it for support and security.

## Trail

`GET /v1/deliveries/{id}/trail` lists the accepted fixes from the delivery window, oldest first. The window runs
from assignment (from creation in the `direct` model) until the delivery is final.

- `limit` 1 to 1000 (default 500), and `starting_after` with the previous page's `next_cursor`.
- Each fix: `t`, `lat`, `lng`, `accuracy_m`, `speed_mps`, `heading_deg`, `source` (`relay` or `simulator`).
- `?format=geojson` returns a GeoJSON FeatureCollection (`application/geo+json`): one `LineString` for the
  route and one `Point` per fix. Coordinates are `[lng, lat]`, as GeoJSON requires.
- After 30 days, or after a personal-data erasure, the raw fixes are gone: `raw_available` is `false`, `data` is
  empty and `simplified` holds the route as `[lat, lng]` pairs.

## Proof points

`GET /v1/deliveries/{id}/proof-points` returns one entry for each reported status in which the courier's
position matters (`at_pickup`, `pickup_confirmed`, `arriving`, `delivered`, `failed`): `status`, `at`, `actor`,
`position`, `position_at` and `accuracy_m`. The position is the latest accepted fix from the 2 minutes before
the report, or `null` when there was none. Use it to settle "the courier says they were at the door".

Every read of the trail and of the proof points is written to your audit log.

## How long DT keeps it

| Data | Kept |
|---|---|
| Raw fixes (the trail) | 30 days, then replaced by one simplified route |
| Simplified routes, proof points, tracking data, deliveries and tracking links | 13 months after the delivery is final, then deleted |
| Personal-data erasure (`DELETE /v1/deliveries/{id}/personal-data`) | the raw trail is replaced by the simplified route at once; proof points stay |

Courier location is personal data. Read it for the purpose you told your couriers about (see
[Courier locations](courier-locations.md)), and do not copy it beyond what you need.
