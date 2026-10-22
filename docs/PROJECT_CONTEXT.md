# DT (Delivery Tracker): Project Context

> Portable state file. It works without any AI-specific memory. Share it with any assistant or person to pick up where we left off.
> **Last updated:** 2026-10-22
> **Open questions (shareable on their own):** [`OPEN_QUESTIONS.md`](OPEN_QUESTIONS.md)
> **MVP spec:** [`superpowers/specs/2026-10-18-dt-mvp-design.md`](superpowers/specs/2026-10-18-dt-mvp-design.md) (approved 2026-10-19)
> **Current plan:** M2 deliveries: [`superpowers/plans/2026-10-22-m2-deliveries.md`](superpowers/plans/2026-10-22-m2-deliveries.md). **Status: implemented on branch `m2-deliveries` (all 11 tasks, `make ci` + `make smoke` pass locally); PR pending.** Decisions C1–C12 were made by Claude at the owner's request (recorded as D22). Previous: M1 [`superpowers/plans/2026-10-20-m1-accounts-gateway.md`](superpowers/plans/2026-10-20-m1-accounts-gateway.md) (merged 2026-10-21, PR #8). Previous: [`superpowers/plans/2026-10-19-m0-foundations.md`](superpowers/plans/2026-10-19-m0-foundations.md) (M0, merged 2026-10-20)
> **Architecture draft:** [`ARCHITECTURE.md`](ARCHITECTURE.md) (v0.1, under review)

## 1. What DT is

DT is a **SaaS that gives delivery apps a videogame-like 2D/3D live-tracking experience**, built on **real map locations**. It aims to be easy to integrate, simple to interact with and flexible to skin.

**Target customers**
- Existing delivery platforms (food, quick-commerce: Uber Eats, Flink, Thuisbezorgd style) that want to give their end customers a better tracking UI.
- **Startups building delivery apps from scratch**, which subscribe to DT and use our API instead of building tracking themselves.

**Business model:** subscription SaaS. Partners subscribe and integrate through our API, and a translation/adapter layer handles partners whose data doesn't fit our model.

## 2. Roadmap (owner's 3 stages)

1. **Stage 1 (current):** build DT itself. The 2D/3D tracking UI runs on real maps, keeps the current stylized look, and comes with a strong partner API (consider pub/sub). Also planned: a courier phone app where couriers see deliveries they can pick up.
2. **Stage 2:** a B2B proposal and go-to-market for companies that want the videogame-style UI.
3. **Stage 3:** DT becomes a full marketplace app (like Uber Eats / thuisbezorgd.nl) with 3 user types: consumer, courier and provider (store). Stores are real businesses (Google Maps business profile), and they can claim a listing to show products in the "DT world" (a "multiverse"). This is secondary for now.

## 3. Decisions made

| # | Decision | Date |
|---|---|---|
| D1 | Follow existing live-tracking industry patterns (Uber Direct, DoorDash Drive, Stuart, Onfleet). Partners do the integration work on their side. | 2026-10-15 |
| D2 | **Ingest:** the partner backend calls the DT REST API (one API key per tenant). It sends order lifecycle events (`created → accepted → at_pickup → picked_up → arriving → delivered`) and courier location pings every few seconds. | 2026-10-15 |
| D3 | **Adapter layer:** a per-partner adapter translates non-standard payloads into canonical DT events. We stay flexible early on, learn one model that fits all partners, and evolve the API additively. | 2026-10-15 |
| D4 | **Core:** canonical event model plus an internal pub/sub bus. `TrackingSnapshot` is the read model built from events. | 2026-10-15 |
| D5 | **Outbound:** WebSocket/SSE streams to tracking viewers, plus webhooks to partners on status changes. | 2026-10-15 |
| D6 | **Contract:** versioned public docs (OpenAPI for REST, AsyncAPI for events). | 2026-10-15 |
| D7 | **Build order:** architecture first, then stack brainstorming, then implementation. Build our own end-to-end solution first: 2D/3D on **real map locations**, keeping the current stylized UI. | 2026-10-15 |
| D8 | **Phase 1 = web only.** The website (signup + subscription), the Go API, the web tracking experience (2D/3D) and the public docs. Native apps come later. | 2026-10-18 |
| D9 | **Two integration options for partners:** (1) **Hosted redirect**: after checkout the partner's app redirects or links the customer to a DT tracking URL; (2) **Installable SDK** in the partner's own front end. In both, the partner's **backend** creates the delivery with its secret API key; the browser only ever gets a short-lived tracking token. Partner receives updates through signed webhooks ("callback listeners"). | 2026-10-18 |
| D10 | **Backend language: Go** (owner preference; confirmed as a good fit for the API + WebSocket fan-out). | 2026-10-18 |
| D11 | **Paid integration support** is offered as a purchasable add-on for partners who want help integrating. | 2026-10-18 |
| D13 | **Backend is modular, not a monolith.** It is split by bounded context, and each module/service follows **onion architecture**: the domain core has no dependencies; application/use cases come next; adapters/infrastructure (HTTP, DB, bus, Stripe, OSRM) sit at the outside, and dependencies always point inward. It follows big-tech best practices, uses versioned contracts between services, and evolves incrementally as partner contracts become clearer. How many deployable services to start with is still open (OQ5b in OPEN_QUESTIONS.md). Replaces the earlier modular-monolith proposal. | 2026-10-18 |
| D14 | **Courier-side integration and trust model.** The courier shares live GPS directly with DT, and most integration happens on the courier side. There are three channels, each with its own credential: ① partner backend uses a secret API key (orders, metadata, mints courier sessions); ② courier device uses a short-lived courier session token (GPS, status, chat); ③ customer browser uses a tracking token (live map, chat). The partner is the GDPR controller and DT is the processor, with data minimization, encryption, retention limits and an audit log. Customer ↔ courier **messaging through DT** is in scope, and messages are mirrored to the partner via webhooks. See ARCHITECTURE.md §12; follow-ups OQ4a–e. | 2026-10-18 |
| D15 | **Delivery models, status and security (answers OQ4a–e).** a) Courier GPS goes **directly from the phone to DT** (backend relay as fallback). b) **Two delivery models, chosen as a plan at signup:** **Direct** (partner's own couriers, e.g. Flink; the customer sees the live map from pickup) and **End-to-end** (independent couriers accept jobs, e.g. Uber Eats; the customer sees the courier from acceptance, including the ride to the store). The courier is visible only after payment is confirmed AND a courier is in charge. c) **No chat in the MVP** (Messaging stays designed for later). d) Couriers can report status; pickup is confirmed by courier and/or store and/or partner under a per-tenant policy, each confirmation is broadcast to all parties, and the delivery session starts at pickup. e) **Security is the first priority.** DT does not dispatch couriers. See ARCHITECTURE.md §3, §12. | 2026-10-18 |
| D16 | Product rules: **mixed delivery models** are allowed per delivery when the plan allows it; the **default pickup confirmation is `any`** (first of courier/store/partner, configurable per partner); **before the courier is visible** the customer sees the 3D map with store + home, an animated "preparing" store and the ETA. | 2026-10-18 |
| D17 | **Courier web link is in the MVP** (OQ2). **Maps = option (a)**: own Three.js renderer on OpenStreetMap vector tiles + OSRM (OQ3). **Day-one services:** gateway, deliveries, tracking, webhooks, accounts-billing; messaging later (OQ5b). | 2026-10-18 |
| D18 | **Providers:** Stripe for DT subscriptions (OQ7), Zitadel for partner console login with MFA (OQ8), partners send lat/lng in the MVP with no geocoding (OQ9). | 2026-10-18 |
| D19 | **MVP design approved** by the owner (OQ1). Spec written: `docs/superpowers/specs/2026-10-18-dt-mvp-design.md` (adds a threat model, API v1 summary, lifecycle tables, testing strategy, milestones M0–M9). Spec choices waiting for owner review: NATS JetStream for events, gRPC via Connect for internal calls. | 2026-10-18 |
| D20 | **MVP spec approved** by the owner. Implementation is split into **one plan per milestone (M0–M9)**. The first plan, **M0 Foundations**, is at `docs/superpowers/plans/2026-10-19-m0-foundations.md`; it covers the monorepo, onion service skeletons, schema per service, contracts, CI and security scans, plus the roadmap for M1–M9. | 2026-10-19 |
| D21 | **M1 choices confirmed** by the owner: NATS JetStream for events and gRPC via Connect for internal calls (spec A5, part of OQ14); public `GET /v1/me` endpoint to check a key (OQ15); the rate limiter **fails open** when Redis is down, while authentication keeps running (OQ15). Plus the plan's defaults: key format `dt_sk_<env>_<lookup><secret>` with a peppered HMAC, internal accounts RPC + `dtctl` until the console (M7), RLS deferred to M2. Execution: subagent-driven. *Amended by implementation:* Connect is **v2** (`connectrpc.com/connect/v2` v2.0.1, buf plugins pinned), and the audit log records **every authenticated write except a `429`** (also failed writes, e.g. 4xx), not only successful ones. | 2026-10-20 |
| D22 | **M2 deliveries: choices made by Claude at the owner's request ("just decide"); please review.** **C1** tenant delivery settings live in `accounts` (`dtctl tenant settings`); `deliveries` keeps an event-fed read model and uses defaults until it arrives. **C2** a delivery keeps the pickup policy it was created with. **C3** deferred: tracking link in the create response, courier sessions/endpoints, automatic `arriving` from tracking, the `InboundTranslator` port (M3/M5). **C4** conflict rule: within 60 s of the other side's applied transition, a partner report targeting another valid state overrides it (`overrode: true`); a courier report against the partner's is recorded as `conflict` and ignored. **C5** out-of-order: a report behind the current state is `stale` (200, recorded) when its `occurred_at` is older than the last applied one, otherwise `409 invalid_transition`; repeats are `stale`, known `event_id`s are `duplicate`. **C6** RLS (enabled and forced) on the deliveries tables; gateway and accounts tables get none. **C7** field encryption now (AES-256-GCM, local key ring `DATA_KEYS`; a cloud KMS wraps the keys at the first staging deploy); metadata and dropoff label are sealed too. **C8** erasure only on final deliveries (`409 delivery_active`); it clears name, notes, label and metadata, while coordinates stay until the 13-month retention job. **C9** idempotency keeps storing `400`s and now hashes the query string. **C10** `courier_id` in assignment is DT's `cou_` id, returned by `PUT /couriers/{external_id}`. **C11** `DELIVERIES` stream keeps 7 days; events carry no customer personal data. **C12** reporting returns `{outcome, delivery}`. *Controller rulings during implementation:* **R1/R5** the conflict rule only fires when the report contradicts the other side (current to T is not allowed for the reporter's actor and L.From to T is allowed), L is the latest applied state-changing entry and its actor must be on the other side, and T must not be behind L.To in progression rank (confirmed < courier_assigned < at_pickup < picked_up < arriving < delivered = failed = cancelled), so a late report of an earlier state is `stale`, never an override; `pickup_confirmed` reports skip the conflict and stale steps and follow the pickup rules. **R6** the pickup watch's `since` is the first confirmation's `occurred_at` (a backdated confirmation can trigger the 10-minute warning early). **R7** the RLS policies use `current_setting('dt.…', true)`, so a query outside a tenant transaction fails closed (sees no rows) instead of erroring. **R8** (final review) the gateway's stored idempotency responses can carry customer data, so they are sealed with the gateway's own `DATA_KEYS` (AES-256-GCM, bound to tenant, environment and key), each record names its delivery, and a successful `DELETE /personal-data` deletes that delivery's records (a failed purge is logged, the records expire within 24 h). Also from the final review: an invalid `tenant.settings_set` is rejected (terminated) instead of blocking the ordered consumer, and control characters (U+0000–U+001F, U+007F) in text fields are `400 validation_failed` (`invalid_characters`). | 2026-10-22 |
| D12 | Build to best practices **with tests** from the start; the public docs are written together with the code and live in the website. | 2026-10-18 |

## 4. Open questions

> The up-to-date, standalone list is in [`OPEN_QUESTIONS.md`](OPEN_QUESTIONS.md). Keep both in sync.

- **Q1. Front-end packaging:** *Resolved by D9 (2026-10-18): hosted redirect page + installable web SDK, both in Phase 1. Native SDKs later.*
- **Q2. Courier location source:** *Answered by D14 (2026-10-18): the courier shares live GPS with DT; three-channel trust model; messaging in scope. Follow-ups OQ4a–e in OPEN_QUESTIONS.md.*
- **Q3. Real maps + stylized 3D:** *Answered by D17 (2026-10-18): option (a), our own renderer on OpenStreetMap vector data.*
- **Q4. Deployment shape:** *Answered by D13 (2026-10-18): modular services with onion architecture, not a monolith. Follow-up OQ5b (service granularity) still open.*
- **Q5. Dispatch-lite (offer/accept for Mode F):** Stage 1 or later? *Proposed:* Stage 1b.
- **Q6. Tech stack:** Go backend decided (D10); the rest is proposed in §6.

## 5. Current code state

- Repo: `~/Documents/ai_projects/DT`. M0 Foundations merged to `main` 2026-10-20 (PR #1, all CI checks green). M1 merged 2026-10-21 (PR #8, plus PR #9 fixing CI after dependabot bumps). Current work branch: `m2-deliveries`.
- **Monorepo:**
  - `backend/`: Go 1.27.3 workspace (`go.work`). Five service skeletons (`gateway`, `deliveries`, `tracking`, `webhooks`, `accounts`), each in onion layout (`cmd/`, `internal/{domain,app,adapters}`, `migrations/`), enforced by golangci-lint `depguard`. Shared technical libraries in `pkg/`: `logx` (JSON logs with PII redaction), `httpx` (problem+json errors, `Request-Id`, panic recovery, access log, `/livez` + `/readyz`, graceful shutdown), `bootstrap` (service wiring from env), `testkit` (real Postgres in tests). Generated contract code in `gen/`.
  - `contracts/`: OpenAPI 3.1 skeleton (`openapi/dt.v1.yaml`) and the Protobuf event envelope (`proto/dt/events/v1`), managed by buf.
  - `web/`: pnpm + Turborepo workspace. The prototype now lives in `web/packages/renderer` (`@dt/renderer`); `@dt/contract` holds the TypeScript types generated from OpenAPI.
  - `infra/compose/`: local stack. Postgres 18 + PostGIS with **one schema and one login role per service**; integration tests prove no role can read or write another's schema.
  - Hardened images: distroless, non-root, read-only root filesystem, all capabilities dropped, `no-new-privileges`.
  - CI (`.github/workflows/`): Go lint/tests/govulncheck, web lint/tests/build, contract lint + breaking check + codegen drift, gitleaks, Trivy image scan + SBOM, a `make dev` smoke test, CodeQL, Dependabot. All actions pinned to commit SHAs.
- **M2 deliveries** (branch `m2-deliveries`, PR pending):
  - `accounts`: per-tenant delivery settings (`allowed_models`, `default_model`, `pickup_confirmation`) set with `dtctl tenant settings` and published as `tenant.settings_set`.
  - `deliveries` (port 8081): domain state machine (direct / end_to_end, pickup policies, conflict and stale rules), couriers, Postgres with forced RLS and sealed personal data, internal Connect RPC (`dt.deliveries.v1`, same bearer token as accounts), settings consumer, pickup sweeper (10 min warning), outbox to the `DELIVERIES` stream (7 days).
  - `gateway`: 9 public routes: `POST/GET /v1/deliveries`, `GET/PATCH /v1/deliveries/{id}`, `POST .../assignment`, `POST .../events`, `POST .../cancel`, `DELETE .../personal-data`, `PUT /v1/couriers/{external_id}`; scopes `deliveries:read|write`, `couriers:write`.
  - `make smoke` now runs `scripts/smoke-m1.sh` then `scripts/smoke-m2.sh` (couriers, the delivery lifecycle, duplicate, invalid transition, erasure, NATS assertion).
- **M1 accounts + gateway** (branch `m1-accounts-gateway`, awaiting CI + merge):
  - `accounts`: tenants (`ten_…`, `live_enabled`) and partner API keys (`dt_sk_<env>_<lookup><secret>`, HMAC-SHA256 with the shared `APIKEY_PEPPER`, plaintext shown once). Create, roll (24 h overlap) and revoke with role rules (live keys need owner/admin and `live_enabled`). Exposed as an **internal Connect v2 RPC** (`dt.accounts.v1.AccountsService`, port 8084, `Authorization: Bearer <INTERNAL_RPC_TOKEN>`, never routed publicly; error codes in a `dt-code` trailer).
  - Events: `accounts.apikey.*` through a transactional **outbox** (`pkg/outbox`) to **NATS JetStream** (file storage, 2 min dedup window, `Nats-Msg-Id = event_id`); the gateway keeps a key read model with an **inbox** (each event applied once, in stream order). A revoked key is rejected in well under 5 s (smoke: ~0.3 s).
  - `gateway` (public `/v1` edge, port 8080): secret-key authn (Bearer only; query-string or Basic keys are 401), scopes, environment rule, per-key IP allowlist with `TRUSTED_PROXIES` for `X-Forwarded-For`, `403 secret_key_in_browser` when a secret key comes with an `Origin`, 64 KiB body limit, `GET /v1/me`.
  - Rate limits in **Redis** (GCRA, 50 req/s + 100 burst per key with `RateLimit-*` headers; 20 failed auths/min per IP), **failing open** when Redis is down.
  - **Idempotency** (`Idempotency-Key` on every POST, Postgres, 24 h, replay / `422` mismatch / `409` in-flight).
  - **Audit log** (append-only Postgres table, 365 days): every authenticated write except a `429`.
  - **OpenTelemetry** traces (`pkg/otelx`), and the compose stack gains Redis and NATS.
  - Dev tooling: `dtctl` (`backend/tools/dtctl`, dev CLI over the accounts RPC), `make seed` (a "Dev Tenant" and a test key), `make smoke` (`scripts/smoke-m1.sh`, end-to-end, also in CI). The CI codegen drift check now also covers sqlc and oapi-codegen output.
- **Commands:** `make dev` / `make down` (local stack), `make seed` / `make smoke` (after `make dev`), `make test`, `make lint`, `make gen`, `make ci` (all checks), `make web-dev` (renderer). See `README.md`.
- **Known local issue:** on the owner's machine, rootless Docker's bridge network has no internet access, so `make dev` cannot download Go modules during the image build. Fix Docker networking or build with host networking (see `README.md`, Troubleshooting).
- The prototype's `TrackingSnapshot` / `TrackingSource` contract still drives the renderer. The generated v1 contract replaces it in M3–M4.

## 6. Phase 1 (MVP) design: approved 2026-10-18 (D19); full detail in the MVP spec

- **Maps (decided D17, option a):** OpenStreetMap vector tiles in the OpenMapTiles schema (free hosted OpenFreeMap tiles while developing, then our own Netherlands extract built with Planetiler as a PMTiles file on our CDN). Our Three.js renderer decodes the tiles (roads, buildings with `render_height`, water, parks) into low-poly meshes in the prototype's style. The 2D mode is the same scene with a top-down orthographic camera. Routing, ETA and road snapping come from self-hosted **OSRM** (bike/car profiles, `/route` + `/match`). Partners send lat/lng (geocoding comes later). OSM attribution is shown.
- **Backend:** Go, modular services per bounded context with onion architecture (D13). Proposed services: gateway, deliveries, tracking, webhooks, accounts-billing. Stack: stdlib `net/http` router, OpenAPI-first with oapi-codegen, pgx + sqlc, golang-migrate), PostgreSQL + PostGIS, Redis (pub/sub fan-out + hot snapshots), WebSocket with SSE fallback, transactional outbox, signed webhooks with retries. Tests: `go test`, testcontainers for Postgres/Redis, contract tests against the OpenAPI spec.
- **Front end:** a TypeScript pnpm monorepo: `contract` (types generated from OpenAPI), `renderer` (evolved prototype), `sdk-js` (`<dt-tracking>` web component + `DT.mount()`), `tracking-web` (hosted page), `console` (React SPA), `site` (Astro + Starlight: marketing, pricing, docs). Tests: Vitest + Playwright.
- **Billing (D18):** Stripe Billing (Checkout + Customer Portal + webhooks); the support add-on is a Stripe product.
- **Console login (D18):** Zitadel (OIDC, EU region, MFA required).
- **Plug-and-play aids:** sandbox/live keys, a copy-paste quickstart with keys prefilled, a "simulate a delivery" button, a webhook test sender, a per-delivery event log.
- **Integration flow (both options, D9):**
  ```
  Customer pays ──► Partner backend ──POST /v1/deliveries (secret key)──► DT API
                                     ◄── { id, tracking_url, tracking_token }
       Option A "Redirect":  redirect / link / iframe → tracking_url        (zero front-end code)
       Option B "SDK":       npm i @dt/tracking → <dt-tracking token="…">   (inside their own app)

  Partner backend ──POST /v1/deliveries/{id}/events + /locations──► DT ──live──► map in the browser
  DT ──signed webhooks (delivery.status_changed…)──► partner "callback listener"
  SDK ──front-end events (onStatusChange, onDelivered)──► partner UI
  ```
  The secret key never reaches the browser; the browser only gets a short-lived tracking token for one delivery.
- **Why Go:** handles thousands of open WebSocket connections cheaply, deploys as one binary, is a common choice for public APIs. The front end stays in sync because TypeScript types are generated from the OpenAPI file.
- **Public docs outline (written together with the code, published in the website):** 1) Overview with diagram · 2) Quickstart (5 min: create a delivery, open its tracking link) · 3) Concepts (deliveries, status flow, tracking tokens, test vs live) · 4) Guides (redirect integration, SDK integration, **courier integration: Courier SDK, courier web link, backend relay**, **messaging**, webhooks) · 5) API reference (generated from OpenAPI) · 6) SDK reference (options, theming, front-end events) · 7) **Security & trust** (the three credentials, token lifecycle, webhook signatures, GDPR roles/DPA, data minimization, retention, courier privacy), errors, rate limits · 8) Testing with the simulator · 9) Plans and paid integration support · 10) Changelog.
- **Courier web link (decided D17: in the MVP).** A mobile web page where a startup's courier taps "start" and the phone's GPS (`navigator.geolocation.watchPosition`) feeds the live map, so startups without a courier app still get live tracking. Limitation: browsers stop sending GPS when the screen turns off, so it's a starter, not a replacement for a native courier app.
- **Out of MVP:** native apps, chat/messaging, dispatch, concrete partner adapters (only the adapter interface), geocoding, a marketplace.

## 7. Next steps

**Owner (business and decisions):**
1. Answer the remaining questions in `OPEN_QUESTIONS.md` (suggested order is at the top of that file), ending with OQ1 (approve the MVP design).
2. Define the two plans and pricing (Direct / End-to-end), a free test tier and the price of integration support (OQ12).
3. Find 1–2 **pilot partners** (one with its own fleet, one with independent couriers) to validate the API contract early.
4. Legal and trust documents (with a lawyer): Terms of Service, Privacy Policy, Data Processing Agreement (DT = processor), sub-processor list.
5. Accounts: company name and domain, payment provider (Stripe/Mollie), EU cloud hosting account, a GitHub organization.
6. Brand and website content: name, logo, tagline, landing page copy.

**Engineering:**
1. ~~Write the spec~~ → approved 2026-10-19.
2. ~~Write the M0 implementation plan~~ → done 2026-10-19.
3. ~~**M0 Foundations**~~ → merged to `main` 2026-10-20 (PR #1, CI green on GitHub).
4. ~~**M1 accounts + gateway**~~ (tenants, API keys, authn/scopes, rate limits, idempotency, audit log, NATS + outbox) → all 14 tasks done 2026-10-21 on branch `m1-accounts-gateway` (`make ci` + `make smoke` pass locally); merged to `main` 2026-10-21 (PR #8; Go 1.27.3, grpc v1.83.2, rate-limit test flake fixed). PR #9 restored sqlc in the contracts job (lost in a merge) and pinned TypeScript 6.0.3 (dependabot #5 bumped it to 7, merged with red checks). Branch protection on `main` applied 2026-10-21 (PR required, 12 CI checks required, no force-push/deletion, conversation resolution). Plan: `docs/superpowers/plans/2026-10-20-m1-accounts-gateway.md`.
5. ~~**M2 deliveries**~~ (models, state machine, pickup policy, couriers, outbox events, RLS) -> implemented 2026-10-22 on branch `m2-deliveries`; PR pending. Plan: `docs/superpowers/plans/2026-10-22-m2-deliveries.md`.
5b. **Next: M3** (tracking, courier sessions, tracking link). Each milestone gets its own plan the same way.
6. **Follow-ups from the M1 reviews** (not blocking; pick up in the milestone named):
   - ~~**M2:**~~ done in M2 (authz matrix extended, `400`s stored and query hashed, see D22): extend the authz matrix (per-route success status, request bodies, audit rows) with the first real POST routes; decide whether idempotency should store validation `400`s and hash the query string; clear the unused OpenAPI components; add `gofmt` to golangci.
   - **From M2 (open):** a cloud KMS wrapping `DATA_KEYS` and a key-rotation procedure (first staging deploy); a location-trail truncation on `delivery.personal_data_erased` and the 13-month retention job (M3); tracking and webhooks tables get RLS in their own milestones; the owner-role question and the lat/lng schema format (OPEN_QUESTIONS OQ16, OQ17); courier-self-repeat after a partner override (OQ18). **Before M3** (final M2 review): per-delivery ordering of delivery events (OQ19), the `reason` free text and erasure (OQ20), unbounded history (OQ21), a single owner for the `ACCOUNTS` stream config (OQ22), a write racing an erasure (OQ23); retired gateway keys must stay 24 h after rotation (OQ24). Watch `TestIdempotencyConcurrentBegin` (gateway), which failed once during the final fix wave and passed on reruns.
   - **Before M3 staging:** accept `ip:port` hops in `X-Forwarded-For`; `otelhttp.WithPublicEndpoint()` on the gateway before any trace exporter; a consumer-lag signal for `gateway-apikeys` (a NATS/relay outage silently pauses the ≤ 5 s revocation guarantee); bounded `nats` drain timeout; mTLS for the internal accounts RPC.
   - **M7:** what a lapsed subscription (`live_enabled = false`) does to existing live keys.
   - **M9:** alert when an `apikey.revoked` event is rejected as poison; make the failed-auth limiter block before the DB lookup and count `ip_not_allowed`; pepper rotation; new-IP/volume anomaly alerts.
