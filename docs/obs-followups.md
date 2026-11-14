# OBS (steps 1-3) follow-ups

Plan: [`superpowers/plans/2026-11-09-obs-stack.md`](superpowers/plans/2026-11-09-obs-stack.md). Spec: [`superpowers/specs/2026-11-04-observability-design.md`](superpowers/specs/2026-11-04-observability-design.md) (rev 2, approved 2026-11-09). Branch `obs-stack`.
Written at the end of the OBS work so the plan decisions, the controller's rulings and the deferred minors outlive the scratch workspace (`.superpowers/sdd/`, gitignored). Each ruling keeps its cost if wrong.

## Plan decisions P1-P6 (Claude's calls, owner-approved 2026-11-10 with "go ahead with the plan")

| # | Decision | Cost if wrong |
|---|---|---|
| P1 | Prometheus ingests OTLP natively (`--web.enable-otlp-receiver`, collector `otlphttp/prometheus`), not remote write as spec §7 said. Keeps exemplars and the `_seconds` / `_total` names Mission Control expects; `service.name` and `deployment.environment` are promoted to labels. | Swap the exporter in `exporters.local.yaml`. |
| P2 | `LagMonitor` moves into `pkg/outbox`; `outbox.Consume` starts one per consumer. The gateway's manual wiring is deleted. | None. |
| P3 | `dt.courier.sessions.active` is replaced by `dt.courier.sessions.started` (counter, `mode`). An up-down counter is wrong after a restart, and a correct gauge needs a cross-tenant count under RLS. | Add the active gauge (follow-up below). |
| P4 | Mission Control defaults to `MC_METRICS_SOURCE=prometheus` and falls back to builtin under `OBS=0`. | One compose default. |
| P5 | The fix-to-viewer start time travels in the context (`app.WithFixReceived`) and in two envelope fields, so no port signature changes. | Port signatures carry it instead. |
| P6 | `service.version` comes from `DT_BUILD_COMMIT` (compose `${DT_BUILD_COMMIT:-dev}`, the Makefile exports `git rev-parse --short HEAD`). | None. |

## Rulings from execution (R1-R9)

| # | Ruling | Cost if wrong |
|---|---|---|
| R1 | The label allow-list follows the plan: spec §5.2 plus `rpc.system` and the otelhttp-standard keys (`network.protocol.*`, `url.scheme`, `server.address`, `server.port`); `dt.courier.sessions.started{mode}` per P3. OTel semconv emits them anyway and none is an id. | A few extra low-cardinality labels in Prometheus. |
| R2 | Prometheus ingests OTLP natively (P1). | Swap the exporter to `prometheusremotewrite`. |
| R3 | otelpgx spans landed in Task 3 with arguments off and a Go test; the exact-key collector deletes for `db.query.parameters` (MC1 F1) landed in Task 8, on the same branch and before any export to a backend existed. | A window of commits where the collector would not delete a key nothing emits. |
| R4 | `dt.outbox.pending` is the number of rows the relay selected but failed to publish in its last tick (no extra query). `OutboxStuck` is therefore `dt_outbox_pending > 0 for 10m`, not "derivative > 0": a constant failing batch never grows. | A backlog deeper than one batch that publishes slowly stays invisible; fix with a periodic `count(*)` on the partial index. |
| R5 | The OSRM `*url.Error` coordinate leak into logs (`routing.go` route_fallback, `simulator.go`) was fixed inside Task 6, because OBS starts shipping logs to Loki and spec §2 forbids coordinates in any log line. | A small out-of-plan change in `osrm/client.go getRaw`. |
| R6 | The value mask is `dt_sk_`, `dt_pk_`, `dt_lnk_` and `eyJ` (link and courier tokens are JWTs; `eyJ` is the base64 of `{"`, the start of every JWT). | An unrelated value starting `eyJ` is masked in telemetry (harmless). |
| R7 | Loki runs as uid 0 (read_only, cap_drop ALL, no-new-privileges, local only) because its distroless image owns `/loki` as root. | A local container runs as root; fix with a named-volume init or a tmpfs uid option. |
| R8 | The collector mounts the dev-only, git-ignored dtctl client cert and key read-only for the httpchecks on the mTLS `/readyz` of deliveries and accounts. Both files already exist for dtctl, and a new identity would need the services' mTLS authz to accept it. | A compromised local collector holds dtctl's dev credentials. See the dedicated cert follow-up. |
| R9 | The Mission Control Health tab has no metrics-source label. The brief's "reports metrics source prometheus" is met by the container env, the MC wire values tracking the Prometheus rule, and `metricsSourceUnavailable=false`. Adding a label is MC UI work. | A small MC follow-up. |

## Follow-ups (parked; none blocks merge unless marked)

Before merge:
- **Browser check of the dashboards.** With the stack up, open the four dashboards and one Mission Control trace link (Explore) in a browser. Only the queries were checked (`make obs-check` lints their syntax, the live checks ran the metrics), not the rendering.

Owner decisions and security:
- **Dedicated readiness client cert (R8).** Create a separate client cert in `dev-certs.sh` for the collector's httpchecks and have the services' mTLS authz accept it, instead of mounting dtctl's. The owner has not yet decided whether to do this now.
- **Loki as uid 0 (R7).** Run it as the volume's owner with a named-volume init or tmpfs uid option.
- **`eyJ` mask (R6).** It is a heuristic; revisit if a legitimate attribute starting `eyJ` matters.
- **`make obs` echoes the admin password**, and the Grafana datasources are editable in the UI.

Features not built:
- **Active courier sessions gauge** (P3): needs a cross-tenant count under RLS, or a restart-safe up-down counter.
- **Infrastructure scraping**: Postgres, Redis, NATS, OSRM and host metrics are not collected.
- **Streaming RPC metrics**: the Connect interceptors cover unary calls.
- **RUM**: no browser-side metrics from the tracking page or SDKs.
- **Step 4, staging**: CloudWatch (EMF) and X-Ray exporters (`exporters.staging.yaml`, ADOT), after the AWS resume (D28) and OQ30. Staging needs its own https httpcheck targets; the local ones are compose names.
- **Webhook metrics arrive in M6.** `dt.webhook.*` is in the allow-list and in the dashboards, but there is no instrument yet, so those panels and `WebhookFirstAttemptSlow` have no data.
- **MC Health tab shows no source name (R9).**
- **`dt.outbox.pending` semantics (R4):** a periodic `count(*)` would show a deep slow backlog. A failed select, UPDATE or commit in `publishBatch` leaves the gauge stale (it can read 0 while rows are unpublished).

Smoke and flakes:
- **Latency tool 503 `tracking_unavailable` start race** (about 4 of 12 runs, NATS not ready when the tool starts). The OBS smoke step masks it with 3 retries; fix the cause in tracking's readiness or the tool.
- **M3 "trail is empty" and M5 arriving-vs-picked_up (geofence) smoke flakes** appeared under host load (load 10-22) in 2 of 4 runs of the full smoke. They predate OBS and are timing races in those smoke scripts; later runs passed. Track separately.
- Deliveries rpc tests failed once under parallel load and passed on rerun (Task 3); not reproduced.
- The relay threshold of 10 in the smoke step is tied to the latency tool's roughly 19 fixes; the Docker-daemon error path skips loudly instead of failing (M1-M5 would fail first); a 11-trace sample.

Deferred minors, grouped:
- **otelx / allow-list:** `testReader` package-var seam; package and `Enabled` doc comments do not mention metrics; tests leave the global providers set; no test that `Init` applies `Views()`; `DT_BUILD_COMMIT` placement and recursive expansion in the Makefile; overlong comment at `otelx.go:44`; `attributeSets` fails open on ExponentialHistogram and Summary; tests cover only `Sum[int64]`, with no negative case for empty-label instruments.
- **RPC and HTTP metrics:** Close-without-receive records a success duration (matches span semantics); ignored `Float64Histogram` errors in rpc and fix-to-viewer (possible nil Record); the meter is resolved at interceptor construction (works through the global delegating provider, confirmed live).
- **Outbox and consumers:** lag gauge reads 0 until the first poll (10 s); `lagRegistries` never pruned; `dt.outbox.pending` callback registered per `Relay.Run` (two relays on one meter lose one); no test for registry removal or poll-error tolerance; long header comment in `lag.go`.
- **Gateway and tracking:** no viewer-surface 429 test; link-mode `started` counts at partner create (stated in the metric description); `ErrSessionEnded` maps to `expired`, so ended sessions count as expired; OSRM server-supplied `out.Message` could echo text into a log; `hub.join` without a transport records an empty label in tests; a dial timeout counts as `timeout`; no simulator-stamp test; negative clamp in fix-to-viewer hides clock skew; the ctx reaching `Publisher.Snapshot` is covered by the smoke check, not a unit test.
- **Collector and stack:** no F1 coverage for cookie, log and event attributes (only span and metric keys); the `[REDACTED]` count in the mask test is a floor (at least 6); retention shows deleted data for a short delay; `TelemetryExportFailing` fires locally for `otlphttp/mission-control` when Mission Control is not running.
- **Dashboards and rules:** Overview "API p95 now" uses all gateway routes while the SLO excludes the ingest routes; the dashboard lint skips template-variable queries, is syntax-only, and checks uids indirectly; "OSRM routes" means gateway routes reaching tracking ingest (the gateway never calls OSRM).
- **Smoke:** a stray blank line in `smoke-obs.sh`.

## Closed by OBS

MC-PR1 F1 (redaction hardening: exact-key deletes for headers, `url.query`/`url.full`, `db.statement` and the like, in `redact-keys.yaml`) and F2 (parallel provider shutdown within 5 s) from [`mc1-followups.md`](mc1-followups.md).
