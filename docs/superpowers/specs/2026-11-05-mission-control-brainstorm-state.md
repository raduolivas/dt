# Mission Control: brainstorm state (in progress)

Status: **design approved by the owner 2026-11-05** (`2026-11-05-mission-control-design.md`). Kept as the decision log (MC-1…MC-14). Saved 2026-11-05 so the session can
resume after a restart. When the design is written it goes to
`docs/superpowers/specs/YYYY-MM-DD-mission-control-design.md` and this file can be deleted.

Branch: `observability-app` (from `origin/main` at `3661bab`, M5 merged). Not pushed. The working tree still holds
uncommitted M5 follow-up changes (`Makefile`, `infra/compose/compose.yaml`, `scripts/osrm-prepare.sh`,
`scripts/local-region.sh`, `docs/runbooks/courier-phone-test.md`, docs); still to decide whether they go on this
branch or their own.

## The owner's brief (summary)

The owner gave a long brief (2026-11-04): build **"Delivery on Track — Mission Control"**, a separate internal
engineering app to understand, test and troubleshoot DT. Key points:

- Separate app: own frontend + backend, build, config, storage and retention, access control, failure isolation.
  DT must never depend on it synchronously.
- Main workflow: enter an **order number** → see the parties, services, instances, requests, messages and delivery
  events for it → follow live → click any node, connection or event to inspect. System overview + order-focused view.
- Visual: miniature **3D infrastructure scene** (dark raised platforms, labelled server-like cubes, restrained neon,
  glowing curved wires, pulses travelling between services, isometric camera); rotate/pan/zoom/reset; filters;
  live / pause / replay; synchronized searchable timeline; reduced motion; cyan primary, amber warn, red fail.
  (Owner mentioned reference images; **none were attached yet**.)
- Honest animation: a pulse = an observed application-level request/message (or a labelled aggregate), never
  implied packet capture or latency; idle vs uninstrumented vs broken look different; simulated / live / replayed
  are distinct; only claim stages with evidence.
- Terminal-like inspectors (diagnostic only, no shell, no Docker socket); unknown / stale / not instrumented /
  unreachable / unhealthy distinguished; liveness vs readiness vs dependency vs business success separated.
- Normalized event model, OTel + W3C trace context, redaction at collection, no ids in metric labels, no trust in
  external telemetry metadata.
- Detect missing activity: stale courier updates in active deliveries, accepted-not-processed,
  processed-not-published, viewer disconnects/lag, retry storms, rejections, stuck states.
- Backend: collect, normalize, historical order queries, live stream, topology, pagination, bounded retention,
  reconnect gaps, self-health (drops, lag, storage failures).
- Simulation mode on real contracts, restricted to dev/test; replay replays visuals only.
- Teach while building; verify everything; deliverables list (docs, commands, example order, limitations).

## Repository findings (verified in code, 2026-11-04)

- Five Go services (`backend/services/`): gateway (only public edge), deliveries, tracking, webhooks, accounts.
  Internal RPC = Connect over mTLS. Outbox → NATS JetStream (`backend/pkg/outbox/`); streams ACCOUNTS (no max
  age), DELIVERIES, TRACKING, GATEWAY (7 days) in `pkg/outbox/streams.go`. Durable consumers e.g.
  `tracking-deliveries`, `gateway-deliveries`, `deliveries-tracking`.
- Redis: tracking live state (`dt:state:*`, `dt:snap:*`, `dt:active`), rate limits (`rl:*`), deny-lists, **viewer
  fan-out via Redis pub/sub** `dt:snap:<delivery_id>` (`tracking/internal/adapters/outbound/redis/pubsub.go`) →
  WebSocket / SSE hub (`tracking/internal/adapters/inbound/http/hub.go`).
- Delivery states: `confirmed → courier_assigned → at_pickup → picked_up → arriving → delivered`, plus
  `cancelled`, `failed` (MVP spec §4). Deliveries records `stale` / `duplicate` / `conflict` outcomes (D22 C4/C5).
  Tracking signal: `pending` / `live` / `stale` (>30 s) / `lost` (>5 min).
- **Order number = partner `external_id`**, unique per (tenant, environment)
  (`deliveries/migrations/0001_init.up.sql:32`), carried on `delivery.created`
  (`contracts/proto/dt/events/v1/deliveries.proto:17`).
- Location sources: partner relay, courier SDK / courier page (M5), sandbox simulator (`source: simulator`, real
  code paths, `POST /v1/test/deliveries/{id}/simulate`).
- Frontend: **no React**; plain TypeScript + Three.js + Vite (`web/packages/renderer`, `web/apps/*`).
- Webhooks service is an empty skeleton (webhook delivery is M6).

## Gaps found

1. Spans are created (`backend/pkg/otelx/otelx.go`) but exported nowhere; no collector; no metrics.
2. Trace context is injected into NATS headers by the outbox (`outbox.go:31`); consumer-side extraction **not yet
   verified**. The Redis pub/sub snapshot to viewers carries **no** trace context.
3. Browser receipt is not instrumented (no RUM).
4. Gateway is `PublicEndpoint`: each partner request starts a new trace (correct for security), so correlation to
   the partner is by ids, not one trace.
5. `docs/PROJECT_CONTEXT.md` says M5 "not pushed" but it merged (PR #23).

## Decisions so far

| # | Decision | By |
|---|---|---|
| MC-1 | Mission Control lives in `DT/observability/` (monorepo), cleanly separated: own README, CLAUDE.md, make targets, compose profile; talks to DT only through published signals. | Owner (on Claude's recommendation), 2026-11-04 |
| MC-2 | Work happens on branch `observability-app`. | Owner, 2026-11-04 |
| MC-3 | Mission Control sits **alongside** the OBS draft (`2026-11-04-observability-design.md`): OBS step 1 (`otelx` export + one OTel collector) is shared groundwork; the collector tees a copy to Mission Control; Grafana dashboards remain a separate, smaller track. | Owner, 2026-11-05 |
| MC-4 | Approach A: own Go backend reading DT's NATS streams through its own read-only consumers + receiving spans/logs from the collector; own Postgres with its own retention; order index `external_id` → `delivery_id` per tenant/env; SSE to the browser; separate Vite frontend. Rejected: B (thin UI over Tempo/Loki — weak at order search and live), C (services push to MC — puts monitoring on the delivery path). | Claude's call, accepted with "go with your lean" |
| MC-5 | Mission Control never calls DT write APIs except in simulation mode (local/test only, real sandbox simulator + partner API with a sandbox key). | Claude's call (played back, not yet confirmed) |
| MC-6 | v1 is local-only, single user (the owner). Staging deployment comes later, so v1 binds to localhost, has no login, but keeps an auth seam so staging can add access control without a rewrite. | Owner said "just me locally for now, staging later", 2026-11-05; seam is Claude's call |
| MC-7 | Main screen layout = **Cockpit** (mockup B): order + its status/events list on the left, 3D scene centre, inspector always open on the right, synchronized timeline across the bottom. Rejected: A scene-first with slide-in inspector, C scene over live log. Mockups in `.superpowers/brainstorm/` (gitignored). | Owner, 2026-11-05 |
| MC-7a | Cockpit side panels are collapsible so the scene can go full-screen (owner hesitated between A and B). | Claude's call, 2026-11-05 |
| MC-8 | Scene arrangement = **zones by role**: raised platforms Outside world (partner, courier, viewers) · Edge (gateway) · Services (deliveries, tracking, accounts, webhooks) · Messaging (NATS) · Data (Redis, Postgres). Fixed layout for every order; order search lights the touched path and dims the rest. Rejected: left-to-right flow. | Owner, 2026-11-05 |
| MC-9 | Combined order view approved as shown in `screen-playback.html` ("looks right, keep going"): top bar live/pause/replay + order search + SIMULATED badge (purple) + warning count + full-scene toggle; left = order status steps + detectors; centre = zones scene, touched path lit, others dimmed, uninstrumented dashed; right = inspector with liveness / readiness / dependencies / business on separate lines + recent events; bottom = synced timeline. With no order searched: whole-system view lit by aggregate traffic, left panel lists recent orders + active warnings. | Owner approved the playback, 2026-11-05 (purple sim badge, stale courier → courier + tracking amber, and the empty-search view were Claude's guesses, shown and accepted) |
| MC-10 | Mission Control UI: TypeScript + Vite + React, with React Three Fiber for the scene (panel-heavy UI; isolated from DT's plain Three.js web packages). Builder checks current official docs/versions before adopting. | Claude's call, 2026-11-05 |
| MC-11 | Pulses are traffic-light coloured: green = succeeded, amber = slow / retried / dropped, red = failed; cyan stays the UI accent. One pulse = one observed request/message, or a labelled aggregate under high traffic. | Owner chose A, 2026-11-05 |
| MC-12 | Click a component → camera zooms into its platform and opens it into its working parts, with breadcrumb back. Inspector tabs Overview / Logs / Messages / Health. Logs default to this order (trace id / delivery id), toggle all-of-component, warn+ filter, follow. Click pulse → the message + its log lines; click log line → highlight its pulse. Logs = DT's JSON logs (`backend/pkg/logx`) gathered by the collector, redacted before storage. | Owner asked for logs + expand; log/pulse linking is Claude's proposal, shown in mockup and accepted with MC-11 |
| MC-13 | Headline v1 scenario: "courier location stopped updating — where?" across the 6 hops; DT records drop reasons in tracking ingest. | Owner's pain point; hop model Claude's, 2026-11-05 |

Expected small DT changes (Claude's proposal, not approved): `otelx` export; trace context on the Redis pub/sub
viewer message; a "snapshot sent to N viewers" signal from the tracking hub; `dt.delivery_id` / tenant span
attributes. Browser receipt shown as "not instrumented" until later.

Lean, not decided: React + React Three Fiber for Mission Control's UI only (repo uses plain Three.js); check current
official docs before adopting.

Proposed first milestone: run a sandbox-simulated delivery through the real services → search its order number in
Mission Control → see partner → gateway → deliveries/tracking → NATS → viewer stream in 3D with each event → stop
OSRM and show Mission Control locating the failure.

## Where we stopped

Access question answered (MC-6). Layout (MC-7) and arrangement (MC-8) chosen. Screen approved (MC-9), UI stack MC-10.

Owner's real pain (2026-11-05): "courier location stopped updating and I couldn't tell where". This is the
headline scenario for v1. Verified location path: phone/courier page (browser, uninstrumented) → gateway
`courier.go` reportLocations (JWT auth, rate limits → 401/429) → tracking `IngestLocations` → `app/ingest.go`
(returns only accepted/dropped COUNTS; silent drop reasons: no active delivery for courier, delivery final or
reassigned, `domain.CheckFix` rejects, state update failure) → Redis state + `dt:snap:<id>` publish (best effort,
`snapshot_publish_failed` warn) → hub fan-out (WS/SSE) → viewer browser (uninstrumented). Proposed DT change: record
drop reasons (span attributes/events, no ids in metric labels). Played back the "where did it stop" hop view;
awaiting owner's correction.

Still to cover after that: the 3D scene and layout (offer the visual companion / mockups; ask for the reference
images), React vs plain Three.js, detection thresholds, simulation scenarios, retention numbers, then write the
design → builder check → owner approval → `superpowers:writing-plans`.

Not yet read in full: `docs/m3-followups.md`, `m4-followups.md`, `m5-followups.md`, guides, runbooks, plans
(headings only), `.superpowers/sdd/*` task logs, `infra/aws/*`, CI workflows, web package internals.

Owner feedback on the location playback (2026-11-05): wants **logs for each component**, open to a more
interactive way; wants to **click a component and expand it** to see more detail; wants wires to show **real
communication**, with **green light** pulses crossing between healthy components. Claude's proposal (mockup
`expanded-component.html`): clicking a component zooms into its platform and opens it into its working parts
(e.g. tracking → IngestLocations, NATS consumer, state+snapshot, viewer hub, sweeper); inspector gains tabs
Overview / Logs / Messages / Health; logs default to "this order" (via trace id / delivery id), toggle to "all of
this component", warn+ filter, follow; clicking a pulse shows the exact message and its log lines, clicking a log
line highlights its pulse. Asked: pulse colours traffic-light (green ok / amber slow-retried-dropped / red failed,
cyan stays UI accent) vs green as the whole UI's main colour.

**2026-11-05: design written** → `docs/superpowers/specs/2026-11-05-mission-control-design.md` (owner: "nothing
missing, write it up"). Builder check (fresh reviewer subagent) running; then bring the owner's-only questions in
one message, update the doc, hand it over for approval, then superpowers:writing-plans.

**2026-11-05 builder check done.** Claude settled: fake-phone courier via gateway in sim mode, minimal collector
slice (D1), extra DT tracing D6–D8 + dotted "inferred" wires, scenario 5 corrected, headless SSE viewer. Owner
chose the PR split (Claude's recommendation): M5 region changes → branch `local-region`, **PR #26** (opened
2026-11-05); then PR 1 = DT groundwork (collector + tracing), PR 2 = Mission Control app. Owner asked that Mission
Control integrate with Grafana / Prometheus and AWS native tools (CloudWatch, X-Ray) once deployed beyond local;
Claude proposed collector-as-hub + backend adapters + deep links (see design §4.6 once added); awaiting owner.

**MC-14 (owner, 2026-11-05):** integrations section added (design §4.6): collector hub, adapters builtin/prometheus (v1) and cloudwatch (staging), Grafana/Tempo/Loki links (v1), X-Ray/CloudWatch Logs links (staging). Next: owner reads and approves the design → superpowers:writing-plans.

**Design approved by the owner, 2026-11-05 ("approved, write the plan").**

**PR 1 (DT groundwork), 2026-11-06:** plan `2026-11-05-mc1-dt-groundwork.md` done on branch `mc-groundwork`
(Tasks 1–7, final review + one fix wave clean). Live checks passed: `make dev && make smoke` with the collector up
(collector only logs export drops while Mission Control is absent), `OBS=0` (no collector, no export errors),
`make latency` (p95 5 ms), and one end-to-end trace gateway `POST /v1/courier/locations` → `IngestLocations` →
`tracking.ingest` → `tracking.apply` → `tracking.viewer_fanout`. A viewer fan-out span appears only after pickup,
when the snapshot carries the courier. PR to `main` opened. Next: PR 2, `2026-11-05-mc2-mission-control-app.md`.

**PR 2 (Mission Control app), 2026-11-07:** plan `2026-11-05-mc2-mission-control-app.md`, Tasks 1-14 done on branch
`mission-control` (stacked on `mc-groundwork`, PR #27 still open). Code complete; `make mc-e2e` (milestone test against
`make dev` + `make mc` + `make mc-sim`) passes, `make mc-down && make smoke` passes (DT does not depend on Mission
Control), `make ci` green. Final whole-branch review clean after one fix wave (R40); PR #29 open, stacked on #27. Scenario 4 correction (R36): DT reports
`no_active_delivery`, never `delivery_reassigned`. Rulings and deferred minors: `docs/mc2-followups.md`.
