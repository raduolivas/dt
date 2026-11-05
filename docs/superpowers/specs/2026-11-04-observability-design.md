# OBS: the DT observability stack (design)

Status: draft for owner review, 2026-11-04. Inputs: MVP spec `2026-10-18-dt-mvp-design.md` §10 (SLOs and
observability), `docs/ARCHITECTURE.md` §8, D15 (security first), D27a (the `consumer_lagging` signal and the
CloudWatch alarm), D28 (AWS apply paused for budget), M3 follow-ups (9) and (10), the existing `backend/pkg/otelx`
and `backend/pkg/logx`.

The owner asked Claude to plan this without a question round (2026-11-04: "just start, use sensible defaults").
Every decision that the MVP spec or a recorded decision (D-number) did not already make is marked
**(Claude's call)**. The owner reviews those calls while reading.

## 1. Intent

DT promises numbers it cannot see yet. The MVP spec sets SLOs (API p95 < 200 ms, location ingest → viewer p95 < 1 s,
webhook first attempt < 5 s, 99.5 % availability) and asks for RED metrics per endpoint, consumer lag per stream and
webhook success rate per tenant. Today:

- every service creates OpenTelemetry **spans**, but they go nowhere unless `OTEL_EXPORTER_OTLP_ENDPOINT` is set,
  and nothing in the stack receives them;
- **logs** are redacted JSON on stdout (`logx`), read with `make logs` or, on staging, CloudWatch Logs;
- there are **no metrics** at all: the `dt_consumer_pending_messages` gauge waits for "a metrics exporter"
  (M3 follow-up 9), and the only lag signal is a warn log turned into a CloudWatch metric filter;
- the ingest → viewer latency is only measured on demand by `make latency`.

OBS gives DT **one place to answer "is DT healthy, and if not, where is it broken?"**, locally and on staging, built
on the OpenTelemetry signals the services already half-emit. It lives in the `observability/` folder at the repo
root (the folder exists and is empty).

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
 NATS :8222, Postgres, Redis ──scraped by the collector (local only)──┘
```

- **One collector per environment (Claude's call).** Locally a compose service; on staging one collector container
  per ECS host (a daemon-style service on the single EC2 host), reached over the host network. Services set
  `OTEL_EXPORTER_OTLP_ENDPOINT` to it. With one host, a sidecar per task would cost memory for nothing.
- **Collector distribution:** `otel/opentelemetry-collector-contrib` locally (Claude's call), the AWS Distro
  (ADOT) collector on staging because it ships the `awsemf` and `awsxray` exporters and is supported on ECS. The
  receivers and processors are identical; `observability/collector/` holds a shared base and a per-environment
  exporter file, merged with the collector's multi-`--config` support.
- **Processors (in this order):** `memory_limiter` → `attributes/redact` (a deny-list safety net, §6) →
  `resource` (adds `deployment.environment`) → `batch`. Tail sampling is not used yet: staging traffic is small,
  and the services keep parent-based ratio sampling via `OTEL_TRACES_SAMPLER_ARG` (1.0 local, 1.0 staging, revisit
  for production).

## 5. What the services emit

### 5.1 Changes to `backend/pkg/otelx`

`otelx.Init` grows from traces only to traces, metrics and logs, behind the same rule: **no endpoint, no export**
(spans and instruments still exist, so tests and ids work without a collector).

- **Metrics:** a `MeterProvider` with an OTLP/HTTP exporter and a periodic reader (interval 15 s, Claude's call),
  installed globally. Histograms use explicit buckets chosen for the SLOs (§5.2), not the SDK defaults, and keep
  exemplars (trace-based exemplar filter) so a slow bucket links to a trace.
- **Logs:** `logx` keeps writing redacted JSON to stdout (staging reads that through the `awslogs` driver,
  unchanged). When an endpoint is set, `logx` **also** sends each record over OTLP through the `otelslog` bridge.
  The bridge sits **behind** the redacting handler, so both outputs get identical, redacted records. Every record
  carries `trace_id` and `span_id` when the context has a span (this is what joins logs to traces).
- **Shutdown:** one shutdown func flushes all three providers with a bounded timeout (5 s, Claude's call).
- **Back-pressure:** exporters use bounded queues and drop on overflow; export errors are counted
  (`otel.sdk` self-metrics) and logged at most once a minute, never returned to request handlers.
- **Resource:** `service.name`, `service.version` (build commit), `deployment.environment` (`local`, `staging`).

### 5.2 Metric catalogue (names follow OTel semantic conventions where one exists)

| Signal | Instrument | Labels (the complete list) | Source |
|---|---|---|---|
| HTTP RED per route | `http.server.request.duration` (histogram, s) | `http.route` (the mux pattern), `http.request.method`, `http.response.status_code`, `service.name` | `otelhttp`, already wired; buckets 5 ms…5 s |
| Internal RPC | `rpc.server.duration` / `rpc.client.duration` | `rpc.service`, `rpc.method`, `rpc.connect_rpc.error_code` | Connect interceptor (otelconnect) |
| Consumer lag | `dt_consumer_pending_messages` (gauge, the name the gateway's `LagMonitor` already registers; kept to avoid a rename) | `stream`, `consumer` | the gateway's existing `LagMonitor` (polls every 10 s), extended to every NATS consumer; goes live once the meter provider exports (closes M3 follow-up 9) |
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
histogram covers the gateway hop too. Nothing comes from the client clock (Claude's call: client clocks are wrong
too often, and the M5 server-clock offset only exists in the SDK).
`make latency` stays as the end-to-end check from outside; the histogram is the always-on view.

### 5.3 Traces

- Already: one server span per request named by route, credential path segments redacted, `PublicEndpoint` on the
  gateway.
- Add: NATS publish and consume spans with W3C context in the message headers, so a trace runs gateway → NATS →
  tracking/webhooks (the MVP spec asks for "gateway → services → NATS consumers"); spans on OSRM calls, Redis
  pub/sub fan-out, Postgres queries (`otelpgx`, statement **without** arguments), outgoing webhook calls (the
  partner URL host only, never the path or query).
- Span attributes allowed for DT ids: `dt.tenant_id`, `dt.environment`, `dt.delivery_id`, `dt.event_id`,
  `dt.request_id`. Not allowed: anything in the `logx` redaction list.

### 5.4 Logs

No new log content. OBS only adds the OTLP export and the `trace_id`/`span_id` fields. The existing
`consumer_lagging` warn log and its CloudWatch metric filter stay until the gauge-based alarm has run on staging
for a week, then the metric filter is removed (Claude's call: keep the proven signal until the new one is proven).

## 6. Privacy and security

- **Two layers.** Layer 1 is in the code: `logx` redaction, `otelx` path redaction, the label rules above,
  statement-without-arguments for SQL spans. Layer 2 is the collector's `attributes/redact` processor, which drops
  (not masks) any attribute whose key matches the `logx` sensitive-key list (`lat`, `lng`, `lon`, `latitude`,
  `longitude`, `first_name`, `name`, `phone`, `address`, `notes`, `api_key`, `token`, `authorization`, `cookie`,
  …) and any string value matching the DT secret prefixes (`dt_sk_`, `dt_pk_`, and the link and courier token
  prefixes). Layer 2 is a safety net; a hit there means a layer 1 bug, so the processor also counts its drops
  (`dt.telemetry.redacted` counter by signal) and an alert fires on any non-zero value.
  The exact key list is generated from `logx`'s list so the two cannot drift (Claude's call).
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

```
observability/
  README.md                      how to open it, what each dashboard answers, how to add a metric
  compose.yaml                   collector, prometheus, loki, tempo, grafana (included by infra/compose)
  collector/
    base.yaml                    receivers, processors, pipelines
    exporters.local.yaml
    exporters.staging.yaml
    redact-keys.yaml             generated from backend/pkg/logx (make obs-gen)
  prometheus/
    prometheus.yaml
    rules/slo.yaml               recording rules + alerts (§9)
    rules/slo_test.yaml          promtool unit tests
  loki/loki.yaml
  tempo/tempo.yaml
  grafana/
    provisioning/datasources/    Prometheus, Loki, Tempo, with trace↔log↔metric links
    provisioning/dashboards/
    dashboards/*.json            (§8)
```

- **Wiring:** `infra/compose/compose.yaml` gains an `include: ../../observability/compose.yaml` (Claude's call:
  on by default, because the success criteria assume it is there; `OBS=0 make dev` skips it for a lighter laptop
  run). Services get `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318`.
- **Make targets:** `make obs` (open Grafana's URL, start the stack if needed), `make obs-gen` (redact keys),
  `make obs-check` (validate configs, `promtool check rules` and `promtool test rules`, `otelcol validate`,
  dashboard JSON lint). `obs-check` joins `make lint` and CI.
- **Tests:**
  - Go: in `otelx`, a test with an in-memory exporter asserting every instrument's attribute keys are inside the
    §5.2 allow-list, and a test that a log record with a sensitive key arrives at the OTLP bridge already redacted.
  - Collector: a config test that feeds a span with `lat` and a value `dt_sk_test_x` through the redact processor
    (`otelcol` with a file exporter in a container, run in `make obs-check`) and asserts both are gone and the drop
    counter is 1.
  - Alerts: `promtool test rules` with a fixture per alert (fires and stays quiet).
  - Smoke: `make smoke` gains an OBS step: after the existing scenarios it queries Prometheus for a non-zero
    `dt_tracking_fix_to_viewer_duration` count, Tempo for one trace that spans gateway and tracking, and Loki for
    a log line carrying that trace id.

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
| `TelemetryRedactionHit` | any `dt.telemetry.redacted` > 0 | ✓ | ✓ |
| `TelemetryExportFailing` | collector export failures for 10 min | ✓ | ✓ |

Thresholds are starting values (Claude's call), tuned after a week of staging data. Locally alerts show in
Grafana's alert list only; on staging they go to the `dt-staging-alerts` SNS topic with
`treat_missing_data = notBreaching`, as the existing alarms do (the host sleeps nights and weekends).

## 10. Order of work and what is left to the builder

Suggested order, each step shippable on its own:

1. `otelx` metrics and logs export, the label allow-list test, the collector with the redact processor and its
   test, the local stack with data sources (traces now visible end to end).
2. The metric catalogue in the services (§5.2), NATS and Postgres spans.
3. Dashboards, recording and alert rules with `promtool` tests, the smoke step.
4. Staging: ADOT collector service, IAM, EMF mapping, CloudWatch dashboard and alarms in tofu, offline
   `tftest` for the alarms. Applied when the AWS apply resumes (D28); it does not block steps 1–3.

Left to the builder: exact bucket boundaries inside the stated ranges, panel layout, image digests, the collector
memory limits (stay inside the staging host's reservations; check `budget.tftest.hcl`), the NATS header carrier
code, and whether `otelconnect` or a hand-written interceptor is cleaner.

**Follow-ups, not in OBS:** browser RUM for the tracking page, SDK and courier page (Faro or OTel JS, with the same
privacy rules and no coordinates); the partner-facing per-tenant log viewer (console); tail sampling and production
sizing; synthetic probes from outside AWS for the hosted page.

## 11. Open questions for the owner

- **OQ30. Staging telemetry backend:** CloudWatch + X-Ray (this design, cheapest, no UI to host) or a hosted Grafana
  Cloud EU free tier (one UI for local and staging, a new vendor and DPA). Claude's lean: CloudWatch + X-Ray until
  production.
- **OQ31. OBS on by default in `make dev`?** It adds five containers (about 600 MB of RAM). Claude's lean: on, with
  `OBS=0` to skip.
- **OQ32. Order against M5:** OBS can start right after the M5 PR merges. Claude's lean: OBS before the M4/M5
  staging gates, so the gate measurements (fix-to-viewer p95) come from the always-on histogram instead of a
  one-off `make latency` run.
