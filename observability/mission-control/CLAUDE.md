# Mission Control: rules for agents

- Mission Control talks to DT only through NATS (read-only consumers named `mc-<stream>`; it never publishes) and OTLP from the collector. No DT write calls outside simulation mode (`MC_SIMULATION=1`, `MC_ENV=local`, `dt_sk_test_` key only). Replay never sends anything to DT.
- Binds 127.0.0.1 only. No login; `api.AuthSeam` is a no-op seam. No Docker socket, no shell from the backend.
- Never store coordinates, names, phone numbers, addresses, notes, photo URLs, tokens or API keys. Copy payloads by allow-list only. Span and log attributes go through `logx.SensitiveKeys()` / `logx.SensitiveSegments()`. Every drop increments the self-health `redacted` counter.
- Pulses are never invented: green ok, amber slow/retried/dropped/rejected, red failed; cyan is the accent, purple is simulated.
- Retention 7 days. Courier stale 30 s, lost 5 min; other thresholds in `backend/config/thresholds.yaml`.
- `/readyz` means our own Postgres answers; it must not depend on NATS or DT.
- Go module `backend/` is in `backend/go.work`; it builds with `GOWORK=off` too (replace directives). The Docker build context is the repository root.
- Spec: `docs/superpowers/specs/2026-11-05-mission-control-design.md`.
