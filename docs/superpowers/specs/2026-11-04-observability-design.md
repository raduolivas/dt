# OBS: the DT observability stack (design)

Status: **revision 2, 2026-11-09, for owner review.** Revision 1 (2026-11-04) was written before Mission Control;
revision 2 records what Mission Control's DT groundwork (PR #27, `docs/mc1-followups.md`) already built, settles
OQ30–OQ32 as Claude's calls (§11), and adds the Mission Control wire metrics (§5.5). On 2026-11-09 the owner had no
missing signal in mind and asked for defaults again. Inputs: MVP spec `2026-10-18-dt-mvp-design.md` §10 (SLOs and
observability), `docs/ARCHITECTURE.md` §8, D15 (security first), D27a (the `consumer_lagging` signal and the
CloudWatch alarm), D28 (AWS apply paused for budget), M3 follow-ups (9) and (10), the existing `backend/pkg/otelx`
and `backend/pkg/logx`.

The owner asked Claude to plan this without a question round (2026-11-04: "just start, use sensible defaults").
Every decision that the MVP spec or a recorded decision (D-number) did not already make is marked
**(Claude's call)**. The owner reviews those calls while reading.

## 1. Intent

DT promises numbers it cannot see yet. The MVP spec sets SLOs (API p95 < 200 ms, location ingest → viewer p95 < 1 s,
webhook first attempt < 5 s, 99.5 % availability) and asks for RED metrics per endpoint, consumer lag per stream and
webhook success rate per tenant.

### 1.1 What exists already (from Mission Control's groundwork, merged 2026-11-08)

Revision 1 started from nothing. Mission Control's PR #27 built the traces-and-logs slice of OBS step 1 in the
shape this design asked for, so OBS **builds on it and does not redo it**:

| Built (PR #27) | Where |
|---|---|
| `otelx` exports **traces** over OTLP/HTTP; no endpoint means no export | `backend/pkg/otelx/otelx.go` |
| `logx` **OTLP log bridge** behind the redacting handler, `trace_id`/`span_id` on records | `backend/pkg/logx` |
| The **collector** (`otel/opentelemetry-collector-contrib`, pinned by digest), on by default in `make dev`, `OBS=0` skips it, no host ports | `observability/compose.yaml`, `observability/collector/` |
| Redaction: `attributes/redact` (exact keys, generated list pinned to `logx.SensitiveKeys()` by a Go test) **plus** `transform/redact` (segment match, `dt_sk_`/`dt_pk_` values masked); rulings MC1-R3..R5 | `collector/base.yaml`, `redact-keys.yaml` |
| `make obs-check` (config validation and a redaction test through a real collector), in the CI `go` job | `Makefile`, MC1-R6 |
| **Spans**: a Connect **client** interceptor, DT's own code, unary calls only, no server counterpart and no metrics (D6), outbox publish spans (D7), NATS context in headers and consumer spans, tracking state and Redis spans (D8), viewer snapshot `traceparent` and fan-out span (D3, D4), ingest drop reasons (D2), `dt.tenant_id` / `dt.environment` / `dt.delivery_id` attributes (D5) | services, `pkg/outbox`, `otelx/connect.go` |
| Pipelines `traces` and `logs` → `otlphttp/mission-control` only | `collector/base.yaml`, `exporters.local.yaml` |
| Mission Control's `prometheus` adapter (PromQL templates expecting a `wire` label) and Grafana / Tempo / Loki link templates | `observability/mission-control/backend/internal/metrics/` |

**Still missing, and the work of OBS:**

- there are **no metrics** at all: no `MeterProvider` is installed, so the gateway's `dt_consumer_pending_messages`
  gauge (M3 follow-up 9) exports nowhere, and the only lag signal is a warn log turned into a CloudWatch metric filter;
- nothing **stores** traces or logs except Mission Control's own short-lived store, and there is no UI to query them
  over days;
- no dashboards, no SLO view, no alerts outside the staging CloudWatch alarms;
- Postgres spans (`otelpgx`) and outgoing webhook-call spans;
- the ingest → viewer latency is only measured on demand by `make latency`;
- Mission Control's `prometheus` adapter gets no data, because nothing produces a `wire` label (§5.5).

OBS gives DT **one place to answer "is DT healthy, and if not, where is it broken?"**, locally and on staging, built
on the OpenTelemetry signals the services already half-emit. It lives in the `observability/` folder at the repo
root (it already holds the collector and Mission Control, §1.1).

**Who looks at it (Claude's call):** the owner and the developers (today, the owner and Claude) while building and
running staging, and later an on-call person. **Not** partners: the per-tenant log viewer in the console
(`ARCHITECTURE.md` §8) is a separate product feature built with the console, and it reads DT's own data, not this
stack.

**Success:**

1. `make dev` (or `make obs`) brings up Grafana on `127.0.0.1`; after `make smoke` the owner opens one dashboard and
   sees request rate, errors and latency per route, consumer lag, webhook success and the live ingest → viewer p95,
   with no manual setup.
2. From a slow or failed request on that dashboard, one click reaches its trace, and from the trace one click reaches
   that request's logs (same `trace_id`).
3. Breaking something on purpose (stop `tracking`, stop OSRM, point a webhook at a dead URL) turns an alert red
   within its stated window.
4. On staging, the same signals land in CloudWatch and X-Ray with the alarms in `alarms.tf`, at a cost that fits the
   D28 budget.

## 2. Goals, non-goals, anti-goals

**Goals**

- G1. Metrics in every Go service through OpenTelemetry (the same SDK as the traces), exported over OTLP.
- G2. One OpenTelemetry Collector config shape that serves local and staging; only the exporters differ.
- G3. A local backend stack: Prometheus (metrics), Tempo (traces), Loki (logs), Grafana (UI), with dashboards,
  data sources and alert rules provisioned from files in `observability/`.
- G4. SLO dashboards and alerts for the four MVP SLOs, plus the operational signals the spec names (RED per route,
  consumer lag per stream, webhook success per tenant, health).
- G5. Trace ↔ log ↔ metric correlation by `trace_id` (exemplars on latency histograms).
- G6. Staging mapping: CloudWatch metrics (EMF), X-Ray traces, CloudWatch Logs (already there), CloudWatch alarms.
- G7. Tests: config validation, alert-rule unit tests, and a guard that no forbidden data reaches telemetry.

**Non-goals (for OBS)**

- The partner-facing log viewer and event log in the console (console milestone).
- Browser telemetry (RUM) for the tracking page, the SDK and the courier page. Planned as a follow-up (§10); the
  frame-rate and load budgets from M4 stay measured by their own tools for now.
- Paging and on-call rotation. Alerts go to Grafana locally and to the existing `dt-staging-alerts` SNS email topic
  on staging.
- Production sizing, long retention, HA collectors. Staging and local only.
- A hosted observability vendor (Grafana Cloud, Datadog, Honeycomb). See §8 for why, and when to revisit.

**Anti-goals (it would fail even if it worked)**

- **Telemetry becomes a privacy leak.** Coordinates, names, addresses, phone numbers, notes, API keys, link tokens,
  courier tokens or JWTs in any metric label, span attribute, span name, log line or exemplar. `logx` already
  redacts logs and `otelx` already redacts credential path segments; OBS must keep that guarantee for every new
  signal and every new path (the OTLP log export included), and test it.
- **Cardinality explosion.** `delivery_id`, `courier_id`, `link_id`, raw paths or user agents as metric labels.
  Those belong in traces and logs only. `tenant_id` is allowed on a short list of metrics (§5.3), never more.
- **The stack becomes a new attack surface.** Grafana, Prometheus, Loki, Tempo or the collector reachable from
  outside `127.0.0.1` locally, or from the internet on staging.
- **Telemetry takes a service down.** An unreachable collector must never block a request, fill memory without
  bound, or fail a readiness check.
- **Staging cost blows the D28 budget.**

## 3. Constraints

- Go services use the OpenTelemetry Go SDK already (`otelx`, `otelhttp`, semconv v1.26.0). Stay on it.
- Local stack is Docker Compose (`infra/compose/compose.yaml`), ports bound to `127.0.0.1`, containers
  `read_only`, `cap_drop: [ALL]`, `no-new-privileges`. Images pinned by digest where the repo already does so.
- EU data residency (ARCHITECTURE §8): staging telemetry stays in the staging AWS region.
- Security first (D15): telemetry is treated as data that can leak; see anti-goals.
- `make ci` must stay runnable offline, apart from pulling pinned images.

## 4. Architecture

```
 Go services ──OTLP/HTTP (traces, metrics, logs)──▶ OTel Collector ──▶ local:   Tempo · Prometheus · Loki ─▶ Grafana
 (gateway, accounts,                                 (observability/    staging: X-Ray · CloudWatch EMF · CW Logs
  deliveries, tracking,                               collector/)                └─▶ CloudWatch alarms ─▶ SNS email
  webhooks)
```

Infrastructure scraping (NATS `:8222`, Postgres, Redis exporters) is **not in steps 1–3** (revision 2, Claude's call):
no SLO, dashboard or alert in this design needs it, and the consumer lag that matters comes from DT's own gauge.
It is a follow-up (§10).

- **One collector per environment (Claude's call).** Locally a compose service; on staging one collector container
  per ECS host (a daemon-style service on the single EC2 host), reached over the host network. Services set
  `OTEL_EXPORTER_OTLP_ENDPOINT` to it. With one host, a sidecar per task would cost memory for nothing.
- **The collector fans out (MC-14).** Every pipeline keeps the `otlphttp/mission-control` exporter and adds the OBS
  backends beside it (traces → Tempo, logs → Loki, metrics → Prometheus). A dead destination never blocks the
  others: each exporter has its own bounded queue and no retry locally, as the Mission Control exporter has today.
  Mission Control receives traces and logs only; metrics go to Prometheus, which Mission Control reads (§5.5).
- **Collector distribution:** `otel/opentelemetry-collector-contrib` locally (built, PR #27), the AWS Distro
  (ADOT) collector on staging because it ships the `awsemf` and `awsxray` exporters and is supported on ECS. The
  receivers and processors are identical; `observability/collector/` holds a shared base and a per-environment
  exporter file, merged with the collector's multi-`--config` support.
- **Processors (in this order, built):** `memory_limiter` → `attributes/redact` → `transform/redact` (the
  deny-list safety net, §6) → `resource` (adds `deployment.environment`) → `batch`. The new `metrics` pipeline uses
  the same chain; `transform/redact` gains `metric_statements` on the `datapoint` context with the same statements
  (Claude's call: one rule set for every signal). Tail sampling is not used yet: staging traffic is small,
  and the services keep parent-based ratio sampling via `OTEL_TRACES_SAMPLER_ARG` (1.0 local, 1.0 staging, revisit
  for production).

## 5. What the services emit

### 5.1 Changes to `backend/pkg/otelx`

Traces and the log bridge are built (§1.1). OBS adds **metrics** to `otelx.Init`, behind the same rule: **no
endpoint, no export** (instruments still exist, so tests work without a collector). OBS also fixes MC follow-up F2
here: the three providers shut down **in parallel** inside the one 5 s bound, so a hung trace export cannot eat the
log or metric flush.

- **Metrics:** a `MeterProvider` with an OTLP/HTTP exporter and a periodic reader (interval 15 s, Claude's call),
  installed globally. Histograms use explicit buckets chosen for the SLOs (§5.2), not the SDK defaults, and keep
  exemplars (trace-based exemplar filter) so a slow bucket links to a trace.
- **Logs (built):** `logx` writes redacted JSON to stdout and, with an endpoint, the same redacted records over OTLP
  with `trace_id` and `span_id`. OBS adds no log content.
- **Shutdown:** one shutdown func flushes all three providers in parallel with a bounded timeout (5 s).
- **Back-pressure:** exporters use bounded queues and drop on overflow; export errors are counted
  (`otel.sdk` self-metrics) and logged at most once a minute, never returned to request handlers.
- **Resource:** `service.name` and `deployment.environment` (`local`, `staging`) exist; OBS adds `service.version`
  (the build commit, passed to the image at build time; `dev` when unset).

### 5.2 Metric catalogue (names follow OTel semantic conventions where one exists)

| Signal | Instrument | Labels (the complete list) | Source |
|---|---|---|---|
| HTTP RED per route | `http.server.request.duration` (histogram, s) | `http.route` (the mux pattern), `http.request.method`, `http.response.status_code`, `service.name` | `otelhttp`, already wired; buckets 5 ms…5 s |
| Internal RPC | `rpc.server.duration` / `rpc.client.duration` | `rpc.service`, `rpc.method`, `rpc.connect_rpc.error_code` | DT's own interceptors in `otelx/connect.go` (revision 2, Claude's call: not `otelconnect`, which would duplicate the spans Mission Control reads): the existing client interceptor records `rpc.client.duration`, and a new handler interceptor records `rpc.server.duration` and nothing else (no server span, so traces do not change). Unary calls only; streaming calls get neither, as today |
| Consumer lag | `dt_consumer_pending_messages` (gauge, the name the gateway's `LagMonitor` already registers; kept to avoid a rename) | `stream`, `consumer` | today the gateway's `LagMonitor` (polls every 10 s) watches one consumer (`gateway-apikeys`) and sets only `consumer`. OBS moves it to `pkg/outbox` (Claude's call), adds the `stream` label, and starts one per durable consumer in gateway, deliveries and tracking; the gateway's warn log stays. Goes live once the meter provider exports (closes M3 follow-up 9) |
| Consumer handling | `dt.consumer.process.duration` (histogram), `dt.consumer.messages` (counter, `outcome` = ok / retry / dead) | `stream`, `consumer`, `outcome` | `pkg/outbox` inbox |
| Outbox backlog | `dt.outbox.pending` (gauge), `dt.outbox.publish.duration` | `service.name` | `pkg/outbox` |
| **Ingest → viewer latency** | `dt.tracking.fix_to_viewer.duration` (histogram, s) | `source` (relay / sdk / link / simulator) | tracking: from the gateway's receive timestamp on the fix to the moment the frame is written to the viewer stream; buckets 50 ms…5 s |
| Location ingest | `dt.tracking.fixes` (counter) | `source`, `outcome` (accepted / rejected_reason) | tracking `Ingest` |
| Viewer connections | `dt.tracking.viewer.connections` (up-down counter) | `transport` (ws / sse) | tracking |
| Routing | `dt.routing.duration` (histogram), `dt.routing.errors` (counter) | `operation` (route / match), `outcome` | tracking's OSRM adapter (feeds M3 follow-up 8) |
| Webhooks | `dt.webhook.deliveries` (counter), `dt.webhook.first_attempt.delay` (histogram, s) | `tenant_id`, `outcome` (ok / http_4xx / http_5xx / timeout / dead) | webhooks |
| Courier sessions | `dt.courier.sessions.active` (up-down counter), `dt.courier.refresh` (counter, `outcome`) | `mode` (sdk / link), `outcome` | gateway (M5) |
| Rate limiting | `dt.ratelimit.rejections` (counter) | `surface` (partner / viewer / courier) | gateway |
| Health | `up` from scraping `/readyz` is not used; readiness is a collector `httpcheck` receiver per service | `service.name` | collector |

**Rules for labels (enforced by the test in §7):** only the labels in this table. No ids except `tenant_id`, and
`tenant_id` only on the `dt.webhook.*` metrics (the spec asks for webhook success per tenant). Per-tenant views of
anything else come from traces and logs, filtered by the `tenant_id` span attribute.

The fix-to-viewer latency needs a server receive timestamp that travels with the fix. Today tracking `Ingest`
takes `now` for the plausibility check (`domain.CheckFix`), so the clock starts when tracking receives the fix, not
when the gateway did. OBS adds a gateway receive time to the internal ingest RPC and measures from it, so the
histogram covers the gateway hop too.

What one observation is (revision 2, Claude's calls):

- **One observation per accepted fix that reaches at least one viewer**, taken in the tracking hub at the first
  viewer write of the snapshot that carries it. Not one per viewer (ten viewers on one delivery would weigh ten
  times), and **nothing is observed when no viewer is connected**: the SLO is about what a viewer sees.
  `FixToViewerBudgetBurn` therefore means "share of viewed fixes over 1 s".
- **The start time travels** in a new field of the internal `IngestRequest` (set by the gateway) and in tracking's
  internal Redis snapshot envelope next to the `traceparent` added in PR #27 (D3); the hub strips it before the frame
  goes to viewers, as it does the trace context.
- **Simulator fixes** never pass the gateway: their clock starts at tracking's own receive time, labelled
  `source=simulator`, so dashboards can show or exclude them. The SLO panels and the alert exclude `simulator`.
- The `make smoke` OBS step keeps a viewer connected during its tracking scenario so the histogram has data (§7). Nothing comes from the client clock (Claude's call: client clocks are wrong
too often, and the M5 server-clock offset only exists in the SDK).
`make latency` stays as the end-to-end check from outside; the histogram is the always-on view.

### 5.3 Traces

- Already (before and in PR #27): server spans by route with credential segments redacted, `PublicEndpoint` on the
  gateway, Connect client/handler spans, outbox publish and NATS consumer spans with W3C context in headers, tracking
  state, Redis and viewer fan-out spans.
- Add: Postgres query spans (`otelpgx`, statement **without** arguments; check MC follow-up F1 first: the collector's
  segment rule skips the `db.` namespace, so the "no arguments" rule is enforced in code and by a Go test), spans on
  OSRM calls if the builder finds them missing, and outgoing webhook calls when the webhooks service sends them
  (M6; the partner URL host only, never the path or query).
- **Exporting traces and logs to Tempo and Loki** is collector config only (§4): no service change.
- Span attributes allowed for DT ids: `dt.tenant_id`, `dt.environment`, `dt.delivery_id`, `dt.event_id`,
  `dt.request_id`. Not allowed: anything in the `logx` redaction list.

### 5.4 Logs

No new log content (the OTLP export and the `trace_id`/`span_id` fields are built). The existing
`consumer_lagging` warn log and its CloudWatch metric filter stay until the gauge-based alarm has run on staging
for a week, then the metric filter is removed (Claude's call: keep the proven signal until the new one is proven).

### 5.5 Mission Control wire metrics (new in revision 2)

Mission Control's `prometheus` adapter (`observability/mission-control/backend/internal/metrics/promql.yaml`) asks
for request rate, error rate and p95 **per wire**, one series per topology wire id such as
`partner>gateway.partner_api` or `gateway.partner_api>deliveries.rpc`
(`observability/mission-control/backend/internal/topology/topology.go`). Until something produces a `wire` label it
gets no series and falls back to its `builtin` counts.

**Decision (Claude's call): the `wire` label is made in Prometheus, never in the services.** A
`prometheus/rules/mc-wires.yaml` file holds recording rules that turn the §5.2 labels into wire ids with
`label_replace` / static mappings, for example:

- `http.server.request.duration` on `service.name="gateway"`, `http.route` under `/v1/courier/…` →
  `courier>gateway.courier_api`; under the viewer routes → `viewer>gateway.viewer_api`; every other partner route →
  `partner>gateway.partner_api`;
- `rpc.client.duration` on the gateway, by `rpc.service` → `gateway.partner_api>deliveries.rpc` /
  `gateway.partner_api>tracking.ingest` (a client metric cannot tell the partner API from the courier API; the
  rules attribute RPCs to the partner API wire and say so in a comment; Mission Control's `builtin` source stays
  right for the courier wires);
- `rpc.client.duration` on tracking (the simulator) → `tracking.simulator>deliveries.rpc`.

The rules publish `mc:wire_requests:rate1m`, `mc:wire_errors:ratio1m` and `mc:wire_latency_p95_ms:1m` series
(names are the builder's within the `mc:` prefix), and OBS changes Mission Control's `promql.yaml` to read them.
Wires the labels cannot identify (NATS, Redis, SQL, OSRM) get no rule; Mission Control keeps `builtin` for those.
This keeps the §5.2 label rules intact (no new instrument labels) and keeps the topology knowledge next to the one
dashboard that needs it. A `promtool test rules` fixture per mapped wire guards the mapping, and a Go test in
Mission Control fails if a rule names a wire id that `topology.go` does not have.

Mission Control's Grafana link templates (`grafana_trace`, `grafana_logs`) get working defaults pointing at the
local Grafana (§7) when OBS's backends run.

## 6. Privacy and security

- **Two layers.** Layer 1 is in the code: `logx` redaction, `otelx` path redaction, the label rules above,
  statement-without-arguments for SQL spans. Layer 2 is the collector (built in PR #27): `attributes/redact` deletes
  attributes whose key is exactly in the `logx` sensitive-key list (generated, pinned by a Go test),
  `transform/redact` deletes any key with a sensitive segment (`dropoff_lat`, `x-api-key`) outside the standard
  OTel namespaces and masks values starting `dt_sk_` / `dt_pk_` to `[REDACTED]` (MC1-R3..R5). OBS extends both to
  the metrics pipeline (datapoint attributes) and adds the link and courier token prefixes to the value mask.
- **No redaction counter (revision 2, Claude's call).** Revision 1 asked the collector to count its drops and alert
  on any hit. The `attributes` and `transform` processors cannot count what they delete, and a second, parallel
  pipeline only to count would double the collector's work. The guarantee comes from tests instead: the existing
  `make obs-check` redaction test (extended to a metric datapoint), the Go label allow-list test (§7), and the
  `make smoke` OBS step, which searches Loki and Tempo for the smoke run's known coordinates and DT key prefix and
  fails on a hit. `TelemetryRedactionHit` leaves the alert table (§9).
- **MC follow-up F1 applies to OBS:** the segment rule skips `http.`, `url.` and `db.`; nothing emits headers,
  queries or statements with arguments today. Any OBS change that would (Postgres spans) adds exact-key deletes for
  `http.request.header.authorization`, `http.request.header.cookie`, `url.query`, `url.full` and the statement
  attribute with arguments, after `merge_maps`, in the same change.
- **Local exposure.** Grafana, Prometheus, Loki and Tempo bind to `127.0.0.1` only. Grafana runs with anonymous
  Viewer access locally (Claude's call: it is a local dev tool, and a login per `make dev` costs more than it
  protects) and admin login disabled except through the env-provided dev password. The collector's OTLP port is
  reachable only on the compose network, not published.
- **Staging exposure.** No UI is hosted on staging: the owner uses the CloudWatch and X-Ray consoles through their
  AWS identity. The collector listens on the host network only; the security group does not open its ports.
  IAM: the task role gets `xray:PutTraceSegments`, `xray:PutTelemetryRecords`, `cloudwatch:PutMetricData` limited to
  the `DT/Staging` namespace by condition, and nothing else.
- **Retention.** Local: Prometheus 7 days, Loki and Tempo 72 h, in named volumes deleted by `make down`. Staging:
  CloudWatch Logs retention as today, X-Ray 30 days (fixed by AWS), EMF metrics standard CloudWatch retention.
  Traces and logs hold `delivery_id` and `tenant_id`, which are pseudonymous ids, not personal data on their own;
  they never hold coordinates (anti-goal), so tenant location-retention settings do not apply to them.

## 7. Repository layout and how it runs

Files marked *(built)* exist from PR #27; the rest is new.

```
observability/
  README.md                      (built; extended) how to open it, what each dashboard answers, how to add a metric
  compose.yaml                   (built: collector) + prometheus, loki, tempo, grafana
  collector/
    base.yaml                    (built) receivers, processors, traces/logs pipelines; + metrics pipeline
    redact-keys.yaml             (built) generated from backend/pkg/logx, pinned by a Go test
    exporters.local.yaml         (built: mission-control) + tempo, loki, prometheus
    exporters.debug.yaml         (built)
    exporters.staging.yaml       step 4 (ADOT: awsxray, awsemf, mission-control later)
  prometheus/
    prometheus.yaml
    rules/slo.yaml               recording rules + alerts (§9)
    rules/mc-wires.yaml          Mission Control wire recording rules (§5.5)
    rules/*_test.yaml            promtool unit tests
  loki/loki.yaml
  tempo/tempo.yaml
  grafana/
    provisioning/datasources/    Prometheus, Loki, Tempo, with trace↔log↔metric links
    provisioning/dashboards/
    dashboards/*.json            (§8)
  mission-control/               (built) the Mission Control app; OBS touches only its promql.yaml and link defaults
```

- **Wiring (built):** `make dev` already merges `observability/compose.yaml` unless `OBS=0`, and the services
  already point `OTEL_EXPORTER_OTLP_ENDPOINT` at `otel-collector:4318`. OBS adds its four backends to that same file,
  so they follow the same switch (OQ31, §11). Prometheus receives metrics by **remote write from the collector**
  (Claude's call: the collector stays the only thing services talk to, and Prometheus needs no scrape targets for
  DT services); Prometheus is started with the remote-write receiver and native exemplar storage enabled.
- **Ports (Claude's call):** Grafana on `127.0.0.1:3000`, the only OBS port published. Prometheus, Loki and Tempo
  publish no host ports; Grafana and Mission Control reach them on the compose network. A builder who needs one
  directly uses `docker compose exec` or a temporary override.
- **Make targets:** `make obs` (start the stack if needed and print Grafana's URL); `make obs-check` (built: collector
  config and redaction test) gains `promtool check rules` / `promtool test rules`, Tempo and Loki config checks and
  dashboard JSON lint, run from pinned images. It stays in `make lint` and the CI `go` job (MC1-R6).
- **Tests:**
  - Go: in `otelx`, a test with an in-memory reader asserting every registered instrument's attribute keys are inside
    the §5.2 allow-list (run against each service's instrument registration, so a new label fails CI). The list
    covers DT's own instruments; collector-generated metrics (`httpcheck`, the collector's self-metrics) are outside
    it. Webhooks registers nothing until M6 and is skipped.
  - Collector (extends the built test): a metric datapoint carrying `lat` and a value `dt_sk_test_x` comes out of the
    metrics pipeline without the key and with the value masked.
  - Rules: `promtool test rules` with a fixture per alert (fires, stays quiet) and per mapped wire (§5.5).
  - Smoke: `make smoke` gains an OBS step, skipped when `OBS=0`: after the existing scenarios it queries Prometheus
    for a non-zero `dt_tracking_fix_to_viewer_duration` count and an `mc:` wire series, Tempo for one trace that spans
    gateway and tracking, and Loki for a log line carrying that trace id, then searches Loki and Tempo for the
    smoke run's coordinates and DT key prefix and fails on a hit (§6).

## 8. Dashboards

Four dashboards, each answering one question (Claude's call):

1. **DT overview: is it healthy?** The four SLOs as stat panels with the error budget left (30-day window),
   service readiness, alert list.
2. **API: where is it slow or failing?** RED per service and route, top routes by p95 and by 5xx, rate-limit
   rejections, links to traces from exemplars.
3. **Live tracking: is the map live?** Fix-to-viewer p50/p95/p99 by source, fixes accepted vs rejected by reason,
   viewer connections, routing latency and errors, consumer lag for the tracking consumers, active courier sessions.
4. **Events and webhooks: is data flowing out?** Outbox backlog per service, consumer lag and outcomes per stream,
   webhook success rate and first-attempt delay overall and for a selected tenant.

The staging equivalent is one CloudWatch dashboard (`dt-staging-overview`) in `infra/aws/staging/dashboards.tf`
with the same panels as dashboard 1 plus the lag and webhook panels. Staging has no Grafana (§8.1).

### 8.1 Why not a hosted vendor or Grafana on staging (Claude's call)

Under D28 every euro on staging counts. CloudWatch Logs is already paid for, X-Ray's free tier covers staging
volume (100k traces a month), and EMF metrics cost per metric: the catalogue above is about 40 series without
tenant labels, which stays within single euros a month. Running Grafana, Prometheus, Loki and Tempo on the one
small staging host would compete for its memory reservations. Amazon Managed Grafana needs IAM Identity Center,
which D28 says the account cannot have yet. Revisit at production: Grafana Cloud EU or Amazon Managed Prometheus
plus Managed Grafana, both of which this design can feed by swapping the exporter file.

## 9. Alerts

Error-budget burn-rate alerts (multi-window: 1 h and 5 m at 14.4×, 6 h and 30 m at 6×) for each SLO, plus
symptom alerts:

| Alert | Condition | Local | Staging |
|---|---|---|---|
| `ApiLatencyBudgetBurn` | share of requests over 200 ms (excluding routes that call OSRM) burns the 95 % budget | Prometheus | CloudWatch alarm on EMF metric |
| `FixToViewerBudgetBurn` | share of fixes over 1 s burns the 95 % budget | ✓ | ✓ |
| `WebhookFirstAttemptSlow` | p95 first-attempt delay > 5 s for 10 min | ✓ | ✓ |
| `AvailabilityBudgetBurn` | 5xx share burns the 99.5 % budget | ✓ | existing `alb_5xx` alarm stays, burn alarm added |
| `ConsumerLagging` | `dt_consumer_pending_messages` > 100 for 30 s (the `LagWarnPending` / `LagWarnAfter` threshold of today's warn log) | ✓ | replaces the metric-filter alarm after a week (§5.4) |
| `OutboxStuck` | outbox pending grows for 10 min | ✓ | ✓ |
| `RoutingDegraded` | OSRM error rate > 10 % or p95 > 800 ms for 5 min | ✓ | ✓ |
| `ServiceNotReady` | readiness check failing for 2 min | ✓ | via ECS / ALB health (exists) |
| `TelemetryExportFailing` | collector export failures for 10 min | ✓ | ✓ |

Thresholds are starting values (Claude's call), tuned after a week of staging data. Locally alerts show in
Grafana's alert list only; on staging they go to the `dt-staging-alerts` SNS topic with
`treat_missing_data = notBreaching`, as the existing alarms do (the host sleeps nights and weekends).

## 10. Order of work and what is left to the builder

Each step is shippable on its own. Revision 2 renumbers nothing; it shrinks steps 1 and 2 to what PR #27 left.

1. **Metrics plumbing and the local backends.** `otelx` `MeterProvider` (OTLP/HTTP, 15 s, explicit SLO buckets,
   trace-based exemplars), parallel shutdown (F2), the label allow-list test; the collector `metrics` pipeline with
   redaction and the Tempo / Loki / Prometheus exporters beside Mission Control's; Prometheus, Loki, Tempo and
   Grafana in `observability/compose.yaml` with provisioned data sources and trace↔log↔metric links. Done when the
   gateway's existing lag gauge and `otelhttp`'s request histogram show in Grafana and a trace opens its logs.
2. **The metric catalogue (§5.2) and the remaining spans (§5.3)**, including the gateway receive timestamp on the
   internal ingest RPC for `dt.tracking.fix_to_viewer.duration`, and Postgres spans with the F1 guard. Webhook
   metrics wait for M6, which builds the webhooks service; OBS ships only their names and label rules.
3. **Dashboards, rules and the smoke step:** the four dashboards (§8), SLO recording and alert rules (§9), the
   Mission Control wire rules and `promql.yaml` change (§5.5), Grafana link defaults for Mission Control, the
   `make smoke` OBS step, the README.
4. **Staging** (unchanged from revision 1): ADOT collector service, IAM, EMF mapping, CloudWatch dashboard and alarms
   in tofu, offline `tftest` for the alarms. Built when the AWS apply resumes (D28); does not block steps 1–3.

Steps 1–3 are one plan and one PR (Claude's call: each step alone leaves a half-useful stack, and together they are
smaller than M3). Step 4 gets its own plan with the AWS resume.

Left to the builder: exact bucket boundaries inside the stated ranges, panel layout, image versions and digests
(current stable releases, pinned like the collector), Loki and Tempo storage settings inside the stated retention,
backend memory limits (keep the four backends under about 1 GiB together), the exact `mc:` rule names, how
`otelhttp` is told the `http.route` (check that the label is the mux pattern, never the raw path, and fix it in
`otelx.HTTP` if not), and how the moved `LagMonitor` discovers each service's durable consumers.

**Follow-ups, not in OBS:** browser RUM for the tracking page, SDK and courier page (Faro or OTel JS, with the same
privacy rules and no coordinates); the partner-facing per-tenant log viewer (console); tail sampling and production
sizing; synthetic probes from outside AWS for the hosted page; NATS, Postgres and Redis infrastructure metrics; RPC
metrics for streaming calls; Mission Control's `cloudwatch` adapter (MC-14, with
step 4).

## 11. Decisions on OQ30–OQ32 (Claude's calls, 2026-11-09, for owner review)

The owner asked for defaults on 2026-11-04 and again on 2026-11-09. Each is reversible; the cost of being wrong is
stated.

- **OQ30 → CloudWatch + X-Ray on staging, no hosted Grafana.** It fits D28's budget, needs no new vendor or DPA, and
  matches MC-14, where the owner already chose CloudWatch / X-Ray for Mission Control on staging. Cost if wrong:
  swapping `exporters.staging.yaml` for an OTLP exporter to a hosted backend; no service change.
- **OQ31 → on by default.** The four backends join the collector under the existing `OBS=0` switch, so success
  criterion 1 needs no setup. The owner's machine builds images one at a time to avoid OOM; the backends are pulled
  images, not builds, and add about 600 MB to 1 GiB at run time. Cost if wrong: a second switch (`OBS=collector`) to
  run the collector without the backends, a few lines in the Makefile.
- **OQ32 → build steps 1–3 now.** The M4/M5 staging gates wait for the AWS resume (D28, December), so OBS no longer
  competes with them; when they run, the fix-to-viewer p95 comes from the always-on histogram. OBS goes before M6
  (webhooks) because M6's webhook-delay SLO then has its metric names and dashboard waiting. Cost if wrong: M6 starts
  one plan later.
