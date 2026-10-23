# DT: Open Questions

> Standalone file for sharing with another AI or a person. It's self-contained, so it can be read without the other files.
> **Last updated:** 2026-10-23 (OQ16–OQ24 answered, see R22; cloud provider chosen, see R23; OQ14 tech part and OQ15 answered 2026-10-20) · Full context: [`PROJECT_CONTEXT.md`](PROJECT_CONTEXT.md) · Architecture: [`ARCHITECTURE.md`](ARCHITECTURE.md)

## Context in 30 seconds

**DT (Delivery Tracker)** is a SaaS that gives delivery apps a **videogame-like 2D/3D live-tracking map** built on **real locations**. There are two kinds of customers: existing delivery platforms that want a better tracking UI, and **startups building delivery apps from scratch**. Partners subscribe on our website and integrate through our API. They can either **redirect** their customer to a DT tracking page after checkout, or **install our SDK** in their own front end. Partners send order events and courier GPS from their backend using a secret API key, and DT sends status changes back through signed webhooks. **Phase 1 is web only**, with a **Go** backend, tests from the start, and public docs on the website. The existing prototype is a stylized isometric 3D city in Three.js + TypeScript, and its look must be kept. Partners choose one of **two delivery models** when they sign up: **Direct** (they have their own couriers, like Flink) or **End-to-end** (independent couriers accept jobs, like Uber Eats). **Security is the first priority.**

---

## Suggested order to answer

1. **OQ14**: product name and domain (brainstorm in progress)
2. **OQ12**: business (plans and pricing), can be answered later

---

## Open

### OQ12. Plans and pricing *(new, business)*
Two plans exist: **Direct** and **End-to-end**. Still to define:
- Prices and what they're based on (per delivery, per month, tiers)
- A free test (sandbox) tier
- Limits
- The price of paid integration support

### OQ14. Product name and domain *(from the MVP spec §17; tech part answered 2026-10-20 (R20), cloud provider 2026-10-23 (R23))*
- **Product name and domain.** "DT" is the working name until then.
- **Leading candidate (2026-10-23): "Arrivo".** A DNS check found the short domains already registered: .com, .io, .app, .eu, .nl, .co, .dev, .to (several of them parked, so they may be for sale). Free in that check: `arrivo.delivery`, `arrivomap.com`, `arrivo3d.com`, `playarrivo.com`. An unrelated US transport-tech company called Arrivo (arrivo-loop.com, 2016) exists. Still to do: an EUIPO/WIPO trademark search in classes 9, 39 and 42, and choosing the domain.


---

## Resolved (kept for reference)

| # | Question | Answer | Date |
|---|---|---|---|
| R1 | Where does the end customer see the tracking? | Both a **hosted redirect page** and an **installable web SDK** in Phase 1. Native SDKs later. | 2026-10-18 |
| R2 | Backend language? | **Go** | 2026-10-18 |
| R3 | First platform? | **Web only** in Phase 1 | 2026-10-18 |
| R4 | Integration help for partners? | Offered as a **paid add-on** | 2026-10-18 |
| R5 | How do partners integrate data? | REST API with a per-tenant API key, plus a translation/adapter layer for partners whose data doesn't fit our model. They follow industry patterns (Uber Direct, DoorDash Drive, Onfleet). | 2026-10-15 |
| R6 | Backend shape? | **Modular, not a monolith.** Split by bounded context; each module/service follows **onion architecture** (domain core → application/use cases → adapters/infrastructure, with dependencies pointing inward). Follow big-tech best practices. Contracts are versioned and the domain model gets refined incrementally as we learn more about partner contracts. Granularity still open (OQ5b). | 2026-10-18 |
| R7 | Courier location source? | **The courier shares live GPS with DT, and most of DT's integration happens on the courier side.** Because partners share location and order data with a third party, strong API authentication and data protection must earn their trust. Customer ↔ courier **messaging through DT** is in scope, and messages are also delivered to the partner's side. Design: three channels with separate credentials (partner secret key / courier session token / customer tracking token), GDPR processor model, data minimization, Messaging context. See ARCHITECTURE.md §12. | 2026-10-18 |
| R8 | GPS path, visibility, chat, status, security priority (OQ4a–e) | **a)** Courier phone sends GPS **directly to DT** (backend relay as fallback). **b)** Customer sees the courier only after **payment is confirmed AND a courier is in charge**. This depends on the delivery model: **Direct** (own fleet, e.g. Flink) shows the map from pickup and the customer never sees a courier riding to the store; **End-to-end** (independent couriers, e.g. Uber Eats) shows it from courier acceptance, including the ride to the store. Partners choose the model as a **plan** when they sign up. **c)** **No chat in the MVP** (Messaging stays designed for later). **d)** Couriers **can** report status. Pickup is confirmed by courier and/or store (e.g. McDonald's) and/or partner, and each confirmation is broadcast to all parties; the delivery session starts at pickup. In End-to-end, courier acceptance is broadcast the same way. **e)** **Security is the first priority.** See ARCHITECTURE.md §3 and §12. | 2026-10-18 |
| R9 | Does DT dispatch couriers (old OQ6)? | **No.** The partner or its courier app decides who takes a job and tells DT. Dispatch-lite remains a possible later add-on. | 2026-10-18 |
| R10 | Can a partner mix delivery models (OQ10)? | **Yes.** The model is set per tenant by the plan and can be overridden per delivery when the plan allows it (e.g. own fleet + overflow to independent couriers). | 2026-10-18 |
| R11 | Default pickup confirmation rule (OQ11)? | **`any`**: the first confirmation from courier, store or partner wins. Each partner can change it to `courier`, `store` or `courier_and_store`. | 2026-10-18 |
| R12 | What does the customer see before the courier is visible (OQ13)? | The **3D map with the store and the customer's home, the store animated as "preparing", and the ETA**, with no courier until pickup (Direct) or acceptance (End-to-end). | 2026-10-18 |
| R13 | Courier web link in the MVP (OQ2)? | **Yes.** A mobile web page built on the Courier SDK: the courier taps "start" and the phone's GPS feeds the live map. Known limitation: browsers pause GPS when the screen is off. | 2026-10-18 |
| R14 | Map rendering approach (OQ3)? | **(a) Own renderer on open map data:** OpenStreetMap vector tiles (OpenMapTiles schema) rendered by our Three.js engine in the prototype's low-poly style, 2D = top-down view of the same scene, OSRM for routing/ETA/road snapping. | 2026-10-18 |
| R15 | Services on day one (OQ5b)? | **Five Go services:** gateway, deliveries, tracking, webhooks, accounts-billing. **messaging** is added later with chat. Each has onion architecture, owns its data, and talks to the others only through versioned contracts. | 2026-10-18 |
| R16 | Payment provider for DT subscriptions (OQ7)? | **Stripe:** Billing for the plans (Direct / End-to-end), Customer Portal, invoices, card + SEPA; integration support sold as a Stripe product. | 2026-10-18 |
| R17 | Console login for partners (OQ8)? | **Zitadel** (OIDC, EU data residency, MFA required, enterprise SSO later, open source so it can be self-hosted later). API keys and courier/tracking tokens remain DT's own system. | 2026-10-18 |
| R18 | Geocoding (OQ9)? | **Partners send latitude/longitude in the MVP**; address → coordinates comes later. | 2026-10-18 |
| R19 | Approve the MVP design (OQ1)? | **Yes.** Spec written: `superpowers/specs/2026-10-18-dt-mvp-design.md`. | 2026-10-18 |
| R20 | Internal tech (OQ14 part)? | **Confirmed:** NATS JetStream for events, gRPC via Connect for service-to-service calls. | 2026-10-20 |
| R21 | `GET /v1/me` and rate-limiter outage behavior (OQ15)? | **Yes** to a public `GET /v1/me` (check a key, first quickstart call). When Redis is down the rate limiter **fails open** (requests are still authenticated, a warning is logged). | 2026-10-20 |
| R22 | M2 follow-ups (OQ16–OQ24)? | **Claude's recommendations accepted.** **OQ16** a separate `migrator` role owns the tables and is used only by the deploy pipeline; runtime roles get DML grants only, so they can't weaken RLS. **OQ17** the contract declares `format: double` for `lat`/`lng`; regenerate the models and drop the gateway's own `float64` wire structs. **OQ18** a courier repeating its state after a partner override is `200 conflict` (recorded and ignored). **OQ19** delivery events carry a per-delivery `version`, incremented under the row lock; consumers drop events with an older version. **OQ20** `reason` is sealed and cleared on erasure. **OQ21** stale/recorded reports are capped per delivery (e.g. 200); past the cap only a counter goes up; state-changing entries are always stored. **OQ22** only `accounts` defines the `ACCOUNTS` stream; `deliveries` waits for it and is not ready until it exists. **OQ23** `{id}` routes set `resource_id` when the idempotency record starts, and the erasure purge also deletes in-progress records. **OQ24** a retired gateway data key stays in `DATA_KEYS` for 24 h after rotation (written into the key-rotation procedure). | 2026-10-23 |
| R23 | Cloud provider (OQ14 part)? | **AWS `eu-central-1` (Frankfurt)** for staging and production (RDS Postgres + PostGIS, KMS for wrapping `DATA_KEYS`, Secrets Manager). | 2026-10-23 |
