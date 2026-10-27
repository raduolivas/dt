# DT (Delivery Tracker)

DT is a SaaS that gives delivery apps a videogame-like 2D/3D live-tracking experience for their customers.
Partners send order events and courier GPS; DT renders the delivery as a stylized city map. Full context:
[`docs/PROJECT_CONTEXT.md`](docs/PROJECT_CONTEXT.md).

## Prerequisites

Go 1.27.3, Node 24 LTS with pnpm (via corepack), Docker with Compose, buf, golangci-lint, govulncheck,
actionlint, pinact and sqlc (v1.31.1, `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1`). Check them all with:

```bash
make doctor
```

## Everyday commands

| Command | What it does |
|---|---|
| `make dev` | Build the service images, start the local stack (Postgres + PostGIS, Redis, NATS JetStream and the five services) and wait until every `/readyz` answers 200 |
| `make seed` | After `make dev`: create the tenant `Dev Tenant` and one test key, and print a `curl` line for `GET /v1/me` |
| `make smoke` | After `make dev`: the M1, M2 and M3 end-to-end smoke tests (`scripts/smoke-m1.sh`, `scripts/smoke-m2.sh`, `scripts/smoke-m3.sh`, need `jq`); prints `SMOKE OK`, then `SMOKE M2 OK`, `SMOKE M3 OK` and `OK` |
| `make latency` | After `make dev`: drive 20 couriers at 1 Hz with 5 viewers for 2 minutes and print the fix-to-viewer p50, p95 and p99; exits non-zero when p95 is 1000 ms or more. Samples where the snapshot beats the relay response count as 0, so the figure is a floor |
| `make dtctl` | Build the dev CLI for the accounts API into `backend/bin/dtctl` |
| `make down` | Stop the local stack and delete its volumes |
| `make logs` | Follow the local stack logs |
| `make test` | Run every Go test (race detector; integration tests need Docker) and every web test |
| `make test-go-short` | Go unit tests only, no Docker |
| `make lint` | Go lint (including the onion rules), web lint + typecheck, contract lint |
| `make gen` | Regenerate code from the contracts (Protobuf → Go + Connect, SQL → Go via sqlc, OpenAPI → Go models + TypeScript) |
| `make vuln` / `make audit-web` | Scan Go modules / web dependencies for known vulnerabilities |
| `make web-dev` | Run the renderer prototype at http://localhost:5173 |
| `make osrm-data` | Download and preprocess the Amsterdam routing data (`make dev` runs it) |
| `make ci` | Every CI check locally: lint, tests, web build, audits, vulnerability scans, secret scan. It does **not** run the `iac` CI job (`tofu fmt`, `init`/`validate`, `tofu test` and the trivy scan of `infra/aws`); run those by hand when you touch `infra/` (the job must be among the required checks of `main`) |
| `make aws-bootstrap PHASE=a\|b` | Owner only, in CloudShell: create the AWS organization, guard rails and CI role ([`docs/runbooks/aws-account-setup.md`](docs/runbooks/aws-account-setup.md)) |
| `make staging-up` / `staging-down` | Owner only: create or update, or destroy, the AWS staging stack ([`docs/runbooks/staging.md`](docs/runbooks/staging.md)) |
| `make staging-wake` / `staging-sleep` | Owner only: start or stop the staging host and database outside the schedule |

Local services listen on `127.0.0.1` only: gateway 8080, deliveries 8081, tracking 8082, webhooks 8083,
accounts 8084, tracking's internal RPC 8085, Postgres 5432, Redis 6379, NATS 4222 (monitoring 8222), OSRM car
5000 and bike 5001. Every service exposes `GET /livez` and `GET /readyz`. The public API is the gateway's `/v1`
(tracking's 8082 serves the viewer endpoints behind the same load balancer); the internal RPC listeners (accounts
8084, deliveries 8081, tracking 8085) speak mTLS and are never routed publicly.

## Status

M0 to M2 are merged. **M3 (live tracking: tracking links, courier location relay, viewer streams, sandbox simulator,
retention) is code complete on branch `m3-tracking`** and not merged yet. Its AWS staging stack
(`infra/aws`, target `https://api.staging.deliveryontrack.io`) is written and validated but waits for the owner to
apply it. Partner guides: [`docs/guides/`](docs/guides/). Runbooks: [`docs/runbooks/`](docs/runbooks/) (AWS account
setup, staging, key rotation).

## Tenants and API keys in development

Until the partner console exists (M7), `dtctl` creates tenants and keys through the accounts internal
Connect RPC. It reads `DT_ACCOUNTS_URL` (default `http://127.0.0.1:8084`) and `DT_INTERNAL_RPC_TOKEN` (the
accounts `INTERNAL_RPC_TOKEN`; the Makefile defaults it to the dev-only value in `compose.yaml`) and acts as
the tenant `owner`.

```bash
make dtctl
export DT_INTERNAL_RPC_TOKEN=dev-only-not-a-secret-internal-token-0000
T=$(backend/bin/dtctl tenant create --name "Acme")
K=$(backend/bin/dtctl key create --tenant "$T" --env test --name ci)   # secret on stdout, once
curl -sS -H "Authorization: Bearer $K" http://127.0.0.1:8080/v1/me
backend/bin/dtctl key list   --tenant "$T" --env test
backend/bin/dtctl key roll   --tenant "$T" --key ak_...   # new secret on stdout; the old key works 24 h more
backend/bin/dtctl key revoke --tenant "$T" --key ak_...   # the gateway rejects it within 5 s
backend/bin/dtctl tenant live --tenant "$T" --enabled=true   # allow live keys (Stripe sets this from M7)
```

`key create` also takes `--scope S` and `--allow-ip CIDR` (both repeatable; default: every scope, any IP).
A secret is printed once on stdout and is never stored or logged; everything else goes to stderr. Errors
print as `<connect code>: <dt-code>`, for example `failed_precondition: live_not_enabled`. A new key reaches
the gateway through NATS, usually within milliseconds.

## Deliveries and couriers (M2)

Public routes on the gateway (`/v1`, secret key, every POST needs an `Idempotency-Key`; test and live keys both work):

| Route | Scope | What it does |
|---|---|---|
| `PUT /v1/couriers/{external_id}` | `couriers:write` | Create (201) or update (200) a courier; returns DT's `cou_` id |
| `POST /v1/deliveries` | `deliveries:write` | Create a delivery in `confirmed` (`201`) |
| `GET /v1/deliveries` | `deliveries:read` | List, newest first (`limit` 1-100, `starting_after`) |
| `GET /v1/deliveries/{id}` | `deliveries:read` | Get one delivery |
| `PATCH /v1/deliveries/{id}` | `deliveries:write` | Change dropoff, dropoff label, notes or metadata (the dropoff location only before pickup) |
| `POST /v1/deliveries/{id}/assignment` | `deliveries:write` | Assign a courier by `cou_` id |
| `POST /v1/deliveries/{id}/events` | `deliveries:write` | Report progress (`event_id` makes it idempotent); returns `{outcome, delivery}` |
| `POST /v1/deliveries/{id}/cancel` | `deliveries:write` | Cancel a delivery before pickup (`picked_up` or later: `409 invalid_transition`) |
| `DELETE /v1/deliveries/{id}/personal-data` | `deliveries:write` | Erase personal data of a final delivery |

Delivery settings per tenant (allowed models, default model, pickup confirmation rule) are set with `dtctl`.
Note that `--default` must be one of the `--models`:

```bash
backend/bin/dtctl tenant settings --tenant "$T" --models direct,end_to_end --default direct --pickup any
backend/bin/dtctl tenant settings --tenant "$T" --models end_to_end --default end_to_end --pickup store
```

`--pickup courier` and `--pickup courier_and_store` need a courier's own confirmation (`by: courier`), which only
the courier endpoints (M5) can send; until then, deliveries under those rules cannot get past pickup.

Until a tenant's settings reach `deliveries` (through NATS), the defaults apply: both models, `direct`, `any`.
The `deliveries` service also needs `DATA_KEYS` (`id:base64,...`, 32-byte keys) and `DATA_KEY_CURRENT` to
seal personal data, plus `INTERNAL_RPC_TOKEN`; it refuses to start without them. The gateway needs
`DELIVERIES_RPC_URL` and its own `DATA_KEYS` / `DATA_KEY_CURRENT` (same format, a separate key ring):
stored idempotency responses can carry customer data, so the gateway seals them and deletes a delivery's
stored responses when its personal data is erased. `scripts/smoke-m2.sh` walks through the whole flow.

## Service configuration

Every service reads `PORT`, `LOG_LEVEL`, `DATABASE_URL` (the runtime role, `svc_<svc>` locally) and
`MIGRATIONS_DATABASE_URL` (the role that owns the service's schema and runs its migrations, `mig_<svc>`
locally). The runtime role owns no table, so it cannot weaken row-level security. Staging and production
always set `MIGRATIONS_DATABASE_URL`; without it, migrations run as `DATABASE_URL` and the service logs the
warning `migrations_with_runtime_role`. M1 adds:

| Variable | Service | Rule |
|---|---|---|
| `APIKEY_PEPPER` | accounts, gateway | base64, at least 32 decoded bytes, the **same** value in both. HMAC key for API key hashes; the service refuses to start without it |
| `INTERNAL_RPC_TOKEN` | accounts, deliveries, tracking (servers); gateway, tracking, `dtctl` (callers, same value) | at least 32 bytes; callers send `Authorization: Bearer <token>` (on top of mTLS since M3) |
| `TRUSTED_PROXIES` | gateway | comma-separated CIDRs of proxies whose `X-Forwarded-For` hops count; empty means the TCP peer is the client |
| `REDIS_URL` | gateway | rate limits (the limiter fails open if Redis is down) |
| `NATS_URL` | accounts, gateway | JetStream for the `accounts.*` events |
| `DATA_KEYS`, `DATA_KEY_CURRENT` | deliveries, gateway (M2) | `id:base64,...` of 32-byte AES keys and the id that seals new values; each service has its own keys. It refuses to start without them |
| `VIEWER_SIGNING_KEYS`, `VIEWER_SIGNING_KEY_CURRENT` | gateway (M3) | `kid:base64(32-byte Ed25519 seed),...` and the `kid` that signs viewer tokens; every listed key is published at `/.well-known/jwks.json`. Rotation: [`data-key-rotation.md`](docs/runbooks/data-key-rotation.md) |
| `TRACKING_RPC_URL` | gateway (M3) | `https://` URL of tracking's internal mTLS RPC port (`https://tracking:8085` locally); without it tracking routes answer `tracking_unavailable` |
| `TRACKING_URL_BASE` | gateway (M3) | absolute http(s) base of the viewer links the gateway returns (no query, fragment or credentials) |
| `DELIVERIES_RPC_URL` | gateway, tracking (M3 for tracking) | `https://` URL of deliveries' internal RPC (`https://deliveries:8081`) |
| `JWKS_URL` | tracking (M3) | the gateway's `/.well-known/jwks.json`, used to verify viewer tokens |
| `OSRM_CAR_URL`, `OSRM_BIKE_URL` | tracking (M3) | OSRM routing services (`http://osrm-car:5000`, `http://osrm-bike:5000` in Compose); if unset, routes and ETAs are off |
| `INTERNAL_RPC_PORT` | tracking (M3) | port of tracking's internal mTLS RPC listener (8085); `PORT` serves health and the public viewer endpoints |
| `INTERNAL_RPC_TLS_CERT`, `INTERNAL_RPC_TLS_KEY`, `INTERNAL_RPC_TLS_CA` | gateway, deliveries, tracking, accounts (M3) | mTLS for every internal RPC: the service's certificate and key and the CA (all three or none; the RPC URLs must then be `https`). `make dev` creates dev certificates with `scripts/dev-certs.sh` |
| `MIGRATIONS` | every service (M3) | `run` (default: migrate, then serve), `only` (migrate and exit: the one-off deploy tasks) or `skip` (serve without touching the schema; needs no `MIGRATIONS_DATABASE_URL`) |

The values in `infra/compose/compose.yaml` are dev-only placeholders; never reuse them anywhere else.

## Repository layout

```
backend/                Go workspace (go.work)
  services/{gateway,deliveries,tracking,webhooks,accounts}/
                        one module per service: cmd/, internal/{domain,app,adapters}/, migrations/
  pkg/                  technical shared libraries only (logx, httpx, bootstrap, outbox, apikey, otelx, testkit, ...)
  gen/                  generated contract code (never edited by hand)
  tools/dtctl/          dev CLI for the accounts internal RPC
  tools/latency/        fix-to-viewer latency measurement (make latency)
contracts/
  openapi/dt.v1.yaml    public REST API (OpenAPI 3.1)
  proto/dt/.../v1/      event and internal contracts (Protobuf, managed by buf)
web/                    pnpm workspace + Turborepo
  packages/renderer/    the Three.js tracking renderer (former prototype)
  packages/contract/    TypeScript types generated from the OpenAPI contract
infra/compose/          local stack; postgres/init creates one schema and one role per service
scripts/                helper scripts (wait-ready.sh, smoke-m1.sh, smoke-m2.sh, smoke-m3.sh)
docs/                   project context, architecture, open questions, specs and plans
  guides/               partner guides (tracking links, courier locations, tracking data, simulator)
```

## The onion rule

Each service is split into three layers that depend inward only. `internal/domain` holds entities, value
objects and domain events and uses the standard library only. `internal/app` holds use cases and the ports
(interfaces) they need, with no transport or driver imports. `internal/adapters` implements those ports
(HTTP handlers, Postgres repositories, message publishers). golangci-lint's `depguard` enforces this in
`make lint`, so a domain file importing `net/http` or an app file importing an adapter fails the build.
Each service also owns its own database schema and login role, and no role can read another's schema.

## Renderer

`web/packages/renderer` is the 3D tracking view. The engine (`src/engine/`, no Three.js) and the view
(`src/view/`) only talk through one small data contract, `TrackingSnapshot`, delivered by a
`TrackingSource`. The prototype feeds it from a simulator; in production the generated v1 contract replaces
it and the snapshots come from the tracking service. Open **Parameters → Live snapshot** in `make web-dev`
to watch the snapshot update live.

## Documentation

- [`docs/PROJECT_CONTEXT.md`](docs/PROJECT_CONTEXT.md): decisions and current state
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md): architecture draft
- [`docs/guides/`](docs/guides/): partner guides: [tracking links](docs/guides/tracking-links.md), [courier locations](docs/guides/courier-locations.md), [partner tracking data](docs/guides/partner-tracking-data.md), [sandbox simulator](docs/guides/sandbox-simulator.md)
- [`docs/runbooks/`](docs/runbooks/): [AWS account setup](docs/runbooks/aws-account-setup.md), [staging](docs/runbooks/staging.md), [key rotation](docs/runbooks/data-key-rotation.md) (data keys and viewer signing keys)
- [`docs/OPEN_QUESTIONS.md`](docs/OPEN_QUESTIONS.md): questions waiting for an answer
- [`docs/superpowers/specs/`](docs/superpowers/specs/): the MVP spec; [`docs/superpowers/plans/`](docs/superpowers/plans/): milestone plans

## Troubleshooting

- **A service exits with `migratex: cannot open database for migrations (does the migration role
  exist? ...)`:** your local Postgres volume was created before the `mig_<svc>` roles existed (the init
  script only runs on an empty volume). Reset the dev database with `make down` (that is,
  `docker compose down -v`), then `make dev`.

- **`make dev` fails with `lookup proxy.golang.org ... i/o timeout`:** the Docker build containers have no
  network access. This happens with some rootless Docker setups. Fix Docker's networking (for example, the
  rootless network driver or the daemon `dns` setting), or build once with host networking through a
  Compose override that sets `build.network: host` for each service. Pass it with
  `COMPOSE_OVERRIDE=/path/to/override.yaml make dev` (empty by default).
