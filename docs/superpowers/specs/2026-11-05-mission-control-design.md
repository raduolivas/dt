# Mission Control: a 3D troubleshooting app for DT (design)

Status: **draft for owner review, 2026-11-05.** Brainstormed with the owner 2026-11-04 → 2026-11-05 (state file
`2026-11-05-mission-control-brainstorm-state.md`, decisions MC-1 … MC-13). Mockups the owner saw and approved are in
`.superpowers/brainstorm/1631728-1793902454/content/` (gitignored; `layout.html`, `scene-arrangement.html`,
`screen-playback.html`, `expanded-component.html`).

Every decision is tagged with who made it: **(owner)** for the owner's own words or picks, **(Claude's call)** for
choices Claude made that the owner has not specifically confirmed. The owner checks the Claude's calls while reading.

## 1. Intent

DT moves a delivery through five services, NATS, Redis, Postgres and two browsers. When something goes quiet, the
owner cannot see where. The trigger was concrete **(owner)**: *"courier location stopped updating and I couldn't tell
where."*

**Mission Control** ("Delivery on Track — Mission Control") is a separate, internal engineering app. You type an
**order number**, and it shows every part of DT that order touched as a miniature 3D infrastructure scene. Real
requests and messages travel the wires as pulses. Clicking anything shows exactly what happened there, including
its logs. It answers **"where did it stop, and why?"** in one look.

It is a troubleshooting and learning tool. It is not a monitoring/alerting product; that is the OBS track
(`2026-11-04-observability-design.md`), which Mission Control sits next to (MC-3).

**How it is used (owner):** start DT as usual (`make dev`), then start Mission Control. Mission Control watches what
DT publishes. DT runs exactly the same with or without it.

**Success (first milestone):**

1. With DT running, `make mc` brings Mission Control up on `127.0.0.1`.
2. A sandbox-simulated delivery is created. Searching its order number shows partner → gateway → deliveries /
   tracking → NATS → Redis → viewers lit in the 3D scene, its status steps in the left panel, and live green pulses
   for real messages as the simulation runs.
3. For each courier-location break scenario (§9), Mission Control shows the pulses stopping at the right hop, colours
   that hop amber or red, and the inspector names the reason (e.g. "3 dropped: delivery reassigned").
4. Stopping OSRM shows a failing dependency on tracking, not a vague red screen.

## 2. Goals, non-goals, anti-goals

**Goals**

- G1. Find any order by its order number (the partner's `external_id`) and show its delivery end to end.
- G2. A 3D scene of DT, laid out by role, showing observed communication honestly.
- G3. Click any component, wire, pulse or event to inspect it; components expand into their working parts.
- G4. Per-component logs, linked both ways to the pulses they belong to.
- G5. Live, pause and replay with one synchronized timeline.
- G6. Detect missing activity (stale courier location, stuck states, processed-not-published, consumer lag, retry
  or rejection storms) and point at the hop.
- G7. A simulation mode, on DT's real contracts, that can break specific hops on purpose.
- G8. Mission Control reports its own health (drops, lag, storage errors), so a gap in its data shows as a gap.

**Non-goals (v1)**

- Alerting, paging, SLO dashboards (OBS).
- Hosting on staging; login; multiple users (MC-6: later, but leave a seam).
- Browser instrumentation (RUM) of the tracking page and courier page: those hops show "not instrumented".
- Webhook delivery visuals beyond "not built yet" (the webhooks service is a skeleton until M6).
- A shell, `docker exec`, Docker socket or any way to change DT from the inspector.
- Packet capture or network-level latency.

**Anti-goals (would make it a failure even if it works)**

- DT depending on Mission Control in any way: slower, blocked, or broken when Mission Control is down or slow.
- Animation that lies: a pulse that was not an observed request or message, a stage shown as reached without
  evidence, or "idle" looking the same as "not instrumented" or "broken".
- Mission Control storing coordinates, names, phone numbers, addresses, notes, photo URLs, tokens or API keys.
- Simulation mode reachable outside local/test.

## 3. Constraints

- Lives in `DT/observability/mission-control/` in the monorepo, with its own README, CLAUDE.md, make targets and
  compose file. It talks to DT only through what DT publishes: NATS streams, and telemetry through the OTel
  collector (MC-1). The other folders under `observability/` belong to OBS (OBS §7).
- Work is on branch `observability-app` (MC-2).
- Local only, single user, binds to `127.0.0.1`, no login (MC-6). An auth middleware seam exists (a no-op locally)
  so staging can add access control without a rewrite (Claude's call).
- DT is Go 1.27 (`backend/go.work`), Connect over mTLS internally, NATS JetStream, Redis, Postgres/PostGIS, plain
  TypeScript + Three.js + Vite on the web side.
- Privacy rules follow D15 and OBS §6: redaction at collection, no DT ids in metric labels, telemetry metadata from
  outside DT is not trusted.

## 4. Architecture

```
  DT (unchanged runtime)                              Mission Control (observability/mission-control)
  ─────────────────────                               ──────────────────────────────────────────────
  gateway, accounts,          OTLP (traces, logs)     ┌───────────────┐
  deliveries, tracking,  ───────────────────────────▶ │ OTel collector│──(tee, bounded queue, drop on full)──┐
  webhooks                                            │ (shared, OBS  │──▶ OBS backends (optional)           │
                                                      │  step 1)      │                                      ▼
  NATS JetStream  ◀── read-only consumers mc-* ───────┴───────────────┴──────────────────▶  mc-backend (Go)
  (ACCOUNTS, DELIVERIES,                                                                   normalize · redact
   TRACKING, GATEWAY)                                                                      · index · detect
                                                                                                 │
  sandbox simulator + partner API ◀── simulation mode only (local), sandbox key ─────────────────┤
                                                                                                 ▼
                                                                                         mc-postgres (own)
                                                                                                 │
                                                                    browser ◀── HTTP + SSE ──────┘
                                                                    (mc-web: React + R3F)
```

**Approach (MC-4, Claude's call accepted by the owner):** Mission Control has its own Go backend. It reads DT's NATS
streams through its own read-only consumers and receives spans and logs from the collector. It keeps its own
Postgres with its own retention, and serves its own frontend over HTTP + SSE. Rejected: a thin UI over
Tempo/Loki (weak at order search and live views); services pushing to Mission Control (puts monitoring on the
delivery path).

### 4.1 Signal sources

1. **NATS streams (business events).** Durable consumers named `mc-<stream>` (e.g. `mc-deliveries`), deliver policy
   "all" on first start, so Mission Control catches up on whatever the streams still hold (7 days for DELIVERIES,
   TRACKING, GATEWAY; ACCOUNTS has no max age, so Mission Control starts ACCOUNTS from "last 7 days by start time",
   Claude's call). All four streams use limits retention (`backend/pkg/outbox/streams.go`), so extra consumers do not
   take messages from DT's consumers. Mission Control acks only its own consumers and never publishes.
2. **Traces and logs (how it happened)** arrive over OTLP from the collector. The collector exports to Mission Control
   through an `otlphttp/mission-control` exporter with a bounded sending queue and no blocking: if Mission Control is
   down, the collector drops for that exporter and DT is untouched. **Traces and logs from before Mission Control
   started are not recoverable**; the UI says "no trace data for this period" (owner was told, 2026-11-05).
3. **NATS monitoring** (`:8222`, JetStream consumer info) for consumer lag and pending counts, polled read-only
   every 5 s (Claude's call).
4. **Service health**: the existing liveness/readiness endpoints, polled from Mission Control's side every 5 s.
   Liveness, readiness, dependency health and business health are separate fields everywhere (owner's brief).

### 4.2 Normalized event model

Everything becomes one `mc_event` row (Claude's call on exact shape; the builder may refine):

| field | meaning |
|---|---|
| `id` | Mission Control's id |
| `observed_at` / `occurred_at` | when Mission Control saw it / when DT says it happened |
| `kind` | `request`, `message_published`, `message_consumed`, `state_change`, `log`, `health`, `detector`, `mc_self` |
| `source` / `target` | component ids from the topology (§5), e.g. `gateway`, `tracking.ingest`, `nats.DELIVERIES` |
| `outcome` | `ok`, `slow`, `retried`, `dropped`, `rejected`, `failed`, `unknown` |
| `reason` | short machine reason (`delivery_reassigned`, `rate_limited`, `snapshot_publish_failed`, …) |
| `tenant_id`, `environment`, `delivery_id`, `order_ref` | ids only; `order_ref` = `external_id` |
| `trace_id`, `span_id` | W3C, when known |
| `origin` | `live`, `simulated` (event `source = simulator` or created in simulation mode) |
| `attrs` | small JSON of **allow-listed** fields only |

**Redaction at collection (anti-goal).** Event payloads are decoded with the generated Go protobuf types from
`backend/gen`, and Mission Control copies **only allow-listed fields** into `mc_event`. Never copied: `LatLng`
(pickup, dropoff), `pickup_name`, `display_name`, `photo_url`, contact fields, notes. For spans and logs,
Mission Control applies the same key deny-list as OBS §6 (generated from `logx`'s list) and drops matching
attributes. A dropped attribute increments a self-health counter; a non-zero value shows in Mission Control's own
health panel as a DT redaction bug. Attribute values from outside DT (partner-supplied strings) are shown as text,
never interpreted as markup or links.

### 4.3 Order index

`external_id` → `delivery_id` per (tenant, environment), built from `delivery.created`
(`contracts/proto/dt/events/v1/deliveries.proto:17`; uniqueness at `deliveries/migrations/0001_init.up.sql:32`).
Search accepts an order number, or a delivery id. If the same order number exists in several tenants or
environments, the search shows a short picker (Claude's call).

### 4.4 Storage and retention

- Own Postgres container (`mc-postgres`), own volume, never DT's database.
- **Retention 7 days**, matching DT's streams; an hourly job deletes older rows (Claude's call, played back and not
  corrected).
- Paginated queries everywhere; the live stream is bounded per client.
- Self-health table: ingest lag per source, drops, storage errors, last-seen per source. The UI shows a gap on the
  timeline wherever Mission Control itself was down or dropping.

### 4.5 Live stream to the browser

SSE from `mc-backend`. Each message carries a monotonically increasing sequence number. On reconnect the browser
sends `Last-Event-ID`; the backend replays from storage and, if it cannot, sends an explicit `gap` marker that the
timeline draws (Claude's call).

### 4.6 Integrations: Grafana, Prometheus and AWS native tools (MC-14)

The owner wants Mission Control to work with Grafana and Prometheus, and with AWS's own observability tools
(CloudWatch, X-Ray) once DT runs outside local **(owner, 2026-11-05)**. The shape (Claude's proposal, accepted by the
owner):

- **The collector is the hub.** Services send telemetry once, to the collector. The collector forwards copies to
  every destination: Mission Control always, plus Tempo/Loki/Prometheus locally when OBS runs, plus X-Ray and
  CloudWatch on staging/production (ADOT collector, OBS §4). Adding a destination is collector config, never a DT
  code change.
- **Mission Control keeps only what those tools are bad at:** the order index and the per-order event timeline
  (§4.2–4.4). It does not become a metrics database or an alerting system.
- **Source adapters, chosen by config.** The backend reads aggregate numbers (request rate, error rate, consumer lag,
  latency percentiles for the whole-system view and the Health tab) through one Go interface with one adapter per
  backend:
  - `builtin`: counts from Mission Control's own events (always available; the fallback);
  - `prometheus`: Prometheus HTTP query API (local, when OBS runs);
  - `cloudwatch`: CloudWatch `GetMetricData` on the `DT/<env>` namespace (staging/production).
  A failing adapter degrades to `builtin` and shows "metrics source unavailable" in the self-health panel; it never
  blocks the scene.
- **"Open in…" deep links.** The inspector offers links for the selected trace, log or component, built from URL
  templates in config: Grafana Explore (Tempo trace by id, Loki logs by `trace_id`, Prometheus panel), and X-Ray trace
  / CloudWatch Logs Insights on AWS. A link appears only when its template is configured.
- **One trace id everywhere.** DT uses W3C trace context, so the same id is searchable in Mission Control, Tempo and
  X-Ray. The builder confirms in current AWS docs that X-Ray accepts W3C-format trace ids through ADOT, and if not,
  stores the X-Ray form alongside.

**Phasing:**
- **v1 (local):** adapter interface, `builtin` and `prometheus` adapters, Grafana / Tempo / Loki link templates (shown
  only when OBS is running).
- **Staging (later, with MC-6 access control):** `cloudwatch` adapter, X-Ray and CloudWatch Logs link templates, the
  ADOT collector's Mission Control exporter, IAM read-only for `cloudwatch:GetMetricData` limited to the DT
  namespace. Designed here, built with the staging work.

## 5. Topology (the scene's parts)

Static, defined in code, matching the repo (verified 2026-11-04/05):

- **Outside world:** partner (API caller), courier (phone / courier page), viewers (tracking page / SDK).
- **Edge:** gateway (the only public edge).
- **Services:** deliveries, tracking, accounts, webhooks (shown "not built yet" until M6).
- **Messaging:** NATS, with one sub-part per stream (ACCOUNTS, DELIVERIES, TRACKING, GATEWAY) and per consumer.
- **Data:** Redis (tracking state `dt:state:*`, snapshots `dt:snap:*`, pub/sub), Postgres per service, OSRM.

Each component has **working parts** for the expanded view (MC-12). At least for tracking: `IngestLocations` (rpc),
NATS consumers (`tracking-deliveries`, …), state + snapshot (Redis), viewer hub (WS/SSE), sweeper (stale/lost checks).
Gateway: partner API routes, courier API, viewer endpoints, rate limiter. Deliveries: partner RPCs, state machine,
outbox publisher, consumers. The builder fills in the rest from the code.

Each wire is a known channel: Connect RPC, NATS subject, Redis key/channel, HTTP/WS/SSE.

## 6. The screen (owner-approved mockups)

### 6.1 Layout: cockpit (MC-7, owner)

- **Top bar:** live / pause / replay; order search; badges for **SIMULATED** (purple) and warning count; filters;
  "full scene" toggle.
- **Left panel:** the order (tenant, environment, delivery id) and its status steps
  (`confirmed → courier_assigned → at_pickup → picked_up → arriving → delivered`, plus `cancelled` / `failed`), each
  marked done ✓, warning ▲ or not reached ○, then the detector results for this order.
- **Centre:** the 3D scene.
- **Right panel:** the inspector for the selected thing.
- **Bottom:** the timeline, synced with the scene and both panels; drag to scrub (replay).
- Side panels are **collapsible**, so the scene can go full-screen (MC-7a, Claude's call: the owner went back and
  forth between the scene-first and cockpit layouts).

With **no order searched**, the scene shows the whole system lit by aggregate traffic, and the left panel lists recent
orders and active warnings (Claude's guess, shown and accepted).

### 6.2 Scene: zones by role (MC-8, owner)

Isometric camera over dark raised platforms: **Outside · Edge · Services · Messaging · Data**. Components are
labelled server-like cubes; wires are glowing curves. Rotate, pan, zoom, reset. Same fixed layout for every order,
so the owner learns where things live. When an order is searched, the parts and wires it touched light up and
everything else dims.

### 6.3 Pulses and states (MC-11, owner)

- A pulse = **one observed request or message**. Under heavy traffic a pulse may stand for a bundle, and then it
  carries a count label (owner's brief). Pulses are never invented to fill a gap.
- Pulse colour: **green** = succeeded, **amber** = slow, retried or dropped, **red** = failed. Cyan stays the
  interface's accent colour.
- Component and wire states must look different from each other:
  healthy+active (lit), **idle** (lit, still), **not instrumented** (dashed grey outline), **stale / no data**
  (hatched or faded with an age label), **unreachable / unhealthy** (red), **warning** (amber).
- **Simulated / live / replayed** are always distinguishable: a purple SIMULATED badge, and a REPLAY banner with the
  replay time while scrubbing.
- Reduced motion (OS setting or toggle): pulses become static markers with counts; no camera tweening.
- A stage is shown as reached only with evidence (an event, a span or a log line).
- **Inferred links.** Where two hops are joined only by ids and timing (no shared trace), the wire is drawn
  dotted and the inspector says "linked by delivery id, not by trace". Measured and inferred never look the same
  (Claude's call, from the builder check).

### 6.4 Click to expand, inspector and logs (MC-12)

- Clicking a component zooms the camera into its platform and opens it into its working parts, each with its own
  wires. A breadcrumb (`system › order › tracking`) goes back (owner asked for the expand; the form is Claude's,
  shown and accepted).
- Inspector tabs: **Overview** (what it is, current state, last activity), **Logs**, **Messages** (the requests and
  messages in and out, for this order), **Health** (liveness, readiness, dependencies, business health, separately).
- **Logs** come from DT's JSON logs (`backend/pkg/logx`), shipped over OTLP through the collector (OBS §5.1),
  redacted before storage. They default to **this order** (by `trace_id` and `delivery_id`), with a toggle to **all
  of this component**, a warnings-and-up filter and a follow mode.
- **Logs and pulses point at each other:** clicking a pulse shows that request or message (time, duration, outcome,
  trace id) and the log lines it produced; clicking a log line highlights its pulse and wire.
- Inspectors are terminal-styled, read-only, diagnostic only.

### 6.5 Visual language

Dark background, restrained neon, cyan accent, amber warning, red failure, green success pulses, purple simulated.
Monospace for data. The owner mentioned reference images in the brief; none were attached. The approved mockups are
the reference until they are.

## 7. Detection (G6)

Each detector names a hop, an order (when relevant), a reason and the evidence:

| detector | rule (v1) | source |
|---|---|---|
| courier stale / lost | active delivery, no accepted fix for > 30 s (amber) / > 5 min (red): DT's own `stale` / `lost` thresholds | tracking events + spans |
| fixes dropped | `IngestLocations` reported dropped > 0, grouped by reason | spans (after DT change D2) |
| rejected at gateway | 401 / 429 / 4xx on courier or partner routes | gateway spans |
| stuck status | status unchanged longer than a per-status limit (builder picks defaults from the MVP spec; Claude's call) | DELIVERIES events |
| processed, not published | a state change span with no matching NATS publish within 30 s | spans + streams |
| consumer lagging | consumer pending > 0 and not shrinking for 60 s | NATS monitoring |
| snapshot not delivered | `snapshot_publish_failed` log, or a snapshot with 0 viewers while viewers are connected | logs + hub signal (D4) |
| retry / rejection storm | > 10 of the same failure reason per minute on one wire | all |
| Mission Control itself behind | ingest lag > 10 s or drops > 0 | self-health |

Thresholds other than DT's own are Claude's calls and live in one config file.

## 8. Changes in DT

Small, and none of them makes DT depend on Mission Control:

- **D1. Export traces and logs, and a minimal collector.** `otelx` exports traces over OTLP and `logx` adds the OTLP
  log bridge behind its redacting handler (OBS §5.1, traces and logs only; **metrics stay with OBS**). One
  `otel/opentelemetry-collector-contrib` container in `observability/collector/` with the OBS processor chain
  (`memory_limiter → attributes/redact → resource → batch`) and the `otlphttp/mission-control` exporter. It follows
  OBS §7's layout so OBS later adds its exporters and backends (Prometheus, Loki, Tempo, Grafana) beside it without
  rework. The collector is on by default in `make dev` (one small container; `OBS=0` skips it). **Mission Control
  builds only this slice of OBS step 1**; the rest of OBS stays its own track (Claude's call, from the builder check).
- **D2. Drop reasons in tracking ingest.** `app/ingest.go` returns only counts today. Record why each fix was dropped
  (no active delivery, delivery final or reassigned, `domain.CheckFix` rejection with its verdict, state update
  failure) as span events/attributes on the ingest span. No coordinates. No ids in metric labels.
- **D3. Trace context on the viewer snapshot.** The Redis pub/sub message on `dt:snap:<delivery_id>` carries no trace
  context today; add `traceparent` to the published envelope so the hub's fan-out joins the trace.
- **D4. Fan-out signal.** The tracking hub records "snapshot sent to N viewers" (span attribute on a fan-out span).
- **D5. Span attributes** `dt.tenant_id`, `dt.environment`, `dt.delivery_id` on spans where known (OBS §5.3 allows
  these).
- **D6. Trace context across Connect calls.** Connect clients and handlers get OTel interceptors (`otelconnect` or an
  equivalent hand-written interceptor; builder picks), so gateway → deliveries / tracking calls join one trace.
- **D7. Outbox publish span** around the NATS publish in `pkg/outbox`, so "processed, not published" has evidence.
- **D8. State-change and Redis spans** in tracking (`Apply`: state put, snapshot publish) and deliveries (state
  machine transition), without arguments or payloads.

D6–D8 overlap OBS step 2 (NATS spans); doing them here means OBS step 2 shrinks, not that work is duplicated.
Without them Mission Control would have to infer most links (Claude's call to include them, from the builder check).

Already in place (verified): HTTP server spans (`otelx.HTTP`); the outbox injects trace context into NATS headers
(`pkg/outbox/outbox.go`), and consumers extract it and start a consumer span (`pkg/outbox/inbox.go:123`). Not in
place: Connect client propagation, publish spans, state/Redis spans (D6–D8). The gateway is a `PublicEndpoint`, so
each partner request starts a new trace; correlation to the partner is by ids, which is correct.

## 9. Simulation mode (MC-5, MC-13)

- Enabled only when `MC_SIMULATION=1` **and** DT's environment is local/test; the backend refuses to start with it on
  otherwise (Claude's call).
- Creates deliveries through the real partner API with a **sandbox** key (`make seed` creates one).
- **The courier is a fake phone, not DT's built-in simulator.** DT's simulator runs inside tracking and calls
  `Svc.Ingest` directly (`tracking/internal/app/simulator.go:446`), so it never crosses the gateway, the courier JWT
  or the rate limiter, and it cannot be paused. Mission Control's simulation mode therefore opens a real courier
  session the way `make courier-link` does (`scripts/courier-link.sh`, `dtctl`) and posts fixes along a route through
  the gateway courier API, exactly like the courier page. Fixes are tagged as simulated by Mission Control's own
  bookkeeping (the delivery is recorded as created by simulation mode). DT's built-in simulator stays available for
  a quick "happy path" run (Claude's call, from the builder check; this stays inside MC-5: writes only in
  simulation mode, local only, sandbox key).
- **A viewer is connected too.** Simulation mode prints the tracking link for the owner to open, and also connects
  one headless SSE viewer to the delivery, so the viewer hop is live in the demo (Claude's call).
- Everything it creates is shown as SIMULATED.
- **Break scenarios** for the headline problem, one per hop:
  1. courier stops sending (the fake phone pauses) → "nothing arriving from the courier", stale then lost;
  2. courier token invalid / expired → 401s on courier → gateway;
  3. rate limit hit (the fake phone sends too fast) → 429s;
  4. delivery reassigned mid-route → tracking drops fixes, reason `delivery_reassigned`;
  5. Redis paused → tracking cannot read or write state (`ingest_delivery_failed`), tracking → Redis wire red,
     viewer hop goes quiet. (Not `snapshot_publish_failed`: state and pub/sub share one Redis and the state write
     fails first, `service.go` `Apply`. The `snapshot_publish_failed` detector stays, covered by unit fixtures only.)
  6. OSRM stopped → tracking dependency failing.
  How each break is caused locally (compose pause/stop, API calls) is left to the builder; whatever needs Docker
  access is run by a `make` target or test, **never** by the Mission Control backend (no Docker socket).
- **Replay** replays visuals from stored events only; it never re-sends anything to DT.

## 10. Repository layout and how it runs

```
observability/
  mission-control/
    README.md            what it is, how to start it, the example order, limitations
    CLAUDE.md            rules for working in this folder (no DT write calls outside simulation mode, redaction,
                         no Docker socket)
    compose.yaml         mc-backend, mc-postgres, mc-web (dev server or static build)
    backend/             Go module (added to backend/go.work so it can import backend/gen read-only)
      cmd/mc/
      internal/{ingest,normalize,redact,index,detect,store,api,sim,topology,metrics}  (metrics = §4.6 adapters)
      migrations/
    web/                 Vite + TypeScript + React + React Three Fiber (MC-10)
  collector/             built here (D1), shared with OBS (OBS §7); OBS adds its exporters later
```

- **Frontend (MC-10, Claude's call):** TypeScript + Vite + React + React Three Fiber, separate from DT's `web/`
  workspace. The builder checks the current official docs and pins versions.
- **Make targets:** `make mc` (start Mission Control against the running DT stack, print its URL), `make mc-down`,
  `make mc-sim` (create a simulated delivery and print its order number), `make mc-break SCENARIO=<n>`,
  `make mc-test`, `make mc-lint`. `mc-test` and `mc-lint` join `make ci`.
- **Ports:** `127.0.0.1` only; the builder picks free ports and documents them.
- **Start order:** `make dev` first (DT + collector), then `make mc`. If Mission Control starts first, it waits and
  retries for NATS and shows "DT not reachable" (Claude's call).

## 11. Testing

- **Unit (Go):** normalization per event type; the field allow-list (a test feeds every DT event type with all
  personal fields set and asserts none reach `mc_event`); the span/log deny-list; each detector with fixtures (fires
  and stays quiet); SSE resume and gap markers.
- **Integration (Go, testcontainers like DT's):** NATS + Postgres; publish real DT envelopes; assert the order index,
  timeline and detectors; assert Mission Control's consumers do not change DT consumers' pending counts.
- **Isolation test:** with DT and the collector running and Mission Control stopped, `make smoke` passes and the
  collector does not block.
- **Frontend:** unit tests for state → visual mapping (each of the §6.3 states maps to a distinct look) and the
  reduced-motion path.
- **End to end (the milestone):** `make mc-sim`, search the order, and for each break scenario assert through the
  API that Mission Control names the right hop and reason. A short screen recording or screenshots go in the PR.

## 12. Order of work (suggested)

1. DT groundwork: D1 (traces + logs export, minimal collector with redact processor) and D2–D8.
2. Mission Control backend: NATS consumers, normalize + redact, order index, storage, API + SSE, self-health.
3. Frontend: cockpit layout, zones scene with live pulses, order search, timeline.
4. Inspector with tabs, click-to-expand, logs ↔ pulses linking.
5. Detectors.
6. Simulation mode and break scenarios; the milestone demo.

## 13. Left to the builder

Exact table schema and indexes; component working parts beyond tracking; per-status stuck limits; port numbers;
camera and animation details within §6; how each break scenario is triggered locally; library versions.

## 14. Open items

- **Uncommitted M5 follow-up changes** on this branch (`Makefile`, `infra/compose/compose.yaml`,
  `scripts/osrm-prepare.sh`, `scripts/local-region.sh`, `docs/runbooks/courier-phone-test.md`): they belong to M5,
  not Mission Control; recommended to move them to their own branch/PR before Mission Control work starts.
- OBS is still a draft (OQ30–32). Mission Control builds only the collector + traces/logs slice of OBS step 1 (D1);
  the rest of OBS can proceed or wait independently. OQ31 (collector on by default) is answered for the collector
  alone: on, `OBS=0` skips it.
- Reference images: none attached yet.
- Staging (later, MC-6): login, NATS credentials with subscribe-only permissions, hosting. Not designed here,
  except the integrations in §4.6.
