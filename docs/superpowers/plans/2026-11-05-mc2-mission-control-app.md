# MC-PR2: Mission Control app — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a local, separate app. You type an order number and see that delivery move through DT as a 3D scene with real green, amber and red pulses, linked logs and a timeline. It also shows where a courier's location stopped, and why.

**Architecture:** `mc-backend` (Go) reads DT's NATS streams through its own read-only consumers and receives OTLP traces and logs from the collector built in PR 1. It normalizes and redacts everything into `mc_event` rows in its own Postgres, keeps an order index, runs detectors, and serves a JSON API plus SSE on `127.0.0.1`. `mc-web` (Vite + React + React Three Fiber) renders the cockpit. Simulation mode (local only) drives a real delivery through DT's public APIs with a sandbox key: a fake courier phone plus one headless viewer.

**Tech Stack:** Go 1.27; `nats.go/jetstream`; pgx v5 + `migratex`/sqlc following DT's conventions; OTLP protobuf (`go.opentelemetry.io/proto/otlp`); protobuf types from `backend/gen` (read-only import); TypeScript, Vite, React, `@react-three/fiber`, `@react-three/drei`, Vitest. The builder checks the current official docs and pins exact versions.

**Spec:** `docs/superpowers/specs/2026-11-05-mission-control-design.md` (whole document). Depends on PR 1 (`2026-11-05-mc1-dt-groundwork.md`) being merged.

Branch: `mission-control` from `main` after PR 1 merges.

## Global Constraints

- **Location:** `observability/mission-control/`. It talks to DT only through NATS (read-only consumers named `mc-<stream>`; it never publishes) and OTLP from the collector. **No DT write calls outside simulation mode.** No Docker socket, no shell.
- **Network and access:** binds to `127.0.0.1` only; no login. An `auth` middleware seam is a no-op locally (MC-6).
- **Never stored:** coordinates, names, phone numbers, addresses, notes, photo URLs, tokens, API keys. Payloads are copied **by allow-list** only. Span and log attributes go through the `logx` deny-list (`logx.SensitiveKeys()` from PR 1). Every drop increments self-health counter `redacted`.
- **Retention:** 7 days, enforced by an hourly delete job.
- **Thresholds:** courier stale 30 s (amber), lost 5 min (red) — DT's own values. All other thresholds live in one config file (§7 table values as defaults).
- **Pulse colours:** green = ok, amber = slow/retried/dropped/rejected, red = failed. Cyan is the UI accent and purple means simulated. A pulse is one observed event, or a bundle with a count label. Pulses are never invented.
- **Distinct looks:** healthy+active, idle, not instrumented (dashed grey), stale/no data (faded + age), warning (amber), unhealthy/unreachable (red), inferred link (dotted wire).
- **Simulation:** enabled only with `MC_SIMULATION=1` and `MC_ENV=local`, otherwise startup fails. It uses a `dt_sk_test_` key only; any other key is refused.
- **Replay** never sends anything to DT.
- `make mc-test mc-lint` join `make ci`.

## Review Focus

1. **Mission Control started after DT has been running for a while:** the consumers catch up from stream history, and searching an order created before start shows its events, with a "no trace data" band for the period before start → Task 4 test `TestCatchUpFromStreamHistory`, Task 6 test `TestTimelineMarksPreStartGap`.
2. **The same order number in two tenants or environments:** search returns a picker, not a merged timeline → Task 5 test `TestSearchAmbiguousOrder`.
3. **The browser loses the SSE connection for longer than the server's replay buffer:** the timeline shows a gap, never a silent hole → Task 6 test `TestSSEGapMarkerWhenResumeTooOld`.
4. **A partner-supplied string holding HTML or a URL** (order number, courier display name already removed, reason text) must render as text → Task 11 test `rendersPartnerStringsAsText`.
5. **A burst of hundreds of events per second on one wire:** pulses bundle with a count instead of freezing the scene → Task 9 test `bundlesBurstsAboveThreshold`.

---

### Task 1: Module, config, store, migrations and compose skeleton

**Files:**
- Create: `observability/mission-control/{README.md,CLAUDE.md,compose.yaml,.env.example}`
- Create: `observability/mission-control/backend/go.mod`, module `github.com/raduolivas/dt/observability/mission-control/backend`. Add `../observability/mission-control/backend` to `backend/go.work`.
- Create: `backend/cmd/mc/main.go`, `backend/internal/config/config.go`, `backend/internal/store/{store.go,migrations/0001_init.up.sql}`
- Modify: root `Makefile`:
  - `mc`: `docker compose -p mc -f observability/mission-control/compose.yaml up -d --build`, then wait for `/readyz` and print the URL
  - `mc-down` (drops volumes)
  - `mc-test`: `go test -race ./...` in the backend, plus `pnpm -C observability/mission-control/web test`
  - `mc-lint`: golangci-lint with the repo config, plus web lint/typecheck
  - add `mc-test mc-lint` to `ci`
- Modify: `.github/workflows/ci.yml` (run `make mc-lint mc-test` in the Go and web jobs)
- Test: `backend/internal/config/config_test.go`, `backend/internal/store/store_test.go`

**Interfaces:**
- Produces: `config.Load() (Config, error)`. Fields:
  - `HTTPAddr` (default `127.0.0.1:8095`) and `OTLPAddr` (default `0.0.0.0:4318` **inside the container only**; compose publishes no OTLP port)
  - `DatabaseURL`, `NATSURL`, `NATSMonitorURL`, `GatewayURL`
  - `Env` (`MC_ENV`, default `local`), `Simulation bool`, `SandboxKey string`
  - `MetricsSource` (`builtin` | `prometheus` | `cloudwatch`), `PrometheusURL`
  - `Links map[string]string` (URL templates; §4.6)
  - `Retention` (7 days), `Thresholds` (loaded from `config/thresholds.yaml`)
  - `Load` errors when `Simulation && Env != "local"` and when `SandboxKey` lacks the `dt_sk_test_` prefix.
- Produces, schema `0001_init.up.sql`:
  - `mc_event(id bigserial pk, observed_at timestamptz, occurred_at timestamptz, kind text, source text, target text, outcome text, reason text, tenant_id text, environment text, delivery_id text, order_ref text, trace_id text, span_id text, origin text, attrs jsonb)`, with indexes on `(delivery_id, occurred_at)`, `(trace_id)`, `(observed_at)` and `(source, target, observed_at)`
  - `mc_order(tenant_id, environment, order_ref, delivery_id, created_at, origin, pk(tenant_id, environment, order_ref))`
  - `mc_self(source text pk, last_seen timestamptz, lag_ms bigint, drops bigint, redacted bigint, storage_errors bigint)`
  - `mc_downtime(started_at, ended_at)`, written at startup from the last heartbeat, which gives the pre-start gap
- Produces: `store.Open(ctx, url) (*Store, error)`, `(*Store).InsertEvents(ctx, []Event) error` (batched), `(*Store).DeleteOlderThan(ctx, time.Time) (int64, error)`, and the type `store.Event`, which mirrors `mc_event`.
- Compose:
  - `mc-backend`, `mc-postgres` (the same image family as DT's Postgres, own volume `mc-pg`), `mc-web` (static build served by `mc-backend` under `/`; a Vite dev server only for `make mc-web-dev`)
  - networks: `default` plus `dt_default` as `external: true`, aliased `mc-backend` on it so the collector's exporter resolves it
  - ports: `127.0.0.1:8095:8095` only

- [ ] **Step 1: Write failing tests:** `TestLoadRefusesSimulationOutsideLocal`, `TestLoadRefusesNonSandboxKey`, `TestDefaultsBindLoopback` (asserts `HTTPAddr` starts with `127.0.0.1:`), `TestInsertAndDeleteOlderThan` (testcontainers Postgres via `backend/pkg/testkit.StartPostgres` or a plain container).
- [ ] **Step 2:** `cd observability/mission-control/backend && go test ./internal/... -v`. Expected: FAIL.
- [ ] **Step 3:** Implement the files above. `main.go` wires config → store → HTTP `/livez` + `/readyz`. README and CLAUDE.md state the design's rules: no DT writes outside simulation, redaction, no Docker socket.
- [ ] **Step 4:** Run the tests. Then, with DT running, `make mc && curl -s 127.0.0.1:8095/readyz`. Expected: PASS and `ok`.
- [ ] **Step 5:** Commit: `feat(mc): Mission Control module, config, store and compose skeleton`.

### Task 2: Topology

**Files:**
- Create: `backend/internal/topology/topology.go`, `backend/internal/topology/topology_test.go`

**Interfaces:**
- Produces:
  - `type Component struct{ ID, Label, Zone, Parent string; Instrumented bool; Status string }`. `Zone` is one of `outside|edge|services|messaging|data`. `Status`: `""` or `"not_built"`.
  - `type Wire struct{ ID, From, To, Channel string }`. `Channel` is one of `rpc|nats|redis|http|ws_sse|sql|osrm`.
  - `topology.Default() Topology` with `Components []Component` and `Wires []Wire`.
  - `(Topology).Resolve(service, spanName string, attrs map[string]string) (componentID string)`, which maps a span to a component or working part.
- Contents (design §5):
  - outside: `partner`, `courier`, `viewer` (`Instrumented:false` for `courier` and `viewer` browsers)
  - edge: `gateway` with parts `gateway.partner_api`, `gateway.courier_api`, `gateway.viewer_api`, `gateway.rate_limiter`
  - services: `deliveries` (`.rpc`, `.state_machine`, `.outbox`, `.consumers`), `tracking` (`.ingest`, `.consumers`, `.state`, `.hub`, `.sweeper`), `accounts`, `webhooks` (`Status:"not_built"`)
  - messaging: `nats` with `nats.ACCOUNTS`, `nats.DELIVERIES`, `nats.TRACKING`, `nats.GATEWAY`
  - data: `redis`, `pg.deliveries`, `pg.tracking`, `pg.accounts`, `pg.gateway`, `osrm`
  - wires for every channel named in design §5 and the PR 1 spans

- [ ] **Step 1: Write failing tests:**
  - `TestEveryWireEndsExist`
  - `TestResolveTrackingSpans`: `tracking.ingest` → `tracking.ingest`, `tracking.viewer_fanout` → `tracking.hub`, `publish tracking.x` → `tracking.outbox`, `consume deliveries.x` with consumer group `tracking-deliveries` → `tracking.consumers`
  - `TestWebhooksNotBuilt`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(mc): static topology with working parts`.

### Task 3: Normalize and redact

**Files:**
- Create: `backend/internal/normalize/{envelope.go,span.go,log.go,allow.go}` and tests beside them
- Create: `backend/internal/redact/redact.go`, `redact_test.go`

**Interfaces:**
- Consumes: `topology.Topology.Resolve`; `store.Event`; `eventsv1.Envelope` and the payload types from `github.com/raduolivas/dt/backend/gen/dt/events/v1`; `logx.SensitiveKeys()` from `github.com/raduolivas/dt/backend/pkg/logx`.
- Produces:
  - `normalize.FromEnvelope(subject string, env *eventsv1.Envelope, observedAt time.Time) (store.Event, error)`. `kind=message_published` (the stream's view). `source` is the owning service from the subject prefix. `target` is `nats.<STREAM>`. `delivery_id` comes from `Subject`. `order_ref` is the `external_id` on `DeliveryCreated` only. `origin=simulated` when `env.Source=="simulator"` or the delivery id is in the simulation registry (Task 13).
  - `normalize.Allowed`: a `map[string][]string` from payload full name to allowed fields:
    - `DeliveryCreated`: `delivery_id, external_id, delivery_model, status, pickup_policy`
    - `DeliveryStatusChanged`: `delivery_id, from, to, actor`
    - `CourierAssigned`: `delivery_id, courier_id`
    - `CourierUpserted`: `courier_id, vehicle` (never `display_name` or `photo_url`)
    - `DeliveryUpdated`: `delivery_id, changed_fields` (never `dropoff`)
    - tracking events: every scalar except timestamps, which become `occurred_at`
    - any payload not listed: no attrs, `reason="unknown_payload"`
  - `normalize.FromSpan(resource map[string]string, span *tracepb.Span) []store.Event`:
    - one `request` event, or `message_consumed` for Consumer spans, or `state_change` for `tracking.apply` and `deliveries.*`
    - plus one `log` event per span event in `fix_dropped`, `snapshot_publish_failed`, `state_conflict`
    - `outcome`: Error status → `failed`; HTTP 401/403 → `rejected` with `reason=unauthorized`; 429 → `rejected` with `reason=rate_limited`; `fix_dropped` → `dropped` with the event's `reason`; duration over the `slow` threshold → `slow`
  - `normalize.FromLog(resource map[string]string, rec *logspb.LogRecord) store.Event` (`kind=log`; `attrs` = the redacted body + attributes)
  - `redact.Attrs(in map[string]any) (out map[string]any, dropped int)`: deletes keys whose segments hit the sensitive list (the same split rule as `logx.isSensitive`: `_ . -`), and values matching `^(dt_sk_|dt_pk_)`.

- [ ] **Step 1: Write failing tests:**
  - `TestEveryPayloadTypeIsAllowListed`: iterate every message in the `eventsv1` file descriptors with all fields set, including `LatLng`, names and photo. Normalize each and assert that no coordinate, `display_name`, `photo_url`, `pickup_name` or `notes` value appears anywhere in the marshalled `store.Event`.
  - `TestOrderRefFromDeliveryCreated`
  - `TestSimulatorSourceIsSimulated`
  - `TestSpanOutcomes`, table-driven: error, 401, 429, `fix_dropped reason=delivery_reassigned`, slow
  - `TestRedactDropsSensitiveKeysAndSecretValues`: also asserts the dropped count
  - `TestLogKeepsTraceID`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement. Unpack payloads with `env.Payload.UnmarshalNew()`. Copy fields by protoreflect, by allow-list.
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(mc): normalize DT events, spans and logs with allow-list redaction`.

### Task 4: Ingest — NATS consumers, OTLP receiver, pollers, self-health

**Files:**
- Create: `backend/internal/ingest/{nats.go,otlp.go,poll.go,self.go}` and tests

**Interfaces:**
- Consumes: Task 3 normalizers, `store.InsertEvents`, `config.Config`.
- Produces:
  - `ingest.RunNATS(ctx, js jetstream.JetStream, st *store.Store, n Normalizer) error`
    - creates or updates the durable consumers `mc-accounts`, `mc-deliveries`, `mc-tracking` and `mc-gateway`
    - `DeliverPolicy`: `All` for the 7-day streams; for ACCOUNTS, `ByStartTime` = now − retention
    - `AckPolicy` Explicit; it acks after insert
    - an invalid envelope: ack, count a drop, and log once per minute
  - `ingest.OTLPHandler(st, n) http.Handler`
    - `POST /v1/traces` and `POST /v1/logs` (protobuf; JSON optional)
    - body cap 4 MiB
    - an insert buffer of 10 000 events; when full, drop and count `drops`
    - always answers 200 (partial success) so the collector never retries
  - `ingest.PollHealth(ctx, targets map[string]string, every time.Duration, sink func(store.Event))`: polls `/livez` and `/readyz` every 5 s and emits a `health` event only when the state changes
  - `ingest.PollConsumers(ctx, monitorURL string, every time.Duration, sink func(store.Event))`: reads `/jsz?consumers=true` every 5 s and emits `health` events for consumer pending and ack-pending
  - `ingest.Self`: counters `drops`, `redacted`, `storage_errors` and `lag_ms` per source, flushed to `mc_self` every 5 s, and a heartbeat that writes `mc_downtime` on start

- [ ] **Step 1: Write failing tests** (testcontainers NATS + Postgres):
  - `TestCatchUpFromStreamHistory`: publish 3 DT envelopes **before** starting `RunNATS`, start it, and all 3 rows appear.
  - `TestDoesNotDisturbDTConsumers`: create a durable `tracking-deliveries` on DELIVERIES, publish 5 messages, run Mission Control's consumer to completion, and DT's consumer still has 5 pending.
  - `TestOTLPHandlerAlways200AndCountsDrops`: buffer size 1, send 3 spans; the answer is 200 and `drops` ≥ 1.
  - `TestHealthPollEmitsOnChangeOnly`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement. Wire everything into `main.go` (OTLP listener on `OTLPAddr`).
- [ ] **Step 4:** Run the tests. Then, with DT + collector running, `make mc && make courier-link` and query `mc_event` for rows with that delivery id from both NATS and OTLP sources.
- [ ] **Step 5:** Commit: `feat(mc): ingest NATS streams, OTLP and health with self-health`.

### Task 5: Order index, queries and retention

**Files:**
- Create: `backend/internal/index/index.go`, `backend/internal/store/queries.go` (sqlc, or hand-written pgx following DT style) and tests

**Interfaces:**
- Produces:
  - `index.OnEvent(ctx, e store.Event) error`, which upserts `mc_order` from `DeliveryCreated`
  - `store.SearchOrder(ctx, q string) ([]OrderHit, error)`: an exact `order_ref`, or `delivery_id` when `q` starts with `del_`; returns `{TenantID, Environment, OrderRef, DeliveryID, Origin, CreatedAt}`
  - `store.Timeline(ctx, deliveryID string, after int64, limit int) ([]store.Event, next int64, error)`: the events for the delivery **plus** the events sharing their `trace_id`s; keyset by `id`; limit ≤ 500
  - `store.Downtime(ctx, from, to time.Time) ([]Interval, error)`
  - `retention.Run(ctx, st, every=time.Hour, keep=7*24h)`

- [ ] **Step 1: Write failing tests:**
  - `TestSearchByOrderRef`
  - `TestSearchAmbiguousOrder`: the same `external_id` in two tenants → 2 hits
  - `TestSearchByDeliveryID`
  - `TestTimelineIncludesTraceSiblings`: a span event with no `delivery_id` but a shared trace id is included
  - `TestTimelinePagination`
  - `TestRetentionDeletesOld`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(mc): order index, timeline queries and retention`.

### Task 6: API and live stream

**Files:**
- Create: `backend/internal/api/{api.go,sse.go,auth.go}` and tests

**Interfaces:**
- Produces, HTTP JSON under `/api` (all GET except under `/api/sim`):
  - `/api/topology` → `topology.Default()` plus the current component states
  - `/api/orders?q=` → `[]OrderHit`
  - `/api/orders/{delivery_id}` → `{order, statusSteps: [{status, at, state: done|warning|pending}], detectors: [...]}`
  - `/api/orders/{delivery_id}/timeline?after=&limit=` → `{events, next, gaps: [{from, to, kind: "mc_down"|"no_trace"}]}`
  - `/api/components/{id}?delivery_id=` → `{health: {liveness, readiness, dependencies, business}, recent: []Event}`
  - `/api/components/{id}/logs?delivery_id=&level=&after=` → logs (this order by default; all of the component when `delivery_id` is empty)
  - `/api/events/{id}` → one event plus its log lines (same `trace_id` + `span_id`)
  - `/api/system` → aggregate traffic per wire (Task 7 source) plus recent orders and active warnings
  - `/api/self` → the `mc_self` rows
- Produces: `GET /api/stream?delivery_id=` (SSE)
  - every message has `id: <mc_event.id>`, event `event`, `detector` or `gap`
  - on `Last-Event-ID` it replays from the store; if the id is older than retention, it first sends a `gap` event `{from, to}`
  - a server keep-alive comment every 15 s
  - at most 200 events/s per client, with the excess bundled into one `bundle` event `{wire, count, outcome}`
- Produces: `api.AuthSeam(next http.Handler) http.Handler` (a no-op, with a comment pointing at MC-6), and `Content-Security-Policy: default-src 'self'` on every response.

- [ ] **Step 1: Write failing tests:**
  - `TestSSEResumesFromLastEventID`
  - `TestSSEGapMarkerWhenResumeTooOld`
  - `TestSSEBundlesAboveRate`
  - `TestTimelineMarksPreStartGap`: an `mc_downtime` row inside the order's span appears in `gaps` as `mc_down`
  - `TestStatusStepsFromEvents`: confirmed, courier_assigned and picked_up done; a stale signal → warning
  - `TestCSPHeader`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(mc): JSON API and SSE live stream with gaps and bundling`.

### Task 7: Metrics source adapters and "open in" links (MC-14, v1 half)

**Files:**
- Create: `backend/internal/metrics/{source.go,builtin.go,prometheus.go,links.go}` and tests

**Interfaces:**
- Produces:
  - `type Source interface{ WireTraffic(ctx, window time.Duration) ([]WireStat, error); Name() string }`, where `WireStat{WireID string; Rate, ErrorRate, P95ms float64}`
  - `metrics.New(cfg) Source` with three choices:
    - `builtin` aggregates `mc_event`
    - `prometheus` queries `PrometheusURL` `/api/v1/query` with PromQL templates kept in `metrics/promql.yaml`. The metric names follow OBS §5.2; until OBS ships them, this adapter returns an error and falls back.
    - `cloudwatch` returns `ErrNotImplemented` (staging, later)
  - `metrics.WithFallback(primary, builtin Source) Source`: on error, it uses builtin and sets self-health `metrics_source_unavailable=true`
  - `links.Render(templates map[string]string, kind string, vars map[string]string) []Link` for `grafana_trace`, `grafana_logs`, `xray_trace` and `cloudwatch_logs`. A link appears only when its template is set. Vars are URL-escaped.

- [ ] **Step 1: Write failing tests:** `TestFallbackOnPrometheusError`, `TestBuiltinRatesFromEvents`, `TestLinksOnlyWhenConfigured`, `TestLinkVarsEscaped`.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement. Wire it into `/api/system` and `/api/components/{id}` (field `links`).
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(mc): metrics source adapters and open-in links`.

### Task 8: Detectors

**Files:**
- Create: `backend/internal/detect/{detect.go,rules.go}`, `config/thresholds.yaml`, tests

**Interfaces:**
- Produces:
  - `type Finding struct{ Detector, Hop, DeliveryID, Reason, Severity string; Since time.Time; Evidence []int64 }`. `Severity` is `warning` or `failure`. `Evidence` holds `mc_event` ids.
  - `detect.Run(ctx, st, every=5*time.Second, emit func(Finding))`
  - The rules, with defaults from design §7:
    - `courier_stale`: amber at 30 s, red at 300 s; active delivery and no accepted fix
    - `fixes_dropped`: grouped by reason
    - `rejected_at_gateway`
    - `stuck_status`: per-status limits in the YAML
    - `processed_not_published`: 30 s
    - `consumer_lagging`: pending > 0 and not shrinking for 60 s
    - `snapshot_not_delivered`
    - `storm`: > 10 per minute, same reason, same wire
    - `mc_behind`: lag > 10 s or drops > 0
  - Findings are stored as `kind=detector` events, so they replay too.

- [ ] **Step 1: Write failing tests:** one fires/stays-quiet pair per rule, using fixture events (fake clock).
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(mc): detectors for missing activity`.

### Task 9: Web scaffold, cockpit layout, data layer

**Files:**
- Create: `observability/mission-control/web/`. This is its **own** pnpm project, not part of DT's `web/` workspace. It has `package.json` (`packageManager` the same as `web/`), `vite.config.ts`, `tsconfig.json`, `eslint.config.js`, `index.html`, and `src/{main.tsx,App.tsx,api/client.ts,api/stream.ts,state/store.ts,layout/Cockpit.tsx,theme.ts}`
- Test: `src/**/*.test.ts(x)` (Vitest + Testing Library)

**Interfaces:**
- Consumes: Task 6 API shapes. Mirror them in `src/api/types.ts`.
- Produces:
  - `api/client.ts`: typed fetchers, one per endpoint
  - `api/stream.ts`: `openStream(deliveryId?, onMsg)`, with reconnect, `Last-Event-ID` and exponential backoff capped at 10 s
  - `state/store.ts`: one store, holding mode `live|paused|replay`, selected order, selection `{kind: component|wire|event, id}`, the event buffer (capped at 20 000), gaps, findings, panel collapse flags, reduced motion
  - `theme.ts`: tokens `accent #22d3ee`, `ok #4ade80`, `warn #f59e0b`, `fail #ef4444`, `sim #a78bfa`, a dark background, monospace for data
- `Cockpit.tsx`: top bar, left panel, centre, right panel, bottom timeline, each collapsible (MC-7, MC-7a).
- `bundlesBurstsAboveThreshold` lives in `state/pulses.ts`: `toPulses(events, now) → Pulse[]`. Over 20 events per wire per second, it emits one bundle pulse `{count}`.

- [ ] **Step 1: Write failing tests:**
  - `streamResumesWithLastEventId`
  - `storeCapsBuffer`
  - `bundlesBurstsAboveThreshold`
  - `cockpitCollapsesPanels`
- [ ] **Step 2:** `pnpm -C observability/mission-control/web test`. Expected: FAIL.
- [ ] **Step 3:** Implement. `mc-backend` serves the built `dist/` at `/` (Task 1 compose).
- [ ] **Step 4:** Run the tests. Expected: PASS. Then run `pnpm build`.
- [ ] **Step 5:** Commit: `feat(mc-web): cockpit layout and data layer`.

### Task 10: The 3D scene

**Files:**
- Create: `web/src/scene/{Scene.tsx,Platform.tsx,ComponentCube.tsx,Wire.tsx,Pulses.tsx,camera.ts,visualState.ts}` and tests

**Interfaces:**
- Consumes: `/api/topology`, store selection and events, `toPulses`.
- Produces:
  - `visualState(component, events, now) → 'active'|'idle'|'not_instrumented'|'stale'|'warning'|'unhealthy'|'not_built'`
  - `wireStyle(wire, evidence) → 'measured'|'inferred'|'quiet'` (dotted when inferred)
  - `pulseColor(outcome) → ok|warn|fail`
- The scene: an isometric orthographic camera over five platforms (§6.2 order: outside, edge, services, messaging, data). Cubes are labelled. Wires are glowing curves. Pulses travel along them at a fixed visual speed (they are not latency).
- Controls: orbit, pan, zoom and reset. With an order searched, untouched parts dim to 35 %.
- Reduced motion (`prefers-reduced-motion` or the toggle): pulses become static markers with counts, and the camera does not tween.

- [ ] **Step 1: Write failing tests** (pure functions; no WebGL in unit tests):
  - `everyVisualStateDistinct`: each state maps to a distinct `{color, dash, opacity}` triple
  - `notInstrumentedIsNotIdle`
  - `inferredWireIsDotted`
  - `pulseColorsTrafficLight`
  - `reducedMotionHasNoTravellingPulses`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then a manual check with `make mc` + `make courier-link`, posting fixes: green pulses travel courier → gateway → tracking → redis → viewer. Take a screenshot for the PR.
- [ ] **Step 5:** Commit: `feat(mc-web): 3D zones scene with honest pulses and states`.

### Task 11: Order view — search, status steps, detectors, timeline and replay

**Files:**
- Create: `web/src/order/{Search.tsx,OrderPanel.tsx,Timeline.tsx,replay.ts}` and tests

**Interfaces:**
- Consumes: `/api/orders`, `/api/orders/{id}`, `/timeline`, the stream.
- Produces:
  - The search box: one hit opens the order; several hits show a picker with tenant · environment · created.
  - The left panel shows the status steps (✓ done, ▲ warning, ○ pending; including `cancelled`/`failed`), then the detectors.
  - The SIMULATED badge comes from `origin`.
  - The timeline shows ticks per event, coloured by outcome. Gaps are hatched bands labelled "Mission Control was not running" or "no trace data".
  - Dragging the timeline enters replay: a REPLAY banner with the time, and the scene shows only events ≤ the cursor.
  - `replay.ts`: `eventsAt(buffer, t)`, which never calls the API's write paths (there are none outside `/api/sim`).
- With no order searched, the system view shows aggregate traffic from `/api/system` and lists recent orders and active warnings.

- [ ] **Step 1: Write failing tests:**
  - `searchShowsPickerForAmbiguous`
  - `rendersPartnerStringsAsText`: an order ref `<img src=x onerror=alert(1)>` renders literally, with no element created
  - `gapBandsLabelled`
  - `replayFiltersByCursor`
  - `simulatedBadgeFromOrigin`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(mc-web): order search, status steps, timeline and replay`.

### Task 12: Inspector, click-to-expand and logs ↔ pulses

**Files:**
- Create: `web/src/inspector/{Inspector.tsx,OverviewTab.tsx,LogsTab.tsx,MessagesTab.tsx,HealthTab.tsx,OpenIn.tsx}`, `web/src/scene/Expanded.tsx`, plus tests

**Interfaces:**
- Consumes: `/api/components/{id}`, `/logs`, `/api/events/{id}`, `links`.
- Produces:
  - Clicking a component tweens the camera to its platform and renders its working parts (from topology `Parent`) with their own wires. The breadcrumb reads `system › order › <component>`.
  - Tabs: Overview, Logs, Messages, Health.
  - Logs: default "this order", toggle "all of component", a warn+ filter, and follow.
  - Clicking a pulse selects its event: the Messages tab shows time, duration, outcome, trace id and the log lines.
  - Clicking a log line selects its event and highlights the pulse and wire.
  - Health shows liveness, readiness, dependencies and business on separate lines.
  - `OpenIn` shows links only when present.
  - Everything is read-only, and partner strings render as text.

- [ ] **Step 1: Write failing tests:**
  - `logLineSelectsItsPulse`
  - `pulseSelectsItsLogs`
  - `logsDefaultToThisOrder`
  - `healthLinesSeparate`
  - `openInHiddenWithoutLinks`
  - `breadcrumbReturnsToOrder`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then a manual check: expand tracking during a courier-link run and see `ingest_fixes_dropped` logs when the delivery is reassigned.
- [ ] **Step 5:** Commit: `feat(mc-web): inspector tabs, expand view and log-pulse linking`.

### Task 13: Simulation mode — fake phone, headless viewer, break scenarios

**Files:**
- Create: `backend/internal/sim/{sim.go,phone.go,viewer.go,registry.go,scenarios.go}` and tests
- Create: `observability/mission-control/scripts/mc-break.sh` (the Docker-side breaks)
- Modify: root `Makefile`:
  - `mc-sim`: ensure a sandbox key exists, reusing `dtctl` like `scripts/courier-link.sh` and writing it to `observability/mission-control/.env` as `MC_SANDBOX_KEY`; restart `mc-backend` with `MC_SIMULATION=1`; `POST /api/sim/runs`; print the order number and the tracking link
  - `mc-break SCENARIO=<1..6>`
- Modify: `web/src/order/OrderPanel.tsx` (break buttons, shown only when `/api/sim` is enabled)

**Interfaces:**
- Consumes: DT's public APIs, the same sequence as `scripts/courier-link.sh`:
  - `PUT /v1/couriers/{external_id}`, `POST /v1/deliveries`, `POST /v1/deliveries/{id}/assignment`
  - `POST /v1/courier-sessions` (`mode: "link"`), then exchange the link at `POST /v1/courier/token` (the builder reads `gateway/.../http/courier.go` `token` for the request shape)
  - `POST /v1/courier/locations` with fixes along a straight-line route at 1 fix/s
  - viewer: the delivery's `tracking.url` → `POST /v1/tracking/session` → `GET /v1/tracking/stream/sse` (through the edge, the same origin the tracking page uses)
- Produces, routes (mounted only when `cfg.Simulation`):
  - `POST /api/sim/runs` → `{delivery_id, order_ref, tracking_url}`
  - `POST /api/sim/runs/{id}/break` with `{scenario: 1|2|3|4}`
  - `POST /api/sim/runs/{id}/heal`
- `sim.Registry`: the simulated delivery ids, read by the normalizer (Task 3) to set `origin=simulated`.
- Scenarios:
  1. **phone stops:** the phone pauses
  2. **invalid token:** the phone sends with a corrupted JWT → 401s
  3. **rate limit:** the phone sends at 20/s → 429s
  4. **reassigned:** assign the delivery to a second courier while the phone keeps sending → `delivery_reassigned` drops
  5. **Redis down** and 6. **OSRM stopped:** run by `mc-break.sh`, which calls `docker compose -p dt pause redis` / `stop osrm-car osrm-bike` and their heals. **They are never run by `mc-backend`.**
- Scenario 4 changes DT data inside the sandbox tenant only, created by this run.

- [ ] **Step 1: Write failing tests** (unit tests use an `httptest` fake gateway; integration tests run in `make mc-e2e`):
  - `TestSimRoutesAbsentWhenDisabled`
  - `TestPhonePostsThroughGateway`: the fake gateway sees `POST /v1/courier/locations` with `Authorization: Bearer <courier jwt>`
  - `TestScenarioInvalidTokenSends401Path`
  - `TestScenarioRateLimitRate`
  - `TestSimOnlyUsesTestKey`
  - `TestRegistryMarksSimulated`
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(mc): simulation mode with fake phone, viewer and break scenarios`.

### Task 14: The milestone, end to end; docs and PR

**Files:**
- Create: `observability/mission-control/e2e/milestone_test.go` (build tag `mce2e`, run by `make mc-e2e`, against `make dev` + `make mc`)
- Modify: `observability/mission-control/README.md`:
  - start order: `make dev`, then `make mc`, then `make mc-sim`
  - the example order
  - each break scenario and what to expect
  - limitations: browser hops not instrumented, no trace data before start, webhooks not built, local only
- Modify: `docs/PROJECT_CONTEXT.md` and the brainstorm state file (PR 2 status)

- [ ] **Step 1: Write the test** `TestMilestone`:
  - create a sim run and wait until `/api/orders?q=<order_ref>` finds it
  - the timeline holds `DeliveryCreated`, a status change, `tracking.ingest` and `tracking.viewer_fanout` sharing one trace id with the gateway courier span
  - for scenarios 1–4, break → within 40 s a Finding names the hop and reason:
    - 1: `courier_stale` at hop `courier`
    - 2: `rejected_at_gateway`, reason `unauthorized`
    - 3: `rejected_at_gateway`, reason `rate_limited`
    - 4: `fixes_dropped`, reason `delivery_reassigned` at hop `tracking.ingest`
    - then heal
  - scenarios 5 and 6 via `mc-break.sh` → findings at `tracking.state`/`redis` and `osrm`
- [ ] **Step 2:** `make dev && make mc && make mc-e2e`. Expected: FAIL first on any missing piece, then fix.
- [ ] **Step 3:** Isolation: `make mc-down && make smoke`. Expected: PASS. DT is untouched by Mission Control's absence.
- [ ] **Step 4:** Run `make ci`. Expected: green. Record a short screen capture (search → live pulses → scenario 4 → inspector showing `delivery_reassigned`) for the PR.
- [ ] **Step 5:** Commit, push `mission-control`, and open a PR with the evidence. The body ends with the Claude Code attribution line.
