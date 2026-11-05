# observability

What runs today: one container, `otel-collector` (OpenTelemetry Collector contrib, pinned by tag and digest in `compose.yaml`). The DT services send traces and logs to it over OTLP; it strips coordinates, PII and DT keys, tags `deployment.environment=local`, and forwards to Mission Control at `MC_OTLP_ENDPOINT` (default `http://mc-backend:4318`). It publishes no host ports and keeps no state.

It is on by default: `make dev` merges `observability/compose.yaml` into the stack. If Mission Control is not running, the collector's exports fail and are dropped. DT is not affected.

- Turn it off: `OBS=0 make dev`. The collector is not started and the services get an empty OTLP endpoint, so nothing is exported.
- See what it receives: `OTELCOL_DEBUG=1 make dev`, then `docker compose -p dt logs otel-collector`.
- Check the config and the redaction: `make obs-check` (needs Docker; part of `make lint`).

Files in `collector/`:

| File | Purpose |
|---|---|
| `base.yaml` | receivers, redaction, pipelines (traces and logs) |
| `redact-keys.yaml` | attribute keys deleted; must equal `logx.SensitiveKeys()` (a Go test pins it) |
| `exporters.local.yaml` | the Mission Control exporter (no retry, bounded queue) |
| `exporters.debug.yaml` | adds the `debug` exporter when `OTELCOL_DEBUG=1` |

Later: the Mission Control app (a 3D troubleshooting view) lands in `mission-control/`, and the OBS stack (Prometheus, Loki, Tempo, Grafana, alerts) shares this collector. Designs are in `docs/superpowers/specs/`.
