# MC-PR1: DT telemetry groundwork for Mission Control — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** DT exports traces and logs to one local OTel collector. Every hop of a delivery (gateway → RPC → state change → outbox → NATS → Redis snapshot → viewer fan-out) joins one trace or names its reason for stopping. Mission Control (PR 2) can then show where a courier's location stopped.

**Architecture:** `otelx` gains an OTLP log provider. `logx` tees its redacted records to it. A hand-written Connect client interceptor carries trace context across internal RPCs. The outbox relay gets producer spans. Tracking and deliveries get spans and drop reasons. The Redis viewer snapshot is wrapped in an envelope that carries `traceparent`. One `otel/opentelemetry-collector-contrib` container in `observability/collector/` redacts and forwards to Mission Control. It is on by default in `make dev`, and `OBS=0` skips it.

**Tech Stack:** Go 1.27 (`backend/go.work`), OpenTelemetry Go SDK (trace + log), `otelslog` bridge, `redisotel` v9, connect-go v2 (`connectrpc.com/connect/v2`), NATS JetStream, Docker Compose, OTel Collector contrib.

**Spec:** `docs/superpowers/specs/2026-11-05-mission-control-design.md` (§4.1, §4.6, §8 D1–D8). OBS draft `2026-11-04-observability-design.md` §4–§7 for the collector shape this slice follows.

Branch: `mc-groundwork` from `main`. It is the first of two PRs; the app is `2026-11-05-mc2-mission-control-app.md`.

## Global Constraints

- **No endpoint, no export:** with `OTEL_EXPORTER_OTLP_ENDPOINT` unset or empty, nothing is exported. Spans, trace ids and the stdout JSON logs still work.
- **Never block or fail DT:** exporters use bounded queues and drop when full, and export errors are never returned to handlers.
- **Shutdown** flushes traces and logs with a 5 s bound.
- **Span attributes allowed for DT ids:** `dt.tenant_id`, `dt.environment`, `dt.delivery_id`, `dt.event_id`, `dt.request_id`. Nothing in the `logx` sensitive list. No coordinates, ever. Redis spans record no statements or arguments.
- **Metrics are out of scope** (they belong to OBS).
- **Collector:** `memory_limiter → attributes/redact → resource → batch`. The redact processor **deletes** keys from `logx`'s sensitive list. The Mission Control exporter is `otlphttp/mission-control`, with retry off and a bounded queue.
- **Local exposure:** the collector's OTLP ports are not published to the host. Mission Control reaches it on the compose network `dt_default`.
- Every task keeps `make lint-go test-go` green. Nothing in DT's runtime may depend on Mission Control.

## Review Focus

1. **Collector up, Mission Control absent** (the normal state today): `make dev && make smoke` must pass, and the collector only logs export errors → covered by Task 7 Step 5.
2. **`OBS=0 make dev`:** the services must start with no endpoint and no export errors in their logs → Task 7 Step 6.
3. **A pub/sub message in the old bare-snapshot form** (a tracking replica still on the old build during a rolling deploy) must still reach viewers → Task 6 test `TestFeedAcceptsBareSnapshot`.
4. **An incoming `traceparent` on the public gateway** must still start a new trace (`PublicEndpoint`); internal RPC must not be affected by that → Task 3 test `TestPublicEndpointStillStartsNewTrace`.
5. **A log record holding a sensitive key** must reach the OTLP bridge already redacted, and `trace_id` must still be present → Task 2 test `TestOTLPRecordIsRedacted`.

---

### Task 1: `otelx` log provider and combined shutdown

**Files:**
- Modify: `backend/pkg/otelx/otelx.go`
- Modify: `backend/pkg/bootstrap/bootstrap.go:150-160` (order: `otelx.Init` before `logx.New`; an `Init` error is written to stderr as one JSON line, then returned)
- Modify: `backend/services/tracking/cmd/tracking/main.go` if it calls `otelx.Init` / `logx.New` itself (check; keep the same order)
- Test: `backend/pkg/otelx/otelx_test.go`

**Interfaces:**
- Produces: `otelx.Init(ctx, service string) (shutdown func(context.Context) error, err error)`, with the signature unchanged. It now also installs the global `LoggerProvider` (`go.opentelemetry.io/otel/log/global`), with an `otlploghttp` exporter and a batch processor when the endpoint is set. The resource gets `service.name` plus `deployment.environment` from `DT_ENVIRONMENT` (default `local`). `shutdown` flushes both providers with a 5 s bound.
- Produces: `otelx.Enabled() bool`, which reports whether `OTEL_EXPORTER_OTLP_ENDPOINT` is non-empty.

- [ ] **Step 1: Write failing tests**
  - `TestInitWithoutEndpointInstallsNoExporter`: with the endpoint unset, `Init` succeeds, `Enabled()==false`, and `global.GetLoggerProvider()` returns a working no-op provider.
  - `TestInitWithEndpointExportsLogs`: start an `httptest.Server` that accepts `POST /v1/logs` and counts requests. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to it, run `Init`, emit one record through `otelslog.NewLogger("t")`, call `shutdown`, and assert the count is ≥ 1.
  - `TestShutdownIsBounded`: the endpoint points at a server that sleeps 30 s. `shutdown` returns within 6 s.
- [ ] **Step 2:** `cd backend/pkg && go test ./otelx/ -run 'TestInit|TestShutdown' -v`. Expected: FAIL.
- [ ] **Step 3:** Implement the changes in `otelx.go`.
- [ ] **Step 4:** Run the tests. Expected: PASS. Then run `make test-go-short`.
- [ ] **Step 5:** Commit: `feat(otelx): export logs over OTLP and bound shutdown`.

### Task 2: `logx` tees redacted records to OTLP

**Files:**
- Modify: `backend/pkg/logx/logx.go`, `backend/pkg/logx/redact.go` (export the list)
- Test: `backend/pkg/logx/logx_test.go`

**Interfaces:**
- Consumes: `otelx.Enabled()`. Do **not** import otelx from logx if that creates a cycle (otelx imports httpx, check). Read the env var directly through a small `endpointSet()` helper in logx instead.
- Produces: `logx.New(w io.Writer, level slog.Level, service string) *slog.Logger`, unchanged. When the endpoint is set, the redacting handler wraps a **fan-out** handler: the JSON handler plus `otelslog.NewHandler(service)`. Both outputs therefore get identical redacted records, including the `trace_id` and `span_id` that `redactingHandler.Handle` adds.
- Produces: `logx.SensitiveKeys() []string`, which returns the sorted union of `sensitiveSegments` and `sensitiveKeys`. Task 7 uses it to guard the collector config against drift.

- [ ] **Step 1: Write failing tests**
  - `TestOTLPRecordIsRedacted`:
    - Install an in-memory `sdklog` exporter as the global provider.
    - Build `logx.New` with the endpoint env set (to any value; the exporter is the in-memory one).
    - Log `"x"` with `lat=52.1` and `api_key="dt_sk_test_x"` inside a context that carries a valid span.
    - Assert that the exported record's `lat` and `api_key` values equal `logx.Redacted`.
    - Assert that `trace_id` equals the span's trace id.
  - `TestNoEndpointNoTee`: with the endpoint unset, the in-memory exporter receives nothing and stdout JSON is unchanged.
  - `TestSensitiveKeysSortedAndComplete`: the result contains `lat`, `lng`, `phone`, `token`, `api_key`, and is sorted.
- [ ] **Step 2:** `cd backend/pkg && go test ./logx/ -v`. Expected: FAIL.
- [ ] **Step 3:** Implement the `fanout` slog.Handler in `logx.go`. `Enabled` = any child enabled. `WithAttrs` and `WithGroup` are applied to both children. `Handle` calls both and joins the errors.
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(logx): tee redacted records to the OTLP log bridge`.

### Task 3: Trace context across internal Connect calls (D6)

**Files:**
- Create: `backend/pkg/otelx/connect.go`
- Modify: `backend/services/gateway/cmd/gateway/main.go:253-262` and `backend/services/tracking/internal/adapters/outbound/deliveriesrpc/client.go:47-56`. Pass the new interceptor **before** `auth` in `connect.NewClient(..., otelx.ConnectClientInterceptor(), auth)`.
- Test: `backend/pkg/otelx/connect_test.go`

**Interfaces:**
- Produces: `otelx.ConnectClientInterceptor() connect.ClientInterceptor` (connect v2 API). Per unary call it starts a client span named after the procedure (`/dt.deliveries.v1.DeliveriesService/Report`), kind Client, with `rpc.system=connect_rpc`, `rpc.service` and `rpc.method`. It injects W3C context into the request headers with the global propagator. It sets error status from the connect error code (`rpc.connect_rpc.error_code`). Streaming calls get injection only, no span.
- No server interceptor: internal RPC servers already get an `otelhttp` server span from `otelx.HTTP`, which extracts `traceparent` because those listeners are not `PublicEndpoint`.

- [ ] **Step 1: Write failing tests**
  - `TestClientSpanIsParentOfServerSpan`:
    - Set up a `tracetest.SpanRecorder` provider.
    - Start an `httptest` server running `otelx.HTTP(handler, "svc")`, where `handler` is any connect handler from `backend/gen`. Use a tiny test service from gen, or a raw `http.HandlerFunc` that records `trace.SpanContextFromContext(r.Context())`.
    - Call it through a connect v2 client that uses the interceptor.
    - Assert that the server span's parent span id equals the client span's span id, and that both share a trace id.
  - `TestPublicEndpointStillStartsNewTrace`: the same call against `otelx.HTTP(h, "gw", otelx.PublicEndpoint())`. The server span has a **different** trace id and a link to the client span.
  - `TestClientSpanErrorStatus`: the handler returns `connect.CodeNotFound`. The client span status is Error and `rpc.connect_rpc.error_code=not_found`.
- [ ] **Step 2:** `cd backend/pkg && go test ./otelx/ -run Connect -v` (or by the test names). Expected: FAIL.
- [ ] **Step 3:** Implement `connect.go`. Wire the interceptor into the two client sites.
- [ ] **Step 4:** Run the tests, then `make test-go`. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(otelx): carry trace context across internal Connect calls`.

### Task 4: Outbox producer spans (D7)

**Files:**
- Modify: `backend/pkg/outbox/relay.go` (`publishBatch`, the loop at about lines 114-127)
- Test: `backend/pkg/outbox/outbox_test.go` (existing integration tests use `testkit.StartPostgres` / `StartNATS`)

**Interfaces:**
- Produces: for each published row there is one span `publish <subject>`, kind Producer. Its parent comes from the row's stored `headers` (extracted with the global propagator over `propagation.MapCarrier`). It has the attributes `messaging.system=nats`, `messaging.destination.name=<subject>` and `messaging.message.id=<event_id>`, plus `dt.event_id=<event_id>`. The headers written on the NATS message are **re-injected from the publish span**, so the consumer span (`inbox.go:123`) becomes its child. A publish failure sets Error status and records the error.

- [ ] **Step 1: Write failing tests**
  - `TestRelayPublishSpanParentsConsumer`:
    - Enqueue inside a context that carries a known span.
    - Run the relay once, then consume with `outbox.Consume`.
    - With a SpanRecorder, assert the chain: original span → `publish deliveries.x` → `consume deliveries.x`, all with the same trace id.
  - `TestRelayPublishSpanErrorOnFailure`: stop the NATS container (or use a JetStream stub returning an error). The publish span has Error status.
- [ ] **Step 2:** `cd backend/pkg && go test ./outbox/ -run RelayPublishSpan -v`. Expected: FAIL.
- [ ] **Step 3:** Implement the publish span in `relay.go`.
- [ ] **Step 4:** Run the tests. Expected: PASS.
- [ ] **Step 5:** Commit: `feat(outbox): producer span per published event`.

### Task 5: Tracking ingest drop reasons, apply spans, Redis spans; deliveries spans (D2, D5, D8)

**Files:**
- Modify: `backend/services/tracking/internal/app/ingest.go`, `backend/services/tracking/internal/app/service.go` (`Apply`/`applyOnce`)
- Modify: `backend/services/deliveries/internal/app/service.go` (`Create`, `Update`, `Assign`, `Report`, `ReportFor`, `Cancel`)
- Modify: `backend/pkg/bootstrap/bootstrap.go:204`: after `redis.NewClient`, call `redisotel.InstrumentTracing(deps.Redis, redisotel.WithDBStatement(false))` (`github.com/redis/go-redis/extra/redisotel/v9`).
- Test: `backend/services/tracking/internal/app/service_test.go` (fakes in `fakes_test.go`), `backend/services/deliveries/internal/app/service_test.go`, `backend/pkg/bootstrap/bootstrap_test.go` (or the nearest existing test file)

**Interfaces:**
- Produces, tracking:
  - `Ingest` starts span `tracking.ingest` with `dt.tenant_id` and `dt.environment`. At the end it sets the attributes `tracking.fixes.accepted` and `tracking.fixes.dropped`.
  - For every dropped fix group it adds a span event `fix_dropped` with `dt.delivery_id` (empty when there is no delivery), `reason` and `count`.
  - It also writes one `WarnContext` log `ingest_fixes_dropped` per (delivery, reason) with `delivery_id`, `reason` and `count`.
  - The reasons, exactly:
    - `no_active_delivery`: `len(ids)==0`
    - `delivery_final`: `st.Status.IsFinal()`
    - `delivery_reassigned`: `st.Courier == nil || st.Courier.ID != courierID`
    - the `domain.CheckFix` verdict reason for `Drop`: `invalid`, `future`, `speed`, `jump`
    - `state_update_failed`: the `Apply` error path
  - `TrailOnly` is not dropped. A fix kept by another delivery is not dropped.
  - `Apply` starts span `tracking.apply` with `dt.delivery_id`, `dt.tenant_id` and `dt.environment`. It adds the events `state_stored`, `state_conflict` (`!stored`), and `snapshot_published` or `snapshot_publish_failed` (Error status is **not** set; the apply succeeded).
- Produces, deliveries: each listed method starts span `deliveries.<method>` (lowercase: `deliveries.create`, `deliveries.report`, …) with `dt.tenant_id`, `dt.environment` and `dt.delivery_id`. `Report`/`ReportFor` also add `deliveries.outcome` (the `domain.Outcome` string: accepted / stale / duplicate / conflict).

- [ ] **Step 1: Write failing tests** (SpanRecorder plus the existing fakes)
  - `TestIngestDropReasons`, table-driven with one case per reason above. Assert the `fix_dropped` event's `reason` and `count`, and the `tracking.fixes.dropped` attribute.
  - `TestIngestDropLogHasNoCoordinates`: capture the logger output and assert that no `lat`, `lng` or position value appears.
  - `TestApplySpanEvents`: one case each for stored+published, conflict, and publish failure (fake publisher returns an error). Assert the events and that the span status is Unset.
  - `TestDeliveriesReportSpanOutcome`: a duplicate report yields `deliveries.outcome=duplicate`.
  - `TestRedisSpansHaveNoStatement`: bootstrap with a test Redis (`testkit.StartRedis`), `SET k v`. The recorded span has no `db.statement` attribute.
- [ ] **Step 2:** Run `go test ./internal/app/ -v` in each service dir and the bootstrap test. Expected: FAIL.
- [ ] **Step 3:** Implement. Collect reasons inside the `Apply` mutate closure into a per-delivery map, which is reset per attempt like `keptHere`.
- [ ] **Step 4:** Run the tests, then `make lint-go test-go`. Expected: PASS. The onion depguard rule must still pass: spans live in `app`, never in `domain`.
- [ ] **Step 5:** Commit: `feat(tracking,deliveries): spans, drop reasons and Redis tracing`.

### Task 6: Trace context on the viewer snapshot and the fan-out signal (D3, D4)

**Files:**
- Modify: `backend/services/tracking/internal/adapters/outbound/redis/pubsub.go`, `.../redis/feed.go` (`dispatch`, about line 247), `backend/services/tracking/internal/app/ports.go:187-197` (`FeedHandler`), `backend/services/tracking/internal/adapters/inbound/http/hub.go:329` (`Snapshot`), plus any other `FeedHandler` implementations or fakes (`grep -rn "Snapshot(deliveryID string" services/tracking`)
- Test: `.../redis/viewer_test.go`, `.../inbound/http/hub_test.go` (or the existing hub test file)

**Interfaces:**
- Produces: the pub/sub payload on `dt:snap:<delivery_id>` becomes `{"tp":"<traceparent>","s":<snapshot JSON>}`. `tp` may be empty.
- Produces: `FeedHandler.Snapshot(ctx context.Context, deliveryID string, snapshot []byte)`, which gains a `ctx` that carries the extracted remote span context.
- `Feed.dispatch` accepts **both** the envelope form and the legacy bare snapshot form (a JSON object with no `"s"` key) during rolling deploys.
- Produces: `Hub.Snapshot` starts span `tracking.viewer_fanout` (kind Internal, parent = extracted context) with `dt.delivery_id` and `tracking.viewers` = the number of local streams (0 included). It ends after the `offer` loop, and never blocks.

- [ ] **Step 1: Write failing tests**
  - `TestPublishWrapsTraceparent`: publish within a span, subscribe raw, and assert that `tp` matches the span's traceparent and `s` decodes to the snapshot.
  - `TestFeedAcceptsBareSnapshot`: publish a bare snapshot JSON on the channel. The handler receives the same bytes as before.
  - `TestFeedExtractsContext`: publish an envelope. The handler's ctx carries a remote span context with the published trace id.
  - `TestHubFanoutSpanCountsViewers`: with two open streams for a delivery, call `Snapshot`. There is one `tracking.viewer_fanout` span with `tracking.viewers=2` and the parent trace id.
- [ ] **Step 2:** Run the tests. Expected: FAIL.
- [ ] **Step 3:** Implement the changes.
- [ ] **Step 4:** Run the tests, then `make test-go` and `make latency`, after `make dev`. Fix-to-viewer p95 must stay < 1000 ms.
- [ ] **Step 5:** Commit: `feat(tracking): carry traceparent on viewer snapshots and count fan-out`.

### Task 7: Minimal collector, compose wiring, checks and docs (D1)

**Files:**
- Create: `observability/README.md`. It is short: what runs, how to turn it off (`OBS=0`), what Mission Control and OBS add later.
- Create: `observability/compose.yaml`: service `otel-collector`. The image is `otel/opentelemetry-collector-contrib` pinned to a current release tag plus digest. It is `read_only`, `cap_drop: [ALL]` and `no-new-privileges`, with **no published ports**. It runs `--config /etc/otelcol/base.yaml --config /etc/otelcol/exporters.local.yaml` and has a health check on extension `health_check` (port 13133, internal).
- Create: `observability/collector/base.yaml`:
  - receivers: `otlp` (http 4318, grpc 4317)
  - processors: `memory_limiter` (limit 200 MiB), then `attributes/redact` (`action: delete` for every key in `redact-keys.yaml`, plus `include` regex rules that delete any attribute whose **value** matches `^(dt_sk_|dt_pk_)`), then `resource` (`deployment.environment=local` upsert), then `batch`
  - pipelines `traces` and `logs`
- Create: `observability/collector/redact-keys.yaml`: the key list (one key per line, in the YAML list form the processor needs).
- Create: `observability/collector/exporters.local.yaml`: `otlphttp/mission-control` with endpoint `${env:MC_OTLP_ENDPOINT:-http://mc-backend:4318}`, `retry_on_failure.enabled: false`, `sending_queue.queue_size: 1000`. Add `debug` with `verbosity: basic` only when `OTELCOL_DEBUG=1`, as a separate file, so the default config needs no conditional.
- Modify: `Makefile`:
  - `COMPOSE` adds `-f observability/compose.yaml` unless `OBS=0`. With `OBS=0` it exports `DT_OTLP_ENDPOINT=` (empty).
  - New target `obs-check`, joined to `lint`.
- Modify: `infra/compose/compose.yaml`: every Go service gets `OTEL_EXPORTER_OTLP_ENDPOINT: ${DT_OTLP_ENDPOINT-http://otel-collector:4318}` and `DT_ENVIRONMENT: local`.
- Create: `scripts/obs-check.sh`:
  - `otelcol-contrib validate` on the merged config (docker run).
  - Then the redact test: start the collector with a file-exporter override, POST an OTLP/JSON span carrying attributes `lat=52.1`, `phone=+31…` and `note=dt_sk_test_x` to it, and assert that the output file has none of the three and still has the span name.
- Create: `backend/pkg/logx/collector_keys_test.go`: `TestCollectorRedactKeysMatchLogx` reads `../../../observability/collector/redact-keys.yaml` (via `testkit.RepoRoot`) and asserts its key set equals `logx.SensitiveKeys()`.
- Modify: `docs/PROJECT_CONTEXT.md` (current work line) and `.github/workflows/ci.yml` (add `make obs-check` to the lint job; the e2e job already runs `make dev && make smoke` and so covers Review Focus 1).

- [ ] **Step 1: Write failing tests:** `TestCollectorRedactKeysMatchLogx`, then `scripts/obs-check.sh` (it fails because the config files are missing).
- [ ] **Step 2:** `cd backend/pkg && go test ./logx/ -run Collector -v` and `scripts/obs-check.sh`. Expected: FAIL.
- [ ] **Step 3:** Write the collector, compose and Makefile files listed above.
- [ ] **Step 4:** Run both again. Expected: PASS.
- [ ] **Step 5: Isolation check (Review Focus 1).**
  - Run `make down && make dev && make smoke`. Expected: smoke passes.
  - Run `docker compose -p dt logs otel-collector | grep -c mission-control`. Expected: export errors only, with no crash loop (`docker compose ps` shows the collector healthy).
- [ ] **Step 6: Opt-out check (Review Focus 2).**
  - Run `make down && OBS=0 make dev && make smoke`. Expected: passes.
  - `docker compose -p dt ps` lists no `otel-collector`, and `make logs` shows no OTLP export errors from services.
- [ ] **Step 7: End-to-end trace check.**
  - With the collector running, set `OTELCOL_DEBUG=1` and run `make courier-link`, then post one fix.
  - In `docker compose -p dt logs otel-collector`, find one trace id that appears on these spans: gateway `POST /v1/couriers/...` → client `/dt.tracking.v1.../IngestLocations` → `tracking.ingest` → `tracking.apply` → `tracking.viewer_fanout`.
  - Paste the trace id and the span list into the PR description.
- [ ] **Step 8:** Commit: `feat(observability): minimal OTel collector for Mission Control, on by default`.

### Task 8: Branch review and PR

- [ ] **Step 1:** Run `make ci`. Expected: all green.
- [ ] **Step 2:** Update `docs/superpowers/specs/2026-11-05-mission-control-brainstorm-state.md` (PR 1 status) and `docs/PROJECT_CONTEXT.md`.
- [ ] **Step 3:** Push `mc-groundwork` and open a PR to `main` with the trace evidence from Task 7 Step 7. The body ends with the Claude Code attribution line.
