# DT (Delivery Tracker): Project Context

> Portable state file. It works without any AI-specific memory. Share it with any assistant or person to pick up where we left off.
> **Last updated:** 2026-10-18
> **Open questions (shareable on their own):** [`OPEN_QUESTIONS.md`](OPEN_QUESTIONS.md)
> **MVP spec:** [`superpowers/specs/2026-10-18-dt-mvp-design.md`](superpowers/specs/2026-10-18-dt-mvp-design.md) (written 2026-10-18, owner review pending)
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

- Repo: `~/Documents/ai_projects/DT`, commit `48014ee` "create project prototype".
- **Prototype:** Three.js + Vite + TypeScript. A stylized isometric city where a courier bikes to a store, picks up the order and delivers it. Simulator only, with no backend.
- **Key design already in place:** the engine is decoupled from the view through a data contract (`src/engine/types.ts`):
  - `TrackingSnapshot`: orderId, status, courier {position lat/lng, heading, speedKmh}, store, destination, route polylines, ETA, distance, progress, timestamps.
  - `TrackingSource`: `subscribe(listener) → unsubscribe`. Swapping the simulator for a WebSocket source gives live data.
- **Prototype-only limits:** fixed fake city, a router that only works on the street grid, the bike drawn about 6× real scale, and a simple ETA.
- See `README.md` for the architecture and how to run it (`npm install && npm run dev`).

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
1. ~~Write the spec~~ → done (2026-10-18). **Owner reviews the spec** and confirms the remaining open items in its §17.
2. Write the step-by-step implementation plan (milestones M0–M9 from spec §16).
3. Build with tests (TDD), milestone by milestone.
