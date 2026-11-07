# Mission Control

A local 3D troubleshooting view of DeliveryOnTrack: services, the broker and the database as nodes, with pulses for real events (orders, courier updates, webhooks). Design: `docs/superpowers/specs/2026-11-05-mission-control-design.md`.

This is the skeleton: config, the Postgres store and migrations, and `/livez` and `/readyz`. Ingest, the API and the UI arrive in later tasks.

## Run

```
make dev      # the DT stack (Mission Control joins its dt_default network)
make mc       # builds and starts mc-backend + mc-postgres, waits for /readyz
make mc-down  # stops them and drops the mc-pg volume
make mc-test mc-lint
```

It listens on `127.0.0.1:8095` only. `/readyz` means Mission Control's own Postgres answers; NATS and DT health are shown inside the app.

## Simulation mode

```
make mc-sim                  # sandbox key -> .env, restart mc-backend with MC_SIMULATION=1, new simulated order
make mc-break SCENARIO=1..6  # break one hop (add HEAL=1 to heal it)
```

`make mc-sim` reuses `dtctl` (like `make courier-link`) to create a sandbox tenant and a `dt_sk_test_` key once, writes it to the git-ignored `observability/mission-control/.env` as `MC_SANDBOX_KEY` (never printed), starts mc-backend with `MC_SIMULATION=1` (needs `MC_ENV=local`), creates a delivery through the real partner API and prints the order number and tracking link. A fake courier phone posts one fix per second through the gateway courier API with the courier's own JWT (refreshing it as the courier page does), confirms pickup early so the viewer hop is live, and one headless SSE viewer connects through the edge (`MC_EDGE_URL`, compose default `http://edge:8080`). Everything it creates is origin `simulated`. `GET /api/sim/status` exists only in simulation mode; the order panel shows the break buttons when it answers and knows the order.

| # | Scenario | How | Expected finding |
|---|---|---|---|
| 1 | phone stops | API (phone pauses) | `courier_stale` on the order, hop courier |
| 2 | invalid token | API (corrupted JWT) | `rejected_at_gateway` reason `unauthorized`, system view (a 401 carries no delivery id) |
| 3 | rate limit | API (20 posts/s) | `rejected_at_gateway` reason `rate_limited`, system view |
| 4 | reassigned | API: a second courier is assigned | `fixes_dropped`, tracking.ingest; DT reports `no_active_delivery` (empty delivery id), not `delivery_reassigned` (see report) |
| 5 | Redis down | `make mc-break SCENARIO=5` (`docker compose -p dt pause redis`) | dependency at `tracking.state>redis` and `redis` |
| 6 | OSRM stopped | `make mc-break SCENARIO=6` (stops `osrm-car`/`osrm-bike`, then starts a fresh simulated order so tracking builds a route) | dependency at `osrm` (`route_fallback`) |

DT allows a reassignment only before pickup, so `make mc-break SCENARIO=4` makes its own run with the pickup held; healing it reassigns back and the pickup goes ahead. Scenarios 5 and 6 are run by `scripts/mc-break.sh` only: mc-backend never touches Docker.

## Rules

- Read-only toward DT: it consumes NATS with its own consumers (`mc-<stream>`) and receives OTLP from the collector. It never publishes and makes no DT write calls, except in simulation mode (`MC_SIMULATION=1`, `MC_ENV=local`, a `dt_sk_test_` key only).
- Never stored: coordinates, names, phone numbers, addresses, notes, photo URLs, tokens or keys. Payloads are copied by allow-list; span and log attributes go through the `logx` deny-list. Every drop counts in the self-health `redacted` counter.
- No Docker socket, no shell from the backend. No login in this version (`api.AuthSeam` is a seam).
- Retention is 7 days. Thresholds live in `backend/config/thresholds.yaml`.

## Configuration

Environment variables (`.env.example`): `MC_HTTP_ADDR`, `MC_OTLP_ADDR`, `MC_DATABASE_URL`, `MC_NATS_URL`, `MC_NATS_MONITOR_URL`, `MC_GATEWAY_URL`, `MC_ENV`, `MC_SIMULATION`, `MC_SANDBOX_KEY`, `MC_METRICS_SOURCE`, `MC_PROMETHEUS_URL`, `MC_LINK_<KIND>`, `MC_THRESHOLDS_FILE`.

"Open in..." links: `<KIND>` is one of `GRAFANA_TRACE`, `GRAFANA_LOGS`, `XRAY_TRACE`, `CLOUDWATCH_LOGS`. The value is a URL template starting with `http://` or `https://` (anything else is dropped) with `{trace_id}`, `{service}` and `{component}` placeholders, which are URL-escaped. A link shows only when its template is set and every placeholder it uses has a value (trace links need a trace id). An unknown `MC_LINK_` name is logged at startup and ignored.
