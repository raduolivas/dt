# DT Phase 1 (MVP): Design Spec

> **Status:** written 2026-10-18, waiting for owner review. **Approved inputs:** decisions D1–D18 in [`../../PROJECT_CONTEXT.md`](../../PROJECT_CONTEXT.md), resolved questions R1–R18 in [`../../OPEN_QUESTIONS.md`](../../OPEN_QUESTIONS.md), and the long-term architecture in [`../../ARCHITECTURE.md`](../../ARCHITECTURE.md).
> This spec defines **what Phase 1 builds**. The implementation plan (next document) defines **in what order and how**.

---

## 1. Shared understanding

### 1.1 What the owner wants (stated)
- A **SaaS on the web** where companies building delivery apps **subscribe** and give their customers a **videogame-like 2D/3D live-tracking map on real locations**, in the style of the existing prototype.
- **Plug-and-play integration in two ways:** redirect the customer to a DT tracking page after payment, or install a DT package in the partner's own front end. Partners authenticate with API keys and receive callbacks.
- The courier shares **live GPS directly** with DT. Most integration happens **on the courier side**.
- **Two business models**, chosen as a plan:
  - **Direct:** the partner's own couriers, e.g. Flink. The customer sees the courier from pickup.
  - **End-to-end:** independent couriers, e.g. Uber Eats. The customer sees the courier from acceptance.
- **Security is the first priority.** Partners must trust DT with location and order data.
- A **professional modular Go backend** with **onion architecture**, not a monolith, refined as partner contracts become clearer.
- **Tests and best practices** from the start, and **public documentation** on the website, written together with the code.
- **Paid integration support** for partners who want help.

### 1.2 Assumptions made in this spec (owner may correct)
- A1. The first market is the **Netherlands/EU**, so all hosting and data stay in the EU.
- A2. Partners create a delivery in DT **only after the customer's payment is confirmed**. DT does not handle the customer's payment.
- A3. The first pilot city for map data is **Amsterdam / the Netherlands**. The map pipeline works for any region.
- A4. Public API JSON uses **snake_case** (the industry convention, as in Stripe and Uber Direct).
- A5. Event bus = **NATS JetStream** and internal RPC = **gRPC via Connect** with Protobuf contracts (chosen in §8.3; owner may override).

### 1.3 Success criteria for the MVP
1. A new partner can **sign up, choose a plan, pay with Stripe (test mode), get sandbox keys** and see a delivery on the 3D map **in under 15 minutes** by following the docs quickstart.
2. Both **Direct** and **End-to-end** deliveries work end to end, with the correct courier visibility rules.
3. A courier phone (through the **Courier SDK** or the **courier web link**) streams GPS, and the customer sees it on real streets with **p95 latency under 1 s** from ingest to viewer.
4. Partners receive **signed webhooks** for every status change and pickup confirmation.
5. The **security baseline** in §11 passes: authorization tests for every endpoint and credential, CI security scans, and no critical findings.
6. The public docs cover every MVP feature.

---

## 2. Scope

**In Phase 1**
- Public **REST API v1**, plus **realtime** (WebSocket with SSE fallback) and **webhooks**.
- **Five Go services:** `gateway`, `deliveries`, `tracking`, `webhooks`, `accounts`. The `accounts` service also covers billing.
- **Tracking experience:** a hosted tracking page plus the `@dt/tracking` web SDK. It runs on a new **real-map renderer** (Three.js on OpenStreetMap vector tiles) with 2D and 3D modes.
- **Courier side:** the `@dt/courier` web SDK plus the **courier web link** page.
- **Partner console:** login with Zitadel and MFA, sandbox/live API keys, webhook endpoints, branding, delivery list and event log, a delivery simulator, plan and billing through Stripe, and purchase of integration support.
- **Public website:** landing, pricing, signup, and docs (Astro Starlight).
- **Infrastructure:** Docker Compose for local development, CI with security scanning, container images ready for an EU cloud.

**Not in Phase 1:** native mobile apps/SDKs, chat/messaging, DT dispatching couriers, partner-specific adapters (only the adapter port exists), geocoding, the marketplace (Stage 3), SOC 2/ISO certification (roadmap).

---

## 3. Actors and credentials

| Actor | Surface | Credential | Lifetime |
|---|---|---|---|
| Partner backend | REST API | **Secret API key** `dt_sk_test_…` / `dt_sk_live_…` | Until rotated or revoked |
| Partner console user | Console (browser) | **Zitadel OIDC session** (MFA required) + role | Session policy in Zitadel |
| Courier device | Courier API | **Courier access token** (JWT, 15 min) + **refresh token** (opaque, rotating, single-use, ≤ 12 h shift) | 15 min / ≤ 12 h |
| Courier (web link) | Courier web page | **One-time link code** exchanged for courier tokens | Code: 15 min, single use |
| Customer | Tracking page / SDK | **Tracking link token** (opaque, 128-bit random, in the URL) exchanged for a **viewer token** (JWT, 15 min, auto-renewed while valid) | Link: until 30 min after a final state |
| DT → partner | Webhooks | **Endpoint signing secret** (HMAC-SHA256) | Until rotated |

Rules:
- Secret keys are **rejected on browser requests**: when an `Origin` header is present, the request fails with `403 secret_key_in_browser`.
- Test and live are fully separated. Every key and token is bound to one `environment` and can never touch the other environment's data.
- Every credential is bound to one tenant. Courier tokens only reach deliveries assigned to that courier. Viewer tokens only reach one delivery.

Console roles (managed in Zitadel, enforced by DT): `owner` (billing, users, everything), `admin` (keys, webhooks, settings), `developer` (keys in test mode, webhooks, logs), `support` (read-only deliveries and logs).

---

## 4. Domain: deliveries, models and lifecycle

### 4.1 Delivery model
`delivery_model` = `direct` | `end_to_end`. The tenant default comes from the plan. It can be overridden per delivery when `tenant.allowed_models` contains both models (D16).

### 4.2 States

| State | Meaning |
|---|---|
| `confirmed` | Paid order handed to delivery. The tracking link exists. |
| `courier_assigned` | **end_to_end only:** an independent courier accepted the job |
| `at_pickup` | The courier is at the store (optional in `direct`) |
| `picked_up` | The handover is confirmed (§4.4). **The delivery session starts.** |
| `arriving` | The courier is close to the customer |
| `delivered` | Final |
| `cancelled` | Final (before `picked_up`) |
| `failed` | Final (after `picked_up`, e.g. customer unreachable) |

### 4.3 Allowed transitions and who may trigger them

| From → To | Partner backend | Courier device | DT (automatic) |
|---|---|---|---|
| ∅ → `confirmed` | ✅ create | | |
| `confirmed` → `courier_assigned` | ✅ assign (end_to_end) | | |
| `confirmed`/`courier_assigned` → `at_pickup` | ✅ | ✅ | |
| `confirmed`/`courier_assigned`/`at_pickup` → `picked_up` | via pickup policy (§4.4) | via pickup policy | |
| `picked_up` → `arriving` | ✅ | ✅ | ✅ when ≤ 200 m or ETA ≤ 90 s |
| `picked_up`/`arriving` → `delivered` | ✅ | ✅ | |
| pre-pickup → `cancelled` | ✅ | | |
| `picked_up`/`arriving` → `failed` | ✅ | ✅ | |

- `confirmed → picked_up` directly is allowed **only** for `direct`.
- `end_to_end` requires `courier_assigned` before `at_pickup` or `picked_up`.
- Assigning a courier in `direct` is allowed and optional (it attaches the courier identity). It does **not** change the state.
- Invalid transitions return `409 invalid_transition`. A duplicate event (same `event_id` or idempotency key) returns the original result.
- Events that arrive out of order are applied by `occurred_at`. A status older than the current state is recorded in history but does not move the state backwards.
- **Partner wins conflicts:** when a courier report and a partner event conflict within 60 s, the partner's state is kept, and the conflict is logged and shown in the event log.

### 4.4 Pickup handover policy
- `tenant.pickup_confirmation` = `any` (default, D16) | `courier` | `store` | `courier_and_store`.
- Confirmations come from:
  - the **courier device**: `by: courier`
  - the **partner backend**: `by: store` (forwarding the store's "handed over") or `by: partner`
- Every confirmation is stored as `delivery.pickup_confirmed {by, at}` and broadcast. When the policy is satisfied, the state becomes `picked_up` and `delivery.status_changed` is broadcast.
- Under `courier_and_store`, a missing second confirmation after 10 minutes raises a warning event (`delivery.pickup_confirmation_pending`). It does not block tracking.

### 4.5 Courier visibility (what the customer sees)

| State | `direct` | `end_to_end` |
|---|---|---|
| `confirmed` | **Preparing view**: 3D map with store + home, animated "preparing" store, ETA; **no courier** | Same preparing view |
| `courier_assigned` | n/a | Courier visible, route courier → store + store → customer |
| `at_pickup` | Courier hidden (still preparing view) | Courier visible at the store |
| `picked_up`, `arriving` | Courier visible, route store/courier → customer | Courier visible |
| final states | Courier hidden immediately; final summary card | Same |

The visibility rule is applied **on the server** in the snapshot projection. Hidden courier data is **never sent** to the viewer: it is not merely hidden in the UI.

---

## 5. Public API v1 (REST)

Base URL `https://api.<domain>/v1`. The source of truth is the **OpenAPI 3.1** file `contracts/openapi/dt.v1.yaml`, which also documents webhooks. Everything below is a summary.

### 5.1 Conventions
- JSON, snake_case. IDs are prefixed ULIDs: `del_…`, `cou_…`, `evt_…`, `whe_…`, `cs_…`.
- **`Idempotency-Key` header is required on every POST.** Keys are kept 24 h. The same key with a different body returns `422 idempotency_mismatch`.
- Errors use **RFC 9457 problem+json**: `{type, title, status, code, detail, request_id, errors[]}`.
- Pagination is cursor-based: `?limit=&starting_after=`.
- Every response carries a `Request-Id` header. Rate-limit headers: `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`.
- Times are RFC 3339 UTC. Coordinates are `{ "lat": 52.37, "lng": 4.90 }` (WGS84).

### 5.2 Partner endpoints (secret API key)

| Method & path | Purpose |
|---|---|
| `POST /deliveries` | Create a delivery (after payment). Body: `external_id`, `delivery_model?`, `pickup {location, name}`, `dropoff {location, label?}`, `customer {first_name?}`, `items_summary?`, `item_count?`, `notes?`, `metadata?` (≤ 50 keys, values ≤ 500 chars, never public). Returns the delivery plus `tracking {url, token, expires_at}`. |
| `GET /deliveries/{id}` · `GET /deliveries` | Read / list (filters: status, model, created range, external_id) |
| `PATCH /deliveries/{id}` | Update dropoff location/label, notes, metadata (dropoff location only before pickup) |
| `POST /deliveries/{id}/assignment` | Assign a courier `{courier_id}`. In `end_to_end` this moves the state to `courier_assigned`. |
| `POST /deliveries/{id}/events` | Report a status `{event_id, type: at_pickup|pickup_confirmed|arriving|delivered|failed, by?: store|partner, occurred_at, reason?}` |
| `POST /deliveries/{id}/cancel` | Cancel (pre-pickup) |
| `DELETE /deliveries/{id}/personal-data` | Erase customer data and notes, and truncate the location trail to the simplified route |
| `PUT /couriers/{external_id}` | Upsert a courier profile `{display_name, vehicle: bicycle|e_bike|scooter|car|on_foot, photo_url?}` |
| `POST /courier-sessions` | Start a courier shift session `{courier_id, mode: sdk|link}`. Returns tokens (sdk mode) or a one-time `link_url` (link mode). |
| `DELETE /courier-sessions/{id}` | End or revoke a session immediately |
| `POST /couriers/{id}/locations` | **Backend relay (fallback):** batch of up to 50 fixes |
| `GET/POST/PATCH/DELETE /webhook-endpoints` | Manage endpoints. `POST /webhook-endpoints/{id}/test` sends a test event. |
| `POST /test/deliveries/{id}/simulate` | **Sandbox only:** drive a simulated courier along the real route, respecting the delivery model |

### 5.3 Courier endpoints (courier access token)

| Method & path | Purpose |
|---|---|
| `POST /courier/token` | Exchange a link code or refresh token for new tokens (refresh tokens rotate and are single-use) |
| `GET /courier/deliveries` | The courier's active deliveries (minimal data: pickup/dropoff location and labels, items summary) |
| `POST /courier/locations` | Batch of up to 50 fixes `{t, lat, lng, accuracy_m, speed_mps?, heading_deg?}` |
| `POST /courier/deliveries/{id}/events` | `at_pickup`, `pickup_confirmed`, `arriving`, `delivered`, `failed` (always `by: courier`) |
| `POST /courier/session/end` | Courier goes off shift. GPS collection stops. |

### 5.4 Viewer endpoints (tracking link → viewer token)

| Method & path | Purpose |
|---|---|
| `POST /tracking/session` | Exchange the link token for a viewer token. Returns the current snapshot plus branding. |
| `GET /tracking/snapshot` | Current snapshot (polling fallback) |
| `WS /tracking/stream` | Realtime. The first client message is `{type:"auth", token}`; the token is never sent in the URL. Server messages: `snapshot`, `status`, `ping`. |
| `GET /tracking/stream/sse` | SSE fallback (fetch-based, `Authorization: Bearer`) |

### 5.5 TrackingSnapshot v1 (wire contract, evolves the prototype's)

```json
{
  "delivery_id": "del_…",
  "delivery_model": "direct",
  "status": "picked_up",
  "courier": {                       // null when not visible (§4.5)
    "display_name": "Sam", "vehicle": "e_bike", "photo_url": null,
    "position": {"lat": 52.371, "lng": 4.897}, "heading_deg": 84, "speed_kmh": 17.5,
    "position_at": "2026-10-18T12:00:03Z"
  },
  "pickup":  {"location": {"lat": 52.368, "lng": 4.903}, "name": "Food Hub"},
  "dropoff": {"location": {"lat": 52.374, "lng": 4.889}, "label": "Home"},
  "route": {"to_pickup": [[lat,lng], …] | null, "to_dropoff": [[lat,lng], …]},
  "eta_seconds": 420, "distance_remaining_m": 1350, "progress": 0.42,
  "timeline": [{"status": "confirmed", "at": "…"}, {"status": "picked_up", "at": "…"}],
  "updated_at": "2026-10-18T12:00:03.410Z",
  "version": 37                      // monotonically increasing per delivery
}
```

The viewer ignores snapshots whose `version` is lower than or equal to the last one it applied.

### 5.6 Webhooks

- **Events:** `delivery.created`, `delivery.courier_assigned`, `delivery.pickup_confirmed`, `delivery.pickup_confirmation_pending`, `delivery.status_changed`, `delivery.eta_changed` (only when the change is ≥ 2 min, at most 1 per minute), `delivery.cancelled`, `courier.session_started`, `courier.session_ended`.
- **Payload:** `{id: evt_…, type, created_at, environment, data: {…}}`.
- **Signature header:** `DT-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>`. The docs ship verification snippets (Node, Go, Python, PHP). Receivers should reject timestamps older than 5 minutes.
- **Delivery:** at-least-once, 10 s timeout. Retries back off exponentially for up to 24 h (about 12 attempts). After that the event goes to the DLQ, where it's visible and replayable in the console. An endpoint is auto-disabled after 3 days of continuous failure, and the owner is emailed.
- **Endpoint URLs:** HTTPS only. Private, loopback and link-local IPs are rejected when the endpoint is created **and again at send time** (anti-SSRF, protecting against DNS rebinding). Requests go out through an egress proxy.

### 5.7 Rate limits (initial; plan-dependent)
- Partner key: 100 req/s burst, 50 req/s sustained.
- Courier session: 1 location batch/s and 10 events/min.
- Viewer token: 5 concurrent streams and 30 snapshot GETs/min.
- Above the limit the API returns `429` with `Retry-After`.

---

## 6. Tracking experience (web)

### 6.1 Integration options
1. **Hosted page (redirect, zero front-end code):** `https://track.<domain>/t/{link_token}`. The partner redirects to it, links to it, opens it in a webview, or embeds it in an `<iframe>` (embedding is allowed on origins configured in the console through the CSP `frame-ancestors` directive).
2. **SDK:** `npm i @dt/tracking`, also available from a CDN script tag.
   ```html
   <dt-tracking token="LINK_TOKEN" mode="3d" theme="auto"></dt-tracking>
   ```
   ```ts
   import { mount } from '@dt/tracking';
   const t = mount(el, { token, mode: '3d', onStatusChange: (s) => …, onDelivered: () => … });
   t.setMode('2d'); t.destroy();
   ```
   SDK events: `ready`, `statuschange`, `etachange`, `delivered`, `error`.

### 6.2 Renderer (`@dt/renderer`, evolved from `src/view` + `src/engine`)
- **Map data:** OpenStreetMap vector tiles in the **OpenMapTiles schema**.
  - **Development:** OpenFreeMap public tiles.
  - **Production:** an NL extract built with **Planetiler** into a **PMTiles** file on object storage + CDN, read with the `pmtiles` JS library.
  - "© OpenStreetMap contributors" attribution is always visible.
- **Scene building:**
  - Load tiles at zoom 14–15 covering the route bounding box + 300 m. Decode MVT (`@mapbox/vector-tile` + `pbf`) and project with the existing local equirectangular `GeoProjection`, using the pickup point as origin.
  - Layers and how they're drawn:

    | Layer | Rendering |
    |---|---|
    | `transportation` | Flat road ribbons, width by class |
    | `building` | Extruded with `render_height` (default 9 m), low-poly, flat-shaded, palette from the prototype |
    | `water`, `park`/`landcover`, `landuse` | Colored ground planes, instanced low-poly trees in parks |

  - Store and home: the building footprint containing the pickup/dropoff point is highlighted and topped with the prototype's store/house markers.
- **Courier:** the prototype's bike/scooter/car models chosen by `vehicle`. Position and heading are interpolated between snapshots (already in `CityScene`).
- **Routes:** the prototype's road-following ribbon, drawn from `route.to_pickup` / `route.to_dropoff`.
- **Modes:** `3d` (isometric/orbit, as the prototype) and `2d` (orthographic top-down camera on the same scene). The user toggles between them, and so does the SDK API.
- **Preparing view (D16):** store and home visible, a looping "preparing" animation on the store, ETA card, no courier.
- **Budgets:**
  - Initial render ≤ 3 s on a mid-range phone over 4G.
  - Tile payload ≤ 2 MB per delivery.
  - ≥ 30 fps on mobile, 60 fps on desktop.
  - Geometry is built in a **Web Worker**.
- **Fallback:** without WebGL, a status card plus a static route line on a 2D canvas.
- **Accessibility:** the status timeline and ETA are real DOM text (screen-reader friendly), reduced motion is respected, and color contrast follows AA.
- **Theming:** tenant branding (logo, primary color, light/dark/auto) from the console, delivered with the viewer session.

### 6.3 Courier side
- **`@dt/courier` SDK (web):**
  - Takes the session tokens.
  - Samples `navigator.geolocation.watchPosition` with high accuracy, filters fixes with accuracy > 50 m, batches every 2–5 s, buffers offline (≤ 10 min, IndexedDB) and refreshes tokens automatically.
  - Exposes `reportStatus()`.
  - Stops collecting on `endSession()` or when no active delivery is left.
- **Courier web link** (`https://courier.<domain>/s/{code}`): a mobile page built on the SDK. It shows the "start shift" consent screen (what is collected and why), the active deliveries, large status buttons (At store / Picked up / Delivered) and a **"keep this screen on"** notice. It uses the Screen Wake Lock API where available. Known limitation: background GPS isn't possible in browsers, and this is documented.

---

## 7. Partner console and website

- **Website (Astro + Starlight)** at `www.<domain>`: landing, pricing (two plans: Direct / End-to-end, plus the support add-on; prices from OQ12, placeholder copy until then), signup CTA, docs at `/docs`, legal pages, security page, status page link.
- **Console (React SPA)** at `console.<domain>`:
  - Login through **Zitadel** (OIDC + PKCE), MFA required, one Zitadel organization per tenant.
  - **Onboarding wizard:** company name, then delivery model(s), then Stripe Checkout, then sandbox keys and quickstart.
  - **Sections:**
    - **Overview:** usage, active deliveries.
    - **Deliveries:** list, detail with live map, full event log incl. pickup confirmations and conflicts.
    - **Simulator:** create a sandbox delivery and drive it.
    - **Developers:** API keys (create, roll with 24 h overlap, revoke, IP allowlist), webhooks (endpoints, test sends, delivery attempts, DLQ replay), request log (last 7 days).
    - **Settings:** delivery models, pickup policy, branding with live preview, allowed embed origins, data retention.
    - **Team:** invite, roles.
    - **Billing:** Stripe Customer Portal, buy integration support.
  - **Copy-paste quickstart:** curl/Node/Go/Python snippets with the tenant's **test** key pre-filled. Live keys are shown only once, at creation.
- **Billing (Stripe):** Products: `Direct` plan, `End-to-end` plan, `Integration Support` (one-time or package). The `accounts` service consumes Stripe webhooks, which are signature-verified and idempotent. **Live keys are only enabled with an active subscription.** Test mode is free.

---

## 8. Backend architecture

### 8.1 Services and ownership

| Service | Owns (data) | Responsibilities | Publishes | Consumes |
|---|---|---|---|---|
| **gateway** | api key cache (read model), courier sessions, tracking links, idempotency records, audit log | Public edge: TLS termination behind the load balancer; authentication of every credential type; authorization scopes; rate limits; idempotency; secret-key-in-browser check; **token issuing** (courier, viewer); routing to services over gRPC; request logging | `courier.session_started/ended`, `audit.recorded` | `apikey.*` (from accounts), `delivery.status_changed` (to expire tracking links) |
| **deliveries** | couriers, deliveries, delivery history, pickup confirmations | Delivery aggregate, models, state machine, pickup policy, conflict rule, personal-data erasure | `delivery.*`, `courier.assigned` | `tracking.arrival_detected` |
| **tracking** | location trail (partitioned), route cache, snapshots | Location ingest (validation, plausibility, map-matching via OSRM `/match`), routing and ETA (OSRM `/route`), **snapshot projection with visibility rules**, realtime WebSocket/SSE fan-out, simulator driver | `tracking.snapshot_updated`, `tracking.arrival_detected`, `tracking.eta_changed`, `courier.location_reported` (sampled, for usage) | `delivery.*` |
| **webhooks** | endpoints (secrets encrypted), attempts, DLQ | Map domain events to public webhook events, sign, deliver, retry, replay | `webhook.delivery_failed` | `delivery.*`, `tracking.eta_changed`, `courier.session_*` |
| **accounts** | tenants, settings, console users ↔ Zitadel, API keys (hashes), plans, subscriptions, usage | Tenant lifecycle and settings, key management, Stripe, usage metering, branding | `tenant.*`, `apikey.created/revoked`, `subscription.*` | Stripe webhooks, `delivery.created` (usage), `courier.location_reported` (usage, sampled) |

**Location hot path:** courier → gateway (auth, rate limit) → **tracking** over a gRPC stream. Tracking publishes `courier.location_reported` internally, updates the snapshot in Redis, and fans out to connected viewers. Viewers' WebSockets connect **directly to tracking** (path-routed by the load balancer). Tracking verifies viewer JWTs with gateway's public JWKS.

### 8.2 Onion architecture inside every service

```
services/<name>/
  cmd/<name>/main.go            composition root: config, wiring, start
  internal/
    domain/                     entities, value objects, domain events, invariants (stdlib only)
    app/                        use cases (commands/queries) + ports (interfaces) for repos, bus, clocks, providers
    adapters/
      inbound/{grpc,http,consumer}/   translate requests/events → use cases
      outbound/{postgres,nats,redis,osrm,stripe,zitadel}/  implement ports
  migrations/                   SQL migrations for this service's schema only
  api/                          generated code (connect/protobuf, oapi) — never edited by hand
```

- **Dependency rule:** `domain` ← `app` ← `adapters` ← `cmd`. It is enforced in CI with `depguard` (golangci-lint) rules per layer, and the build fails if it's violated.
- **No shared domain code between services.** The shared `backend/pkg/` holds only technical libraries: observability, authn verification, outbox, testkit, problem+json, config.
- **Adapter port for partner translators (D3):** `deliveries/internal/app` defines an `InboundTranslator` port. No concrete partner adapters are built in Phase 1.

### 8.3 Communication and contracts
- **Public:** REST described by OpenAPI 3.1 (`contracts/openapi/dt.v1.yaml`), server stubs generated with **oapi-codegen** (strict server) in gateway, and TS types generated for the web.
- **Internal sync:** **gRPC via Connect (connect-go)**, defined in `contracts/proto/dt/<service>/v1/*.proto` and managed with **buf** (lint + breaking-change check in CI).
- **Events:** **NATS JetStream**, one stream per domain (`DELIVERIES`, `TRACKING`, `ACCOUNTS`, `GATEWAY`). Payloads are Protobuf in `contracts/proto/dt/events/v1/`, wrapped in an envelope `{event_id, type, schema_version, tenant_id, environment, occurred_at, source, subject}`.
  - Each service publishes through a **transactional outbox** (an outbox table in the same DB transaction, plus a relay goroutine).
  - Consumers are **idempotent**: an `inbox` table keyed by `event_id`.
  - Courier location pings skip the outbox: they are ephemeral and loss-tolerant, published directly with an at-most-once tolerance.
- **Versioning:** additive changes only within v1. Breaking changes need v2 and a deprecation period, and buf/oasdiff breaking checks run in CI.

### 8.4 Data storage
- **PostgreSQL 16+ with PostGIS:** one cluster in the MVP, **one schema and one DB role per service**. A role cannot read other schemas. **Row-level security** on `tenant_id` + `environment` is a second safety net behind query scoping.
- **Redis:** rate limits, idempotency fast path, hot snapshots, viewer fan-out across tracking instances (pub/sub), token deny-list.
- **Location trail:** a table partitioned by day in tracking's schema. Raw fixes are kept 30 days by default (tenant-configurable). A nightly job reduces older trails to a simplified route per delivery.
- **Object storage + CDN:** map tiles (PMTiles), 3D assets, tenant logos.
- **Encryption:** at rest by the platform. **Field-level encryption** (AES-GCM, envelope keys from a KMS) for customer first name, notes, webhook secrets and refresh-token material.

### 8.5 External dependencies

| Dependency | Use | Failure behavior |
|---|---|---|
| OSRM (self-hosted, NL extract, bike + car profiles) | `/route`, `/match` | Last known route + straight-line ETA; snapshot flags `eta_quality: "estimated"` |
| Zitadel (cloud, EU region) | Console login | Console unavailable; API unaffected |
| Stripe | Billing | Subscription state cached; grace period 72 h before live keys are restricted |
| Map tiles CDN | Renderer | Retry; fallback view after 10 s |

---

## 9. Key flows

1. **Signup:** website → Zitadel signup (MFA enrollment) → console onboarding → `accounts` creates the tenant and test keys → Stripe Checkout → webhook `checkout.session.completed` → subscription active → live keys can be created.
2. **Create delivery (after payment):** partner `POST /deliveries` → gateway (auth, idempotency) → deliveries creates `confirmed` + outbox `delivery.created` → gateway issues the tracking link → response. Then tracking builds the route pickup → dropoff and a snapshot (preparing view), and webhooks sends `delivery.created`.
3. **End-to-end acceptance:** partner `POST /courier-sessions` (if the courier isn't already on shift) and `POST /deliveries/{id}/assignment` → `courier_assigned` → broadcast. Tracking adds the `to_pickup` leg, and the courier becomes visible.
4. **Live GPS:** courier SDK → `POST /courier/locations` → gateway → tracking (validate, match, ETA, project) → Redis pub/sub → viewers' WebSockets.
5. **Pickup:** the courier taps "Picked up" (`by: courier`) and/or the partner posts `pickup_confirmed by: store`. Deliveries applies the policy, moves to `picked_up` and broadcasts. The viewer switches out of the preparing view (direct) or to the dropoff leg (end_to_end).
6. **Arrival and delivery:** tracking detects ≤ 200 m / ETA ≤ 90 s → `tracking.arrival_detected` → deliveries `arriving`. Then the courier or partner reports `delivered`, the final state is broadcast, the courier is hidden, and the tracking link expires 30 min later.

---

## 10. Reliability, performance and observability

- **SLO targets (MVP):**
  - API p95 < 200 ms (excluding OSRM).
  - Location ingest → viewer p95 < 1 s.
  - Webhook first attempt < 5 s after the event.
  - Availability 99.5%.
- **Capacity target per tracking instance:** 500 location fixes/s and 10k concurrent viewer connections. All services are stateless and scale horizontally; state lives in Postgres, Redis and NATS.
- **Graceful shutdown:** drain WebSockets with a reconnect hint. Viewers reconnect with backoff and resync from `GET /tracking/snapshot`.
- **Observability:**
  - **OpenTelemetry** traces across gateway → services → NATS consumers.
  - Structured JSON logs (`slog`) with `request_id`, `tenant_id` and `event_id`. **PII scrubbing** in the logger: no coordinates at info level, no names or notes.
  - RED metrics per endpoint, consumer lag per stream, webhook success rate per tenant.
  - Health endpoints `/livez` and `/readyz`.

---

## 11. Security (first priority, D15)

### 11.1 Baseline controls
- **Secrets:**
  - API keys are 32 random bytes, shown once, stored as **HMAC-SHA256 with a server pepper**.
  - Lookup by key prefix, with constant-time comparison.
  - Keys can be rolled with overlap and are revocable instantly through a `apikey.revoked` event to gateway's cache (≤ 5 s).
- **Tokens:**
  - JWTs are signed with **Ed25519**, keys rotated every 30 days and published on internal JWKS with `kid`.
  - Short lifetimes, an `aud` claim per surface (`courier`, `viewer`), and a deny-list for revoked sessions.
- **Transport and web:**
  - TLS 1.2+ with HSTS.
  - Strict CSP on all web apps; `frame-ancestors` from tenant settings for the hosted page.
  - CORS: only the tracking/courier endpoints allow browser origins. The partner API never does.
  - Secure, HttpOnly, SameSite cookies for the console's BFF session (backend-for-frontend served by gateway). OIDC tokens are never stored in localStorage.
- **Input:**
  - Strict schema validation on every request (generated from OpenAPI).
  - Payload limits: 64 KB for partner requests, 16 KB for location batches.
  - Coordinate range checks; GPS plausibility (≤ 200 km/h, no jumps over 1 km in under 10 s, which are flagged and dropped).
- **Authorization:**
  - Centralized in gateway by credential type and scope, **plus** a tenant/environment check in every service query, **plus** RLS.
  - An **authorization matrix test** covers every endpoint × every credential type (none, wrong tenant, wrong env, wrong courier, expired, revoked) in CI.
- **Audit log:** every authenticated write records actor (key id / session id / console user), action, target, IP, user agent and time. It's visible to the tenant `owner`/`admin` and retained 1 year.
- **Supply chain and CI:**
  - `govulncheck`, `golangci-lint` (incl. gosec), `npm audit` / osv-scanner, **gitleaks**, **CodeQL**, Trivy image scans, SBOM (CycloneDX).
  - Signed container images (cosign), Dependabot, pinned GitHub Actions.
  - Branch protection with required review.
- **Runtime:** distroless/non-root containers, read-only filesystems, least-privilege DB roles, secrets from the platform secret manager (never in env files committed to git), webhook egress through a proxy.
- **Before launch:** an independent **pen test**, an OWASP ZAP baseline scan in CI against staging, and a public **Security & Privacy** page plus responsible-disclosure policy (`security.txt`).

### 11.2 Threat model (STRIDE summary)

| Threat | Example | Mitigations |
|---|---|---|
| **Spoofing** | Stolen partner API key used by an attacker | Hashed storage, IP allowlist option, instant revoke/roll, anomaly alert on new IP/volume, key prefix makes leaked keys scannable |
| | Fake GPS from a forged courier | Courier tokens bound to courier + tenant + env, short-lived, plausibility checks, only assigned deliveries |
| | Forged webhooks sent to partners in DT's name | HMAC signatures with timestamp; documented verification |
| **Tampering** | Replayed status events | Idempotency keys, `event_id` dedupe, timestamp windows |
| | Courier marks "delivered" falsely | State machine, partner-wins conflict rule, audit log, partner can require `store` confirmation for pickup |
| **Repudiation** | "We never sent that cancel" | Immutable audit log with actor + request id |
| **Information disclosure** | Leaked tracking URL | Opaque token, minimal data (no address text required, first name only), courier hidden outside the window, expiry 30 min after the end, revocable |
| | Cross-tenant data access (IDOR) | Tenant scoping in gateway + every query + RLS + authz matrix tests |
| | PII in logs or analytics | Logger scrubbing, no third-party analytics on the tracking page in the MVP |
| | Location history breach | Encryption, retention limits, DB role isolation |
| **Denial of service** | Flood of pings or viewer connections | Rate limits per key/session/token/IP, payload caps, connection caps, autoscaling, CDN in front of static assets |
| **Elevation of privilege** | Test key touching live data | Environment bound into every credential and every query |
| | `developer` role creating live keys | Role checks in accounts + gateway; live keys require `admin`/`owner` |
| | SSRF via webhook URL | HTTPS only, private-range blocking at create and send time, egress proxy |

---

## 12. Privacy (GDPR)

- **Roles:** the partner is the controller and DT is the processor. A DPA template and a sub-processor list are published: cloud host, Zitadel, Stripe (billing data only), email provider.
- **Data minimization:** customers are represented by coordinates, an optional first name and an optional note. No phone or email. Couriers are a display name, an optional photo and a vehicle.
- **Purpose limitation:** GPS is collected only during an active session with an active delivery. The courier web link shows a consent screen.
- **Retention defaults:** raw GPS 30 days. Delivery records 13 months (partner-configurable down to 30 days). Audit log 1 year. Erasure API (§5.2).
- **EU-only hosting and processing.**

---

## 13. Testing strategy

| Level | What | Tools |
|---|---|---|
| Domain unit | State machine (table-driven over every state × event × actor × model), pickup policies, visibility rules, conflict rule; **property-based tests** for transition invariants | `go test`, `testing/quick` or `rapid` |
| Application | Use cases with in-memory fakes for ports | `go test` |
| Adapter integration | Postgres/PostGIS repos + RLS, NATS outbox/inbox, Redis limits, OSRM client (recorded fixtures) | **testcontainers-go** |
| Contract | Responses validated against the OpenAPI file; buf + oasdiff breaking checks; generated TS types compile | kin-openapi, buf, oasdiff |
| Security | **Authorization matrix** for every endpoint; webhook signature tests; SSRF blocklist tests; **Go fuzzing** for parsers (locations, events, webhooks URL) | `go test -fuzz` |
| Front end | Renderer pure functions (tile decoding, projection, mesh building), SDK state, console components | **Vitest** |
| End to end | Docker Compose full stack: signup with Stripe test mode, create delivery for **both models**, simulate courier, assert map states, webhook received and verified, SDK embed sample app | **Playwright** |
| Performance | Ingest 500 fixes/s, 10k WS viewers, p95 latency | **k6** |

**Coverage:** domain and app layers ≥ 90%, overall ≥ 75%. Every bug fix comes with a regression test. Development follows TDD.

---

## 14. Repository layout and tooling

```
dt/
  backend/                    Go workspace (go.work)
    services/{gateway,deliveries,tracking,webhooks,accounts}/
    pkg/                      technical shared libs only
  contracts/
    openapi/dt.v1.yaml
    proto/dt/{events,deliveries,tracking,accounts,webhooks}/v1/
  web/                        pnpm workspace + Turborepo
    packages/{contract,renderer,sdk-tracking,sdk-courier,ui}/
    apps/{tracking-web,courier-web,console,site}/
  infra/
    compose/                  local stack: postgres+postgis, redis, nats, osrm, zitadel (dev), stripe-cli, mailpit
    tiles/                    Planetiler config + scripts for the NL PMTiles build
    osrm/                     extract + profile scripts
    deploy/                   container/deploy manifests (EU cloud, provider to be chosen)
  docs/                       project docs (this spec, architecture, context, open questions)
  .github/workflows/          ci.yml (lint, test, security), e2e.yml, release.yml
```

- The current prototype (`src/`) moves to `web/packages/renderer` and `web/packages/simulator` logic, and its `TrackingSnapshot` type is replaced by the generated v1 contract.
- **Tooling:**
  - Go: current stable release pinned in `go.work`, `golangci-lint`, `sqlc` + `pgx`, `golang-migrate`, `oapi-codegen`, `buf` + `connect-go`, `nats.go`, `coder/websocket`, OpenTelemetry SDK, `slog`.
  - TS: pnpm, Turborepo, Vite, Vitest, Playwright, ESLint, Prettier, Three.js, `pmtiles`, `@mapbox/vector-tile`.
  - `make dev` starts everything locally. `make test`, `make lint` and `make gen` regenerate contracts.

---

## 15. Public documentation (Starlight, written alongside each milestone)

1. **Overview:** how DT works (diagram), the two plans, the three credentials.
2. **Quickstart:** sandbox key, create a delivery, open the tracking link, simulate (target: under 15 minutes).
3. **Concepts:** deliveries and states (diagram per model), courier visibility, pickup confirmation, environments, idempotency, errors, pagination, versioning.
4. **Guides:** hosted page / redirect / iframe · `@dt/tracking` SDK · courier integration (`@dt/courier`, courier web link, backend relay) · webhooks (verify signatures, retries, replay) · going live checklist.
5. **API reference:** generated from OpenAPI (REST + webhooks).
6. **SDK reference:** tracking SDK (options, events, theming), courier SDK.
7. **Security & trust:** credentials and token lifecycle, data handled, GDPR roles and DPA, retention, courier privacy, responsible disclosure.
8. **Testing:** sandbox, simulator, test webhooks.
9. **Plans, billing and integration support.**
10. **Changelog.**

---

## 16. Milestones (input for the implementation plan)

| # | Milestone | Done when |
|---|---|---|
| M0 | Foundations: monorepo, CI with security scans, Compose stack, contracts skeleton, `pkg/` libs | `make dev` and `make test` work; CI green |
| M1 | `accounts` + `gateway`: tenants, API keys, authentication, rate limits, idempotency, audit log | Authz matrix tests green for the key endpoints |
| M2 | `deliveries`: models, state machine, pickup policy, couriers, outbox events | Domain tests cover every transition × actor × model |
| M3 | `tracking`: ingest, OSRM, snapshot projection with visibility, realtime, simulator | A simulated delivery streams snapshots to a test client at p95 < 1 s |
| M4 | **Renderer on real maps** + hosted tracking page + `@dt/tracking` SDK | Amsterdam delivery renders in the prototype's style in 2D/3D; both models' views correct |
| M5 | `@dt/courier` SDK + courier web link + courier sessions | A real phone drives a sandbox delivery end to end |
| M6 | `webhooks` service | Signed webhooks with retries, DLQ and replay; SSRF tests green |
| M7 | Console (Zitadel) + Stripe billing + onboarding | A new partner reaches sandbox keys and live activation through Stripe test mode |
| M8 | Website + docs + quickstart | An outside tester completes the quickstart in under 15 minutes |
| M9 | Hardening: k6 load tests, ZAP scan, threat model review, pen test prep | §1.3 success criteria all met |

---

## 17. Open items (do not block the plan)

- **OQ12: plan prices**, free tier limits and the integration-support price. Placeholder copy until decided.
- **Cloud provider** (EU region) for staging and production. Decide before M3 deploys to staging. Candidates: GCP `europe-west4` (Netherlands), AWS `eu-central-1`/`eu-west-1`, Scaleway/OVH (EU-owned).
- **Product name and domain** (`<domain>` placeholder in this spec).
- **A5 confirmation:** NATS JetStream + Connect/gRPC (owner may prefer alternatives).
