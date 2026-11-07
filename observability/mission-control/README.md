# Mission Control

A local 3D troubleshooting view of DeliveryOnTrack: services, the broker and the database as nodes, with pulses for real events (orders, courier updates, webhooks). Design: `docs/superpowers/specs/2026-11-05-mission-control-design.md`.

Mission Control shows one order at a time: search by order number, delivery id or tracking link and see where its request, courier updates and events went, hop by hop, with the detector findings that name the hop and reason when something breaks. With no order searched it shows the whole system lit by aggregate traffic.

## Run

Start order (Mission Control joins the DT stack's `dt_default` network, so DT must be up first):

```
make dev      # 1. the DT stack (with the OTel collector, the default)
make mc       # 2. builds and starts mc-backend + mc-postgres, waits for /readyz
make mc-sim   # 3. optional: simulation mode, a fake courier phone and viewer on a new order
make mc-down  # stops them and drops the mc-pg volume
make mc-test mc-lint
make mc-e2e   # the milestone test against the running stack (below); not part of make ci
```

Open http://127.0.0.1:8095. It listens on `127.0.0.1:8095` only. `/readyz` means Mission Control's own Postgres answers; NATS and DT health are shown inside the app.

Example: `make mc-sim` prints an order such as `mc-sim-20261107-084053-6a8e38`. Paste it in the search box: the left panel lists its status steps and findings, the scene lights the path it touched, pulses travel along real events, the inspector shows each part's liveness, readiness, dependencies and recent events, and the timeline at the bottom replays it.

## Milestone test

`make mc-e2e` (after the three steps above) runs `e2e/milestone_test.go` (build tag `mce2e`, its own tiny Go module, standard library only). It creates a simulated order, waits until the search finds it, checks that its timeline holds `delivery_created`, a status change and one trace shared by the gateway courier span, `tracking.ingest` and `tracking.viewer_fanout`, then breaks each scenario and asserts the finding within 40 s, heals it and asserts the finding clears. Scenarios 5 and 6 call `scripts/mc-break.sh`, which uses Docker; that is allowed here because the test is a dev tool, not part of mc-backend. One pass starts three simulated runs and mc-backend allows 8 live at once, so run `make mc-sim` (it restarts mc-backend) before a pass. Measured live: scenario 1 about 32-35 s, 2 and 3 under 10 s, 4 under 10 s, 5 under 10 s, 6 about 29 s; each finding clears within about 1 minute of healing (the detector windows are 60 s). `MC_E2E_ONLY=4,5` runs a subset; `MC_URL` points at another address.

## Simulation mode

```
make mc-sim                  # sandbox key -> .env, restart mc-backend with MC_SIMULATION=1, new simulated order
make mc-break SCENARIO=1..6  # break one hop (add HEAL=1 to heal it)
```

`make mc-sim` reuses `dtctl` (like `make courier-link`) to create a sandbox tenant and a `dt_sk_test_` key once, writes it to the git-ignored `observability/mission-control/.env` as `MC_SANDBOX_KEY` (never printed), starts mc-backend with `MC_SIMULATION=1` (needs `MC_ENV=local`), creates a delivery through the real partner API and prints the order number and tracking link. A fake courier phone posts one fix per second through the gateway courier API with the courier's own JWT (refreshing it as the courier page does), confirms pickup early so the viewer hop is live, and one headless SSE viewer connects through the edge (`MC_EDGE_URL`, compose default `http://edge:8080`). Everything it creates is origin `simulated`. That marker is durable: simulated order refs start with `mc-sim-`, and the normalizer marks any `delivery.created` whose `external_id` has that prefix as simulated (so it cannot race the in-memory registry); later events of the delivery stay simulated through the stored order origin, also after a restart. Use the `mc-sim-` prefix for sandbox orders only: a real partner order ref starting with it would be shown as simulated. `GET /api/sim/status` exists only in simulation mode; the order panel shows the break buttons when it answers and knows the order.

| # | Scenario | How | Expected finding |
|---|---|---|---|
| 1 | phone stops | API (phone pauses) | `courier_stale` on the order, hop courier |
| 2 | invalid token | API (corrupted JWT) | `rejected_at_gateway` reason `unauthorized`, system view (a 401 carries no delivery id) |
| 3 | rate limit | API (20 posts/s) | `rejected_at_gateway` reason `rate_limited`, system view |
| 4 | reassigned | API: a second courier is assigned | `fixes_dropped` reason `no_active_delivery` at `tracking.ingest` (system view, empty delivery id) (system view, empty delivery id); no `courier_stale` appears on the held order (see below) |
| 5 | Redis down | `make mc-break SCENARIO=5` (`docker compose -p dt pause redis`) | `dependency_failing` at `tracking.state` / `redis` (system view; the gateway rate limiter fails too) |
| 6 | OSRM stopped | `make mc-break SCENARIO=6` (stops `osrm-car`/`osrm-bike`, then starts a fresh simulated order so tracking builds a route) | `dependency_failing` reason `route_fallback` at `osrm` (system view) |

DT allows a reassignment only before pickup, so `make mc-break SCENARIO=4` makes its own run with the pickup held; healing it reassigns back and the pickup goes ahead. Scenario 4 and DT: when a courier is reassigned, DT removes the delivery from the old courier's active set, so the old phone's fixes have no active delivery and tracking logs `no_active_delivery`; DT never reports `delivery_reassigned`. Mission Control shows what DT emits. The plan also expected `courier_stale` on the order for scenario 4, but the run that makes it holds its pickup, so DT keeps its status `confirmed`, and courier_stale only examines assigned and picked-up statuses; the milestone test logs it and does not assert it (open item in `docs/mc2-followups.md`). Scenarios 2 to 4 have no delivery id on the signal (a 401 or 429 carries none), so they appear in the system view's warnings, not in the order panel. OSRM is not traced: scenario 6 works because tracking logs `route_fallback` when it builds a route, which the script forces. Each finding should appear within 40 s of the break (scenario 1 takes about 35 s: the 30 s courier-stale threshold plus the 5 s detector tick). Scenarios 5 and 6 are run by `scripts/mc-break.sh` only: mc-backend never touches Docker.

## Limitations

- Browser hops (the tracking page, the courier page) are not instrumented: they appear as outside actors, and pulses for them are inferred from the gateway span and drawn dotted.
- No trace data before Mission Control started: only NATS stream history exists for earlier events, so the timeline shows a "no trace data for this period" band.
- Webhooks are not built in DT, so that part is shown as not instrumented.
- Local only: it binds 127.0.0.1, has no login (`api.AuthSeam` is a seam) and keeps 7 days of data.
- Deliveries and accounts expose health on mTLS only, so Mission Control shows them as "not probed", never red.
- OSRM emits no traces; its state is inferred from tracking's `route_fallback` log.
- Follow-ups and the rulings behind these choices: `docs/mc2-followups.md`.

## Rules

- Read-only toward DT: it consumes NATS with its own consumers (`mc-<stream>`) and receives OTLP from the collector. It never publishes and makes no DT write calls, except in simulation mode (`MC_SIMULATION=1`, `MC_ENV=local`, a `dt_sk_test_` key only).
- Never stored: coordinates, names, phone numbers, addresses, notes, photo URLs, tokens or keys. Payloads are copied by allow-list; span and log attributes go through the `logx` deny-list. Every drop counts in the self-health `redacted` counter.
- No Docker socket, no shell from the backend. No login in this version (`api.AuthSeam` is a seam).
- Retention is 7 days. Thresholds live in `backend/config/thresholds.yaml`.

## Configuration

Environment variables (`.env.example`): `MC_HTTP_ADDR`, `MC_OTLP_ADDR`, `MC_DATABASE_URL`, `MC_NATS_URL`, `MC_NATS_MONITOR_URL`, `MC_GATEWAY_URL`, `MC_EDGE_URL` (simulation only: the origin of the viewer API, default the gateway URL; compose sets `http://edge:8080`), `MC_ENV`, `MC_SIMULATION`, `MC_SANDBOX_KEY`, `MC_METRICS_SOURCE`, `MC_PROMETHEUS_URL`, `MC_LINK_<KIND>`, `MC_THRESHOLDS_FILE`.

"Open in..." links: `<KIND>` is one of `GRAFANA_TRACE`, `GRAFANA_LOGS`, `XRAY_TRACE`, `CLOUDWATCH_LOGS`. The value is a URL template starting with `http://` or `https://` (anything else is dropped) with `{trace_id}`, `{service}` and `{component}` placeholders, which are URL-escaped. A link shows only when its template is set and every placeholder it uses has a value (trace links need a trace id). An unknown `MC_LINK_` name is logged at startup and ignored.
