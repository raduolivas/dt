# MC-PR1 (DT telemetry groundwork): rulings and follow-ups

Plan: [`superpowers/plans/2026-11-05-mc1-dt-groundwork.md`](superpowers/plans/2026-11-05-mc1-dt-groundwork.md).
Spec: [`superpowers/specs/2026-11-05-mission-control-design.md`](superpowers/specs/2026-11-05-mission-control-design.md) §4.1, §4.6, §8 D1–D8.
These are the decisions Claude made while running the plan, and the items left open. Each ruling says what it costs if it is wrong.

## Rulings

| # | Ruling | Why | Cost if wrong |
|---|---|---|---|
| MC1-R1 | `dt.environment` on spans is the tenant scope env (`test`/`live`), the same as Postgres `dt.environment`; the deployment env is the resource attribute `deployment.environment`. | It matches DT's tenant model. | Swap the source in the tracking/deliveries app code. |
| MC1-R2 | The collector has no Compose healthcheck. The `health_check` extension still listens on 13133 inside the network. | The contrib image is distroless (no shell, curl or wget). | Add a sidecar probe later. |
| MC1-R3 | `dt_sk_` / `dt_pk_` values are masked to `[REDACTED]` (the key stays) instead of deleting the attribute. | The attributes processor cannot delete by value; OTTL can replace. The secret never leaves. | The key name is visible. |
| MC1-R4 | Segment-match redaction (`transform/redact`, `delete_matching_keys`) runs in addition to the exact-key `attributes/redact`. It mirrors `logx` segment semantics, and drift tests pin both lists to `logx`. | It closes keys like `dropoff_lat` and `x-api-key` that exact keys miss. | One extra processor stage. |
| MC1-R5 | The segment rule skips standard OTel namespaces (`messaging.`, `server.`, `client.`, `network.`, `db.`, `rpc.`, `http.`, `url.`, `service.`). | Otherwise `name`/`address`/`key` deleted `messaging.destination.name`, `server.address` and others that Mission Control needs. | See F1. |
| MC1-R6 | `make obs-check` runs in the CI `go` job (the one that runs `lint-go`). | The plan said "lint job". | Move one CI line. |
| MC1-R7 | The Redis viewer snapshot envelope stays as planned: new readers accept the old bare form, old readers drop the envelope. | Staging runs one tracking replica with no deploy overlap, and D3 chose the envelope. | Viewer updates are lost during a future rolling deploy's overlap. Before multi-replica tracking, either carry `tp` inside the bare snapshot or deploy readers first. |

## Follow-ups (parked, none blocks merge)

- **F1 (redaction hardening), closed by OBS (2026-11, `obs-stack`; see `obs-followups.md`):** because of MC1-R5, `http.request.header.authorization`/`cookie`, `url.query`/`url.full` and `db.statement` are caught by no collector rule. Nothing emits them today: otelhttp captures no headers, `url.path` goes through `httpx.RedactPath`, and redisotel runs with `WithDBStatement(false)`. Before any header or query capture is turned on, add exact-key deletes for those names after `merge_maps`.
- **F2, closed by OBS (`otelx.shutdownAll` runs the providers in parallel):** tracer and logger providers shut down one after the other inside the 5 s bound, so a hung trace export uses up the log flush's time. Shut them down in parallel.
- **F3:** `scripts/lan-https.sh` and the CI "logs" step use only the infra Compose file, so they leave the collector out.
- **F4:** partner-supplied baggage now reaches internal RPCs. Nothing reads it; consider stripping it at the gateway.
- **F5:** every ingest batch that drops a fix logs one WARN, as the plan says. That may be noisy for routine reasons such as `stale`.
- **F6 (for PR 2):** deliveries spans set Error status on expected 4xx-class domain errors (validation, not found). Mission Control should not paint those red.
- **F7:** `backend/services/webhooks/go.mod` has no `replace` for `backend/gen`, so `go mod tidy` fails there. This predates PR 1; builds go through `go.work`.
- Smaller items: hub fan-out spans set no error status on malformed or encode-error early returns; the Connect injection is skipped silently when there is no CallInfo (the span is still created); `tracedStream` Close after a failed Send ends the span without Error; header carrier `Keys()` may repeat keys; there is no test for an outbox row without a stored trace (root publish span); there are no span tests for update/assign/cancel or for TrailOnly-not-drop; the obs-check wait loop may read a partly written `spans.json`; `make lint` now needs Docker (the plan mandates obs-check there).

## Live checks (2026-11-06, owner's machine)

The images were built with a Compose override setting `build.network: host`, one at a time (see `PROJECT_CONTEXT.md`, Known local issue).

- `make dev && make smoke` with the collector: OK, M2, M3 and M5 all OK. The collector's only errors are "Exporting failed. Dropping data" for the absent `mission-control` exporter.
- `OBS=0 make dev && make smoke`: all OK, no `otel-collector` service, and no OTLP/export lines in the service logs.
- `make latency`: p95 5 ms, p99 17 ms, 595 samples, none lost.
- End-to-end trace: see the PR description. A viewer fan-out span joins the trace only after pickup, when the snapshot carries the courier.
