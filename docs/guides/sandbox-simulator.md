---
title: Sandbox simulator
description: Drive a test delivery with a virtual courier to try tracking without a courier app.
---

# Sandbox simulator

In the sandbox, DT can play the courier for you, so you can see a delivery go from creation to delivered and
watch every tracking endpoint fill in.

```bash
curl -sS -X POST https://api.example.invalid/v1/test/deliveries/del_.../simulate \
  -H "Authorization: Bearer $DT_TEST_KEY" -H "Idempotency-Key: $(uuidgen)" \
  -H 'Content-Type: application/json' \
  -d '{"speed_factor": 10, "signal_gap": {"after_s": 30, "duration_s": 90}}'
```

The answer is `202` with a `simulation_id`; the run continues in the background. Requires scope
`deliveries:write` and a **test key**: a live key gets `403 environment_not_allowed`.

## What it does

- Drives a virtual courier along the road route. Fixes go through the same path as the relay, tagged
  `source: simulator`, so the trail, the signal and the proof points behave as they would for real.
- Reports the courier's status events the delivery's model needs (`at_pickup`, `pickup_confirmed`,
  `delivered`; `arriving` follows from the courier's position), confirming pickup the way your pickup policy asks.
- If the delivery has no courier, it creates a test courier called "Sim Courier" and assigns it. If you
  assigned one, it drives that courier.
- It respects the ready time and your preparation time (scaled by `speed_factor`). With no ready time it
  picks up at once.
- Your delivery's own rules still apply: a refusal such as an invalid transition comes back with its own code.

## Options

| Field | Meaning |
|---|---|
| `speed_factor` | 1 to 20, default 1. How many times faster than real time the courier drives |
| `signal_gap.after_s` | 0 to 3600. When the gap starts, in simulated seconds after the start |
| `signal_gap.duration_s` | 0 to 3600. How long no fixes are sent, in real (wall-clock) seconds, whatever the speed factor |

The gap lets you see `courier.signal` go `stale` after 30 seconds and `lost` after 5 minutes. The courier keeps
driving during the gap, so the fixes after it are plausible.

## Limits

- One run per delivery. A second call is `409 simulation_state`, as is a delivery that is final or in a state the
  simulator cannot drive.
- Follow a run with the viewer ([Tracking links](tracking-links.md)), `GET /v1/deliveries/{id}/tracking`, the
  trail and the proof points ([Partner tracking data](partner-tracking-data.md)).
