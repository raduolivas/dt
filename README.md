# DT (Delivery Tracker)

DT is a SaaS that gives delivery apps a videogame-like 2D/3D live-tracking experience for their customers.
Partners send order events and courier GPS; DT renders the delivery as a stylized city map. Full context:
[`docs/PROJECT_CONTEXT.md`](docs/PROJECT_CONTEXT.md).

## Prerequisites

Go 1.27.2, Node 24 LTS with pnpm (via corepack), Docker with Compose, buf, golangci-lint, govulncheck,
actionlint and pinact. Check them all with:

```bash
make doctor
```

## Everyday commands

| Command | What it does |
|---|---|
| `make dev` | Build the service images, start the local stack (Postgres + PostGIS and the five services) and wait until every `/readyz` answers 200 |
| `make down` | Stop the local stack and delete its volumes |
| `make logs` | Follow the local stack logs |
| `make test` | Run every Go test (race detector; integration tests need Docker) and every web test |
| `make test-go-short` | Go unit tests only, no Docker |
| `make lint` | Go lint (including the onion rules), web lint + typecheck, contract lint |
| `make gen` | Regenerate code from the contracts (Protobuf → Go, OpenAPI → TypeScript) |
| `make vuln` / `make audit-web` | Scan Go modules / web dependencies for known vulnerabilities |
| `make web-dev` | Run the renderer prototype at http://localhost:5173 |

Local services listen on `127.0.0.1` only: gateway 8080, deliveries 8081, tracking 8082, webhooks 8083,
accounts 8084, Postgres 5432. Every service exposes `GET /livez` and `GET /readyz`.

## Repository layout

```
backend/                Go workspace (go.work)
  services/{gateway,deliveries,tracking,webhooks,accounts}/
                        one module per service: cmd/, internal/{domain,app,adapters}/, migrations/
  pkg/                  technical shared libraries only (logx, httpx, bootstrap, testkit)
  gen/                  generated contract code (never edited by hand)
contracts/
  openapi/dt.v1.yaml    public REST API (OpenAPI 3.1)
  proto/dt/.../v1/      event and internal contracts (Protobuf, managed by buf)
web/                    pnpm workspace + Turborepo
  packages/renderer/    the Three.js tracking renderer (former prototype)
  packages/contract/    TypeScript types generated from the OpenAPI contract
infra/compose/          local stack; postgres/init creates one schema and one role per service
scripts/                helper scripts (wait-ready.sh)
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
