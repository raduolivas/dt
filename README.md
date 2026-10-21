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
| `make smoke` | After `make dev`: the M1 end-to-end smoke test (`scripts/smoke-m1.sh`, needs `jq`); prints `SMOKE OK` |
| `make dtctl` | Build the dev CLI for the accounts API into `backend/bin/dtctl` |
| `make down` | Stop the local stack and delete its volumes |
| `make logs` | Follow the local stack logs |
| `make test` | Run every Go test (race detector; integration tests need Docker) and every web test |
| `make test-go-short` | Go unit tests only, no Docker |
| `make lint` | Go lint (including the onion rules), web lint + typecheck, contract lint |
| `make gen` | Regenerate code from the contracts (Protobuf → Go + Connect, SQL → Go via sqlc, OpenAPI → Go models + TypeScript) |
| `make vuln` / `make audit-web` | Scan Go modules / web dependencies for known vulnerabilities |
| `make web-dev` | Run the renderer prototype at http://localhost:5173 |

Local services listen on `127.0.0.1` only: gateway 8080, deliveries 8081, tracking 8082, webhooks 8083,
accounts 8084, Postgres 5432, Redis 6379, NATS 4222 (monitoring 8222). Every service exposes `GET /livez`
and `GET /readyz`. The public API is the gateway's `/v1`; accounts' internal RPC on 8084 is never routed
publicly.

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

## Service configuration

Besides `PORT`, `LOG_LEVEL` and `DATABASE_URL`, M1 adds:

| Variable | Service | Rule |
|---|---|---|
| `APIKEY_PEPPER` | accounts, gateway | base64, at least 32 decoded bytes, the **same** value in both. HMAC key for API key hashes; the service refuses to start without it |
| `INTERNAL_RPC_TOKEN` | accounts | at least 32 bytes; callers send `Authorization: Bearer <token>` (mTLS replaces it in M3) |
| `TRUSTED_PROXIES` | gateway | comma-separated CIDRs of proxies whose `X-Forwarded-For` hops count; empty means the TCP peer is the client |
| `REDIS_URL` | gateway | rate limits (the limiter fails open if Redis is down) |
| `NATS_URL` | accounts, gateway | JetStream for the `accounts.*` events |

The values in `infra/compose/compose.yaml` are dev-only placeholders; never reuse them anywhere else.

## Repository layout

```
backend/                Go workspace (go.work)
  services/{gateway,deliveries,tracking,webhooks,accounts}/
                        one module per service: cmd/, internal/{domain,app,adapters}/, migrations/
  pkg/                  technical shared libraries only (logx, httpx, bootstrap, outbox, apikey, otelx, testkit, ...)
  gen/                  generated contract code (never edited by hand)
  tools/dtctl/          dev CLI for the accounts internal RPC
contracts/
  openapi/dt.v1.yaml    public REST API (OpenAPI 3.1)
  proto/dt/.../v1/      event and internal contracts (Protobuf, managed by buf)
web/                    pnpm workspace + Turborepo
  packages/renderer/    the Three.js tracking renderer (former prototype)
  packages/contract/    TypeScript types generated from the OpenAPI contract
infra/compose/          local stack; postgres/init creates one schema and one role per service
scripts/                helper scripts (wait-ready.sh, smoke-m1.sh)
docs/                   project context, architecture, open questions, specs and plans
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
- [`docs/OPEN_QUESTIONS.md`](docs/OPEN_QUESTIONS.md): questions waiting for an answer
- [`docs/superpowers/specs/`](docs/superpowers/specs/): the MVP spec; [`docs/superpowers/plans/`](docs/superpowers/plans/): milestone plans

## Troubleshooting

- **`make dev` fails with `lookup proxy.golang.org ... i/o timeout`:** the Docker build containers have no
  network access. This happens with some rootless Docker setups. Fix Docker's networking (for example, the
  rootless network driver or the daemon `dns` setting), or build once with host networking through a
  Compose override that sets `build.network: host` for each service.
