# observability

What runs locally with `make dev` (all pinned by tag and digest in `compose.yaml`):

| Container | Role | Retention / limit |
|---|---|---|
| `otel-collector` | receives OTLP from the DT services, redacts, tags `deployment.environment=local`, fans out | stateless |
| `prometheus` | metrics (OTLP receiver, `--web.enable-otlp-receiver`), SLO and Mission Control recording rules, alerts | 7 days, 320 MB |
| `tempo` | traces | 72 h, 256 MB |
| `loki` | logs (trace id as structured metadata) | 72 h, 256 MB |
| `grafana` | dashboards and Explore, provisioned from files | 192 MB |

Only Grafana publishes a host port: `127.0.0.1:3000` (anonymous Viewer, who may open Explore because `viewers_can_edit = true` in `grafana.ini`; local only, provisioned dashboards stay read-only; admin password `dt-local-admin`, or `GF_SECURITY_ADMIN_PASSWORD`). Containers are `read_only`, `cap_drop: [ALL]`, `no-new-privileges`. Loki runs as uid 0 (its image owns `/loki` as root); local only. Data lives in the named volumes `obs-prom`, `obs-tempo`, `obs-loki`; `make down` removes them, also after `OBS=0`.

The services send traces, logs and metrics to the collector over OTLP/HTTP (metrics every 15 s). The collector forwards traces and logs to Mission Control (`MC_OTLP_ENDPOINT`, default `http://mc-backend:4318`) and all three signals to Tempo, Loki and Prometheus. If a backend is down its exports are dropped; DT requests and `/readyz` are not affected. With no endpoint set (`OBS=0`) nothing is exported.

The collector also probes each service's `/readyz` (httpcheck receiver). The deliveries and accounts endpoints are mTLS, so the collector mounts the dev-only `infra/compose/certs/dtctl.pem` and `dtctl.key` read-only (a dedicated readiness cert is a follow-up, `docs/obs-followups.md`).

## Commands

- `make dev`: starts DT and the OBS containers.
- `OBS=0 make dev`: no OBS container, no export; Mission Control falls back to its builtin metrics.
- `make obs`: starts the stack if Grafana is not running, then prints Grafana's URL. Exits 2 with `OBS=0`.
- `OTELCOL_DEBUG=1 make dev`, then `docker compose -p dt logs otel-collector`: see what the collector receives.
- `make obs-check`: validates the collector config and redaction (needs Docker; part of `make lint`).
- `make smoke`: runs M1, M2, M3, M5, then the OBS step.
- `make down`: stops the stack and removes the volumes.

## Dashboards

Provisioned from `grafana/dashboards/` (folder DT). Traces link from metric exemplars to Tempo, and Tempo to Loki by trace id.

| Dashboard (uid) | Question it answers |
|---|---|
| DT overview (`dt-overview`) | Are we inside the SLOs? error budget left (30 d window, at most 7 d of local data) for API latency, fix-to-viewer, webhook first attempt and availability; current p95s, request rate, 5xx ratio, firing alerts, service readiness. |
| DT API (`dt-api`) | Which service or route is slow or failing? RED per service and per route, top routes by p95 and 5xx, gateway rate-limit rejections, internal RPC client rate and p95. |
| DT live tracking (`dt-live-tracking`) | Is the map live? Fix-to-viewer p50/p95/p99 by source, fixes by outcome and source, viewer connections, routing latency and errors, tracking consumer lag, courier sessions started and token refresh. |
| DT events and webhooks (`dt-events`) | Are events flowing? Outbox pending and publish time, consumer lag, messages by outcome, handling time per stream; webhook success and first-attempt delay (no data until M6). |

Alerts are in `prometheus/rules/slo.yaml` (ApiLatencyBudgetBurn, FixToViewerBudgetBurn, AvailabilityBudgetBurn, WebhookFirstAttemptSlow, ConsumerLagging, OutboxStuck, RoutingDegraded, ServiceNotReady, TelemetryExportFailing). They show in Grafana and the overview; nothing is notified locally. `prometheus/rules/mc-wires.yaml` holds the `mc:wire_*` series Mission Control reads (`MC_METRICS_SOURCE=prometheus`, `MC_PROMETHEUS_URL=http://prometheus:9090`). Rule tests: `slo_test.yaml`, `mc-wires_test.yaml` (run by `make obs-check`).

## Smoke step

`scripts/smoke-obs.sh` (the last step of `make smoke`) drives fixes through the gateway with `backend/tools/latency` (20 s, 2 deliveries, 1 viewer each) and checks that:

1. `dt_tracking_fix_to_viewer_duration_seconds_count{source="relay"}` rose by more than 10 over the run's baseline;
2. a `mc:wire_*` series from the run exists;
3. Tempo has a trace with spans from both gateway and tracking;
4. Loki has a line carrying that `trace_id`;
5. no `dt_sk_` / `dt_pk_` in any Loki line of the run, and no key, name or coordinate of the run in the trace JSON.

Prometheus, Tempo and Loki publish no ports, so the script queries them through the grafana container. It skips loudly with `OBS=0` or a stack started with `OBS=0 make dev`, and fails when OBS containers exist but Grafana is not running. Env: `OBS_WAIT_SECONDS` (default 45), `OBS_COMPOSE_PROJECT` (default `dt`).

## Adding a metric

1. Instrument it with `otelx.Meter("<scope>")` in the service (names `dt.<area>.<what>`, base units, only the labels you need). Never put an id, name, address, coordinate, token or key in a label; `tenant_id` is allowed only on `dt.webhook.*`.
2. Add it to `otelxtest.Allowed` in `backend/pkg/otelx/otelxtest/allowlist.go` with the exact label keys. Call `otelxtest.AssertAllowed(t, rm)` in the service's metric test; an unknown `dt.`/`dt_`/`http.`/`rpc.` instrument or label fails CI. If it is a histogram needing SLO buckets, add a view in `backend/pkg/otelx` (`Views()`).
3. Add a panel to the matching file in `grafana/dashboards/`. Prometheus names: dots become underscores, histograms get `_seconds_bucket` / `_count` (for example `dt_tracking_fix_to_viewer_duration_seconds_bucket`), counters `_total`. Check the live name in Grafana Explore before writing the query.
4. If it feeds an SLO, alert or Mission Control wire, add a rule in `prometheus/rules/` and a case in the rule's `_test.yaml`.

## Files

| Path | Purpose |
|---|---|
| `compose.yaml` | the five containers |
| `collector/base.yaml` | receivers (OTLP, httpcheck), redaction, pipelines (traces, logs, metrics) |
| `collector/redact-keys.yaml` | attribute keys deleted; must equal `logx.SensitiveKeys()` (a Go test pins it) |
| `collector/exporters.local.yaml` | Mission Control, Tempo, Loki, Prometheus exporters (bounded queues, no unbounded retry) |
| `collector/exporters.debug.yaml` | adds the `debug` exporter when `OTELCOL_DEBUG=1` |
| `prometheus/`, `tempo/`, `loki/` | backend configs; `prometheus/rules/` has the rules and their tests |
| `grafana/` | `grafana.ini`, `provisioning/` (datasources, dashboards), `dashboards/*.json` |
| `mission-control/` | the Mission Control app |

Staging uses CloudWatch and X-Ray, not these backends (OBS step 4, not built). Designs: `docs/superpowers/specs/2026-11-04-observability-design.md`; follow-ups: `docs/obs-followups.md`.
