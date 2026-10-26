# M3 Tracking + first AWS staging: Design

> **Status:** draft for owner review (2026-10-26)
> **Builds on:** MVP spec [`2026-10-18-dt-mvp-design.md`](2026-10-18-dt-mvp-design.md) (§4.5 visibility, §5.2–5.5 API and snapshot, §8 architecture, §10 SLOs, §11 security, §12 privacy). Everything in the MVP spec still applies unless this document changes it.
> **Calls marked *(Claude's call)*** were made by Claude and are open to the owner's review.

## 1. Intent

DT's product is the customer experience: a videogame-like 3D tracking page that is far better than a partner's own 2D tracker. M3 builds the engine behind it. Courier GPS comes in, gets turned into a correct, privacy-safe live snapshot, and reaches the customer's screen in under a second. M3 also gives partners the courier data they need to settle disputes, because the customer paid the partner and the partner has to answer for the delivery.

M3 ends with DT running on AWS for the first time: a lean staging environment of about €100/month, set up from an empty AWS account.

### What the owner said (2026-10-26)
- **When the customer sees the courier:**
  - **End-to-end** (third-party couriers): from the moment the courier accepts, so the customer watches them go to the restaurant and then come to them.
  - **Direct** (the brand's own fleet, like Flink): only once the courier has picked up, coming straight from the brand.
  - This matches MVP spec §4.5 and is unchanged.
- **Partners need the courier data** for security and for resolving delivery issues. The customer dealt with the partner, so the partner owns the problem.
- **Partners keep their own simple 2D trackers** for internal use if they want. DT competes on the customer experience.
- **How partners reach customers:**
  - send DT's tracking link;
  - embed DT's page in their app or site;
  - later, an easy integration kit (D9's SDK).
- **Signal loss:** the behaviour in §3.3 was approved as proposed.
- **AWS:** M3 ends with a staging deployment. The plan sets up AWS from scratch. Lean budget: **about €100/month**.
- **ETA, following Uber Eats:**
  - Before pickup, the ETA is the kitchen's usual preparation time plus the drive. When DT can't know the preparation time, the customer sees "Calculating delivery time…".
  - The ETA is recalculated if preparation runs shorter or longer.
  - After pickup it is the drive time.
  - Traffic-aware ETAs (learned from courier movement or from map data) come later.

## 2. Goals, non-goals, anti-goals

**Goals**
1. The `tracking` service: location ingest, plausibility checks, OSRM map matching, routes and ETA, arrival detection, and snapshot projection with server-side visibility.
2. Realtime delivery to customers: tracking links, viewer tokens, WebSocket, SSE and polling, signal-loss states.
3. Partner data access: live courier position (unfiltered), the delivery trail, position-stamped proof points, with every read audited.
4. Location input without the courier app: the sandbox simulator and the partner relay `POST /couriers/{id}/locations`.
5. Retention and erasure carried over from M2:
   - trail truncation on `delivery.personal_data_erased`;
   - the 30-day raw-GPS job;
   - the 13-month retention job (tracking **and** deliveries).
6. AWS:
   - account setup;
   - guard rails;
   - staging infrastructure as code;
   - CI deploy from `main`;
   - the "before M3 staging" items from PROJECT_CONTEXT §7.

**Done when**
- A simulated delivery streams snapshots to a test client at **p95 < 1 s from fix accepted to snapshot received**. This is measured locally and on staging with 20 concurrent simulated deliveries and 5 viewers each *(load shape: Claude's call; full capacity testing stays in M9)*.
- The authorization matrix covers the new endpoints and the viewer credential.
- Both delivery models show the correct visibility in every state.
- Staging is reachable at `api.staging.deliveryontrack.io` and redeploys on every merge to `main`.

**Non-goals (stay in their milestones)**
- Courier sessions, courier tokens, the courier SDK and the courier web link: **M5** (MVP spec §16).
  - M3's ingest code must accept a courier session as a source later without restructuring.
  - Per D26, sessions must later be able to list offers.
- The renderer and the hosted tracking page: **M4**. M3 only proves snapshots arrive, using a test client.
  - Dead reckoning and smoothing on screen are the renderer's job.
- Partner webhooks: **M6**. M3 publishes the internal events that M6 will map.
- Partner map views in the console: **M7** or later. Partners get data, not a map.
- Production environment: only the empty AWS account is created in M3.

**Anti-goals (failure even if it "works")**
- A hidden courier position ever reaching a viewer. This includes the network payload, not just the screen.
- Partner A reading anything of partner B's, or test data crossing into live.
- Staging costs drifting above budget unnoticed.
- Long-lived AWS access keys anywhere, including CI, laptops and the repo.
- Real customer or courier data on staging. Staging runs only sandbox and simulated data.
- Collecting more courier personal data than the MVP spec allows (§12: display name, optional photo, vehicle).

## 3. Customer side

### 3.1 Visibility
MVP spec §4.5 applies as written:
- The projection builds the viewer snapshot with `courier: null` whenever the courier is not visible.
- `route.to_pickup` is `null` in the Direct model.
- Visibility is evaluated from the **delivery's own** `delivery_model`, never the tenant's plan (D16, mixed models).

### 3.2 Tracking links and viewer tokens
- **Issuing the link (owner: gateway).** On `POST /deliveries` the gateway issues the tracking link. This was deferred from M2 (C3). The response gains `tracking {url, token, expires_at}`.
  - The token is 128-bit random and stored only as a hash.
  - The URL base is configurable. On staging it is `https://track.staging.deliveryontrack.io/t/<token>`; the page itself arrives in M4.
- **Expiry and rotation.** The link expires 30 minutes after a final state; gateway consumes `delivery.status_changed`. A partner can rotate a link with `POST /deliveries/{id}/tracking-link/rotate`, which immediately invalidates the old token and returns a new one *(Claude's call: the MVP spec says "revocable" but names no endpoint)*.
- **Cutting off viewers.** Rotation and expiry cut off viewers who are already connected **immediately**, not when their JWT runs out *(Claude's call, security first)*:
  - Each viewer JWT carries the link's id.
  - The gateway publishes the revoked link id to a Redis deny-list (TTL = JWT lifetime) and an internal event.
  - Tracking rejects deny-listed JWTs and closes their open WebSocket/SSE streams with a `revoked` message.
- **Renewing the viewer token.** `POST /tracking/session/refresh` takes the current viewer JWT. It checks that the link is still valid, so renewal never needs the link token again.
- **Viewer endpoints** (MVP spec §5.4):
  - `POST /tracking/session` runs on the gateway. It exchanges the link token for a viewer JWT: Ed25519, `aud: viewer`, 15 minutes, bound to tenant, environment and delivery.
  - Snapshot, WebSocket and SSE are served by `tracking` (path-routed). Tracking verifies the JWT against the gateway's JWKS.
  - `branding` in the session response is `null` until M7.
- **Embedding.** Nothing in the viewer flow depends on cookies or URL-borne credentials after the exchange: the WebSocket authenticates with its first message. This keeps the flow working inside a partner's iframe or webview, including when third-party cookies are blocked.
  - CORS on the viewer endpoints allows any origin, because the bearer token is the credential and no cookies are involved.
  - `frame-ancestors` for the hosted page is M4.

### 3.3 Signal loss (approved by the owner)
The server publishes facts; the renderer (M4) does the visual smoothing.

| Time since last accepted fix | Snapshot `courier.signal` | What the customer sees (M4) |
|---|---|---|
| ≤ 30 s | `live` | Courier moves smoothly along the route at last known speed (client-side dead reckoning) |
| > 30 s | `stale` | Courier stops at last known point, slightly faded, "Updating courier location…"; ETA shown as estimated (`eta_quality: "estimated"`) |
| fix returns | `live` | Courier glides to the real position (no jump) |
| > 5 min | `lost` | Same as `stale`; **no alarm to the customer** |
| courier visible, no fix yet | `pending` (`position: null`) | Courier card shown with "Locating courier…", no marker |

- **No fix yet.** `pending` covers a courier who just became visible (End-to-end `courier_assigned`, Direct `picked_up`) before any fix has arrived. If no fix arrives within 5 minutes of becoming visible, `pending` turns into `lost` and the signal-lost event fires *(Claude's call)*.
- **Contract rule.** All four `signal` values are in the M3 contract from the start, so no enum value has to be added later.

- **Snapshot changes.** The snapshot gains `courier.signal` and keeps `position_at` and `speed_kmh`. Both are additive to the v1 contract.
- **The `lost` event.** At `lost`, tracking publishes `tracking.courier_signal_lost` once per gap and `tracking.courier_signal_restored` when fixes return. The partner can see the state in the API (§4.1). M6 maps these events to webhooks (`courier.signal_lost`, `courier.signal_restored`).
- **Timer evaluation.** Signal timers are evaluated by tracking itself, not only when a fix arrives. A silent courier must still flip to `stale` and `lost`, and the snapshot version increases so viewers get the change.

## 4. Partner side

### 4.1 What the partner can read
All endpoints use the partner's secret key and are scoped to tenant and environment.

| Endpoint | Returns |
|---|---|
| `GET /deliveries/{id}/tracking` | Live view **without** the visibility filter: courier position, heading, speed, `position_at`, `signal`, route legs, ETA, distance remaining. Available for every non-final state; after a final state it returns the last values. |
| `GET /deliveries/{id}/trail` | Raw fixes `{t, lat, lng, accuracy_m, speed_mps?, heading_deg?, source}` for the delivery window (from assignment, or from creation in Direct, until final), cursor-paginated. After the raw data is gone (30 days) it returns the simplified route plus `raw_available: false`. `?format=geojson` returns a GeoJSON FeatureCollection *(Claude's call)*. |
| `GET /deliveries/{id}/proof-points` | One entry per status change in which a courier position matters (`at_pickup`, `pickup_confirmed`, `arriving`, `delivered`, `failed`): `{status, at, actor, position, position_at, accuracy_m}` taken from the latest accepted fix at the time of the change, or `position: null` if none within 2 minutes. |

- **Reads are audited.** Every call to `trail` and `proof-points` writes an audit record, so reads of location history are audited, not only writes. This is new: the MVP spec audits writes only. `tracking` is excluded from read auditing because it is a live view used constantly.
- **No new courier fields.** The partner identifies the courier through its own `external_id` (M2), which maps to the partner's own records (phone, contract, and so on). DT stores no more courier data than MVP spec §12.
- **Privacy terms.** Courier location is the courier's personal data, and the partner is the controller. The partner terms and DPA say the partner must inform its couriers that they are tracked during deliveries. That is a documentation item; there's no product change.

### 4.2 Retention

| Data | Kept |
|---|---|
| Raw fixes | 30 days (the tenant setting is M7; M3 uses the default), then simplified (Douglas–Peucker) into one route per delivery |
| Simplified route, proof points, delivery records (tracking and deliveries) | 13 months, then deleted |
| Audit log | 1 year (unchanged) |

- **Erasure.** On `delivery.personal_data_erased`, tracking replaces the raw trail with the simplified route at once, as MVP spec §5.2 requires. Proof points stay, as dropoff coordinates do (M2 C8).
- **Job behaviour.** All jobs are idempotent, run nightly, and drop whole day partitions where possible.

## 5. Ingest and processing

- **Sources in M3:**
  - the partner relay `POST /couriers/{id}/locations`: secret key, ≤ 50 fixes, ≤ 16 KB;
  - the simulator.
  The courier SDK and link come in M5. Each stored fix records its `source` (`relay`, `simulator`, later `sdk`/`link`).
- **Relay contract.** `{id}` in the relay path is DT's `cou_…` id, the same id assignment uses (M2 C10).
  - The relay has its **own rate-limit bucket**, separate from the partner's general API limit, so a fleet's GPS never starves its other calls *(Claude's call)*.
  - The limit is 1 req/s per courier (burst 5) with a per-tenant ceiling of 500 req/s.
  - Per-plan values come in M7.
- **Purpose limitation.** A fix is accepted only while the courier has at least one non-final delivery assigned to them. Fixes outside that window are discarded, and the response reports `{accepted, dropped}` counts. A fix is attached to every active delivery of that courier.
- **Courier in the Direct model.** No state-machine change from M2: the partner assigns the courier any time before pickup. Assignment in Direct doesn't change the state, and the courier stays hidden until `picked_up`.
  - A Direct delivery that reaches `picked_up` with no courier assigned shows `courier: null` for its whole life.
  - The docs tell Direct partners to assign before pickup.
- **Path:** gateway (auth, rate limit, schema) → tracking over Connect/gRPC.
  - Tracking validates plausibility (MVP spec §11.1) and map-matches through OSRM `/match`, falling back to the raw point.
  - It updates the snapshot in Redis, fans out via Redis pub/sub to all tracking instances, and writes fixes to the day-partitioned trail table in batches.
  - The Postgres write is **not** on the path to the viewer.
- **Routes.**
  - OSRM `/route` builds the legs when a delivery is created (preparing view) and at assignment.
  - Re-routing happens when the courier deviates more than 50 m from the route.
  - On OSRM failure, the last known route is used with a straight-line ETA and `eta_quality: "estimated"`.

### 5.1 ETA (owner's model, following Uber Eats)

**Before pickup (preparation + drive)**
- **Ready time comes from, in order:**
  1. the delivery's `ready_at`: a new optional field on `POST /deliveries` and `PATCH /deliveries/{id}`, which the partner can change until pickup;
  2. otherwise, creation time plus the tenant's **default preparation time** (a new tenant setting `default_prep_minutes` in `accounts`, via `dtctl tenant settings` as in M2 C1);
  3. otherwise, unknown.
- **ETA** = max(ready time, courier's arrival at the store in End-to-end) + drive from store to dropoff.
- **Shown as a window** of `eta_window {min_seconds, max_seconds}`, the ETA ±5 minutes, plus `eta_seconds` as the midpoint *(window width: Claude's call)*.
- **Unknown ready time.** `eta_seconds` and `eta_window` are `null` and `eta_state: "calculating"`; the page shows "Calculating delivery time…".
- **Changes to `ready_at`** recalculate the ETA at once.

**After pickup (drive only)**
- `eta_seconds` is the OSRM drive time from the courier's latest position, with no traffic data. `eta_window` is `null` and `eta_state: "live"`.
- It is recomputed at most every 10 s per delivery *(Claude's call)*.

**Contract and events**
- `eta_state` has three values from the start: `calculating`, `estimated` (before pickup) and `live`. `eta_quality: "estimated"` still flags an OSRM fallback.
- `tracking.eta_changed` follows MVP spec §5.6 (changes of 2 minutes or more, at most once per minute) and also fires before pickup, for example when `ready_at` moves.

**Future (not M3):** traffic-aware ETAs from observed courier speeds or a map provider's traffic data. The ETA calculation sits behind a port in `tracking/internal/app`, so a smarter estimator can replace OSRM-only later without contract changes.
- **Arrival.** Within 200 m of the dropoff, or with an ETA of 90 s or less, tracking publishes `tracking.arrival_detected`. Deliveries consumes it and applies `arriving` with actor `system`; this wiring was deferred from M2 (C3).
- **Snapshot versions.** The snapshot `version` increases monotonically per delivery, and viewers ignore older versions.
- **Isolation.** RLS is enabled and forced on tracking tables, with the same pattern as M2 (R7, fail-closed `current_setting`).

## 6. Simulator (sandbox only)

- `POST /test/deliveries/{id}/simulate` with `{speed_factor?: 1–20, signal_gap?: {after_s, duration_s}}`. It works with test keys only; live keys get `403`.
- The simulator drives a virtual courier along the OSRM route at a plausible speed for the vehicle. It emits fixes through the **same** ingest path, tagged `source: simulator`, and reports `at_pickup`, `pickup_confirmed` and `delivered` with actor `courier`, following the delivery's model.
  - It returns `409` if the delivery is in a state it can't drive.
- **The simulated courier.** If no courier is assigned, the simulator upserts a test courier (`external_id: "dt-simulator"`, display name "Sim Courier") in the tenant's test environment and assigns it.
  - **End-to-end:** the courier starts on a road about 1.5 km from the store and drives there.
  - **Direct:** the courier waits at the store and starts moving at pickup.
- **Pickup policy.** The simulator confirms pickup the way the delivery's pickup policy requires. It confirms as courier, as store, or as both, using the same deliveries RPC the partner path uses, so the sandbox demo always reaches `picked_up` *(Claude's call)*.
- **Preparation time.** It respects `ready_at` and the default preparation time, scaled by `speed_factor`.
- `signal_gap` exists so the stale and lost behaviour can be tested and demoed *(Claude's call)*.

## 7. AWS staging

### 7.1 Account setup (owner does the parts that need a person; the plan has a step-by-step runbook)
- **Owner steps:**
  - Create the AWS account: card, phone, and an email the owner controls.
  - Enable root MFA. Create no root access keys.
  - Delegate DNS for `deliveryontrack.io` (or a `staging.` subdomain) from the registrar to Route 53.
- **AWS Organization** with accounts `management` (billing, IAM Identity Center, CloudTrail), `staging` and `production` (empty in M3).
- **IAM Identity Center** for human logins with MFA; no IAM users.
- **SCPs:**
  - lock regions to `eu-central-1` (plus the global services that need `us-east-1`);
  - deny leaving the org, disabling CloudTrail, and root use in member accounts.
- **Organization CloudTrail** to an S3 bucket in `management`.
- **AWS Budgets:** a ceiling of **€100/month including VAT** across all accounts *(Claude's call: the owner set about €100; treated as a hard ceiling)*.
  - Email alerts at 50/80/100% actual and 100% forecast, plus Cost Anomaly Detection.
  - Because AWS bills before VAT, the budget is set to about €82 before VAT.
- **Who uses staging.** Only the DT team, with sandbox and simulated data, during M3 *(Claude's call: the owner didn't answer whether pilots join staging)*. Inviting pilot partners later means treating staging data as real and adding tenant onboarding there, so that is decided before the first pilot.
- **CI access.** GitHub Actions deploys through OIDC: one role per account, limited to this repository's `main` branch and the `staging` GitHub environment.

### 7.2 Infrastructure as code *(Claude's call)*
- **OpenTofu**, with state in S3 using native lockfiles, encrypted.
- Two stacks:
  - `infra/aws/bootstrap`: org, accounts, SCPs, Identity Center, budgets, OIDC roles, state bucket;
  - `infra/aws/staging`: everything in §7.3.
- `make staging-up` and `make staging-down` tear down and rebuild everything except the state bucket, DNS zone and KMS keys.

### 7.3 Lean staging layout *(Claude's call; estimated ≈ €90/month before VAT, so it must be trimmed to fit €82)*

**Trimming to fit**
- Staging **stops outside working hours by default.** A scheduled scale-to-zero of the EC2 host and RDS stop runs at 20:00–08:00 Europe/Amsterdam and all weekend, saving about €25. `make staging-wake` starts it on demand.
- The ALB remains the largest fixed cost. If the estimate still doesn't fit, the builder may replace the ALB with Caddy on the host plus an Elastic IP, and must state the trade-off in the plan.

| Piece | Choice | Approx. €/month |
|---|---|---|
| Compute | ECS on **one EC2 `t3.medium`** (2 vCPU, 4 GB, x86) in a public subnet; security group admits only the ALB; access by SSM Session Manager, no SSH | 35 |
| Containers on that host | 5 services, NATS (JetStream on EBS), Redis, OSRM (**Noord-Holland** extract, car + bike, preprocessed in CI and pulled from S3) | incl. |
| Database | RDS PostgreSQL + PostGIS, `db.t4g.micro`, single-AZ, 20 GB gp3, private subnets, encrypted | 17 |
| Load balancer | ALB with ACM certificate; path routing for `/tracking/*` → tracking | 20 |
| Public IPv4, EBS, ECR, Secrets Manager, KMS, CloudWatch Logs, Route 53 | | ~18 |

- **Traffic and DNS.** No NAT gateway; the host egresses directly. Hostnames:
  - `api.staging.deliveryontrack.io` (API, viewer WebSocket, SSE);
  - `track.staging.deliveryontrack.io` (reserved for M4).
- **Migrations** run as a one-off ECS task with the `migrator` role (D23).
- **Secrets.** Secrets live in Secrets Manager. **`DATA_KEYS` are wrapped by KMS** (from M2). Rotation follows `docs/runbooks/data-key-rotation.md`.
- **Deploys.** Each merge to `main` builds signed images (cosign), pushes them to ECR, runs migrations and updates ECS services. A failed health check rolls back.
- **Observability.** CloudWatch Logs (PII-scrubbed as today). Alarms by email on host CPU/memory, ALB 5xx and budget. Traces are off on staging *(Claude's call: no exporter cost)*.
- **Upgrade path.** If the 4 GB host is too small, move to `t3.large` (+€35/month). The owner is told before this happens, not after.

### 7.4 "Before M3 staging" items (PROJECT_CONTEXT §7, now in scope)
- Accept `ip:port` hops in `X-Forwarded-For`.
- `otelhttp.WithPublicEndpoint()` on the gateway.
- A consumer-lag signal for `gateway-apikeys`.
- A bounded NATS drain timeout.
- mTLS for the internal accounts RPC.

## 8. Contracts and docs
- **Additive changes to OpenAPI v1:**
  - the `tracking` object on create;
  - link rotation;
  - the viewer endpoints;
  - the partner `tracking`, `trail` and `proof-points` endpoints;
  - the relay;
  - simulate;
  - the snapshot `courier.signal` field;
  - `ready_at` on create and patch;
  - `eta_window` and `eta_state`;
  - viewer token refresh.
- **Tenant setting:** `default_prep_minutes` in accounts.
- **New events** in `contracts/proto/dt/events/v1/tracking.proto`:
  - `snapshot_updated`;
  - `arrival_detected`;
  - `eta_changed`;
  - `courier_signal_lost` / `courier_signal_restored`;
  - `courier.location_reported` (sampled).
  The `TRACKING` stream is owned by tracking (same pattern as D23 OQ22).
- **Docs.** Starlight pages for tracking links and embedding, the relay, partner tracking data and the simulator (MVP spec §15). A staging runbook covers the account setup steps, deploy, teardown and budget alarms.

## 9. Left to the builder
- Task breakdown and order. Suggestion: tracking core and local p95 first, then partner endpoints, then AWS.
- Internal proto shapes, table schemas and Redis key layout.
- How map matching batches fixes.
- The test client used to measure p95 (Go or TS).
- The exact simplification tolerance.
- The CloudWatch alarm thresholds.
