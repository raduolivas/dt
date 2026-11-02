# M5: courier sessions, `@dt/courier` SDK and the courier web link (design)

Status: draft for owner review, 2026-10-31. Inputs: MVP spec `2026-10-18-dt-mvp-design.md` §3, §4.3–§4.5, §5.2,
§5.3, §5.7, §6.3, §8 (gateway row), §16 (M5); `docs/ARCHITECTURE.md` §12; M3 design
`2026-10-26-m3-tracking-design.md` §5 (ingest, purpose limitation); D14, D15, D26, D27.

The owner asked Claude to write this design without a question round (2026-10-31: "skip the questions, write the
design"). Every decision that the MVP spec or a recorded decision (D-number) did not already make is marked
**(Claude's call)**. The owner reviews those calls while reading.

## 1. Intent

Until now, the only ways a courier position reaches DT are the partner relay and the sandbox simulator. M5 builds
the path the MVP was designed around (D15): **the courier's phone sends GPS straight to DT**. It serves two kinds of
partners (MVP §1):

- **Partners with their own courier app** embed the **`@dt/courier` web SDK**. Their backend starts a courier
  session and hands the tokens to the app.
- **Startups without a courier app** use the **DT courier web link**. Their backend starts a session in link mode,
  sends the courier a one-time link (SMS, WhatsApp, their own tool), and the courier opens it in the phone browser.
  No install and no account.

Both paths feed the same tracking pipeline M3 built (`Ingest`, with `source: sdk` or `link`). Courier status taps
(At store, Picked up, Delivered) go into the M2 state machine with `by: courier`. That unblocks the `courier` and
`courier_and_store` pickup policies, which no real client could satisfy before M5.

**Success:** a courier opens a link on a real phone, taps "Start shift", rides to the store and on to the customer,
and taps through the statuses. Meanwhile the customer's tracking page (M4) shows them moving, with nothing else
running: no relay and no simulator.

## 2. Goals

1. **Courier sessions** in the gateway: start, refresh, end and revoke, with the credentials in MVP §3. The access
   token is a JWT valid for 15 min. The refresh token is opaque, rotating and single use, and the session lasts at
   most 12 h. The link code is single use and valid for 15 min.
2. **Partner endpoints:** `POST /courier-sessions`, `GET /courier-sessions/{id}` and
   `DELETE /courier-sessions/{id}`.
3. **Courier endpoints** (MVP §5.3): `POST /courier/token`, `GET /courier/deliveries`, `POST /courier/locations`,
   `POST /courier/deliveries/{id}/events` and `POST /courier/session/end`.
4. **`@dt/courier`** (web SDK): GPS sampling, filtering, batching, an offline buffer, token refresh, status
   reports, and stopping when the session ends or no active delivery is left.
5. **The courier web link page** (`web/apps/courier-page`), built on the SDK. It has a consent screen, the active
   deliveries, large status buttons and a screen wake lock. It is in English and Dutch.
6. **An end-to-end proof:** a Playwright test where a courier page with emulated GPS drives a delivery and a
   tracking page in another browser context shows the courier and the status changes.
7. **Docs:** a courier integration guide (SDK and link), the OpenAPI additions, and the security notes.

## 3. Non-goals

- Native iOS/Android SDKs (Phase 2, MVP §1).
- **Background GPS.** Browsers can't do it, and this is documented (MVP §6.3). The page keeps the screen on and
  warns the courier when it's hidden.
- Chat and messaging (D15c).
- Offers or accepting jobs: dispatch-lite is M10 (D26). The partner still assigns couriers. M5 only keeps that door
  open (§6.9).
- Courier accounts, passwords or courier login. Only the partner's backend can start a session.
- Proof-of-delivery photos or signatures.
- Webhooks for `courier.session_*`. The events are published in M5, and M6 maps them to webhooks.
- A console screen for sessions (M7).
- New courier personal data. The MVP §12 list (display name, optional photo, vehicle) stays as it is.

## 4. Anti-goals (these would make M5 a failure even if it technically works)

- **GPS collected when it shouldn't be:** after the session ends, after the shift expires, with no active
  delivery, or before the courier agreed on the consent screen.
- **A courier token reaching data it shouldn't:** another courier's deliveries, another tenant, the other
  environment, or customer data beyond what the courier needs to deliver (§6.4).
- **A link that works twice**, or a revoked session that keeps sending fixes for up to 15 minutes.
- **A status tap lost without the courier knowing.** A tap made offline is queued and retried, and the page shows
  it as "not sent yet" until DT confirms it.
- **A page that kills the battery or freezes** on the reference phone (iPhone SE 3rd gen, Safari).
- **The courier page looks like an internal admin form.** It doesn't need the M4 game look, but it must be clear and
  friendly, and usable with one thumb while the courier stands next to a bike.

## 5. Constraints

- Onion layering (D13). The gateway owns courier sessions and token issuing (MVP §8 gateway row). Tracking and
  deliveries are not told about sessions. They receive a courier id and a `source`. Deliveries gains only the small
  read and actor changes in §6.10.
- Every credential is bound to one tenant and one environment (MVP §3).
- Purpose limitation (M3 §5) stays in tracking: fixes are accepted only while the courier has a non-final
  delivery assigned to them. The SDK also stops sampling then (§6.6), so the rule holds on both ends.
- Visibility (MVP §4.5) does not change. The courier's own app sees its own position. What the customer sees is
  still filtered on the server.
- Rate limits (MVP §5.7): per session, 1 location batch per second (burst 5) and 10 events per minute.
  `POST /courier/token`: 10 per minute per session. The token exchange also has a per-IP limit for link codes
  (Claude's call).
- Limits on `/courier/locations` are the same as the relay's: ≤ 50 fixes and ≤ 16 KB.
- AWS stays paused (D28). The staging pieces (§8) are written and tested offline only.

## 6. How it works

### 6.1 Sessions and tokens (gateway)

- **Session row** `courier_sessions` *(Claude's call on the fields)*:
  - `id` (`cs_…`), tenant, environment, `courier_id` (`cou_…`), `mode` (`sdk` | `link`);
  - `started_at`, `expires_at` (default 12 h; the partner may ask for less with `expires_in_seconds`, minimum
    15 min), `ended_at`, `end_reason` (`courier` | `partner` | `expired` | `replaced` | `refresh_reuse`);
  - a hash of the current refresh token and its generation;
  - in link mode, a hash of the link code and the code's expiry.
- **One active session per courier** *(Claude's call)*. Starting a new session for a courier ends the old one with
  `end_reason: replaced`. This stops two phones from sending competing GPS for one courier, and it is the obvious
  recovery when a courier loses their phone: the partner just starts a new session.
- **Access token:** an Ed25519 JWT with `aud: courier`, valid for 15 min. Claims: tenant, environment, `sub` (the
  courier id), `sid` (the session id), `src` (`sdk` | `link`) and scopes `location:write status:write`. It is
  signed with a key of its own `kid`, published in the same JWKS as the viewer key. Viewer and courier tokens never
  verify as each other because the audience differs *(Claude's call: reuse `pkg/viewerjwt`'s signer behind a
  generalized package, keeping a separate key per audience)*.
- **Refresh token:** 256-bit random and opaque. Only its hash is stored, peppered with a domain prefix so it can
  never match an API key or link token (closes M3 follow-up 12 for the new tokens). Each use returns a new pair, and
  the old refresh token stops working. **Reuse of an old refresh token ends the session** (`refresh_reuse`),
  because a reused token means it was copied (Claude's call, the standard OAuth practice).
- **Link code:** 128-bit random, single use, valid for 15 min. Exchanging it returns the first token pair. A second
  exchange fails with `401 link_code_used`, and the session stays alive for the phone that used it first.
- **Revocation is immediate.** Ending a session writes `sid` to a Redis deny-list with a TTL equal to the longest
  remaining access-token life. Every courier request checks it, as ARCHITECTURE §12 requires. When a session
  expires, its tokens simply run out.
- **Events** (gateway outbox): `courier.session_started {session_id, courier_id, mode}` and
  `courier.session_ended {session_id, courier_id, reason}`. No personal data is included. Webhooks map them in M6.
- **Audit:** session start, end and revoke are audit rows. Token refreshes are not, to avoid noise.

### 6.2 Partner endpoints (secret API key)

| Endpoint | Behaviour |
|---|---|
| `POST /courier-sessions` | Body `{courier_id, mode, expires_in_seconds?}`; idempotent with `Idempotency-Key`. **sdk:** `{session, access_token, access_expires_at, refresh_token}`. **link:** `{session, link_url, link_expires_at}`, where `link_url` is `https://courier.<domain>/s/{code}`. `404` for an unknown courier. |
| `GET /courier-sessions/{id}` | `{id, courier_id, mode, status: pending_link \| active \| ended, started_at, expires_at, ended_at, end_reason, last_seen_at}` |
| `DELETE /courier-sessions/{id}` | Ends the session (`end_reason: partner`). Idempotent: ending an ended session returns `200` with the session. |

The tokens in an sdk-mode response are shown once and are never stored or logged in plaintext. That includes the
idempotency record: it is sealed (M2-R8), and a replayed response returns the same tokens only within the
idempotency window *(Claude's call: needed so a retry after a lost response still works)*.

### 6.3 Courier endpoints (courier access token)

| Endpoint | Behaviour |
|---|---|
| `POST /courier/token` | `{grant_type: link_code, code}` or `{grant_type: refresh_token, refresh_token}`. Returns `{access_token, access_expires_at, refresh_token, session_expires_at}`. No bearer is needed. |
| `GET /courier/deliveries` | The courier's non-final assigned deliveries in this environment (§6.4). It has a shape that can grow an `offers` list later (§6.9). |
| `POST /courier/locations` | `{fixes: [{t, lat, lng, accuracy_m, speed_mps?, heading_deg?}]}` → tracking `Ingest` with `source` from the token's `src`. Returns `{accepted, dropped}` (the same as the relay). |
| `POST /courier/deliveries/{id}/events` | `{event_id, type, occurred_at, reason?}` with type `at_pickup`, `pickup_confirmed`, `arriving`, `delivered` or `failed` → deliveries `ReportEvent` with actor courier. Returns `{outcome, delivery}` (D22 C12, limited to the §6.4 fields). `404` if the delivery isn't assigned to this courier. |
| `POST /courier/session/end` | Ends the session (`end_reason: courier`). |

- CORS: the courier endpoints accept any origin without cookies, because partners' courier apps run on their own
  domains and the token travels in the `Authorization` header (Claude's call). The secret-key-in-browser rule is
  unchanged.
- The M1 authorization matrix test is extended so every route is checked with every credential type: secret key,
  viewer token, courier token, a courier token from another tenant, environment or session, and an expired or
  revoked token.

### 6.4 What a courier may see (data minimisation)

For each assigned non-final delivery the courier sees: `id`, the partner's `reference`, `model`, `status`,
`ready_at`, pickup `{lat, lng, label}`, dropoff `{lat, lng, label, notes}`, an items summary, and the pickup
confirmations so far (so the page can say "Waiting for the store"). It does not see the customer name, phone or
email, metadata, history, or other couriers *(Claude's call: the courier needs the dropoff label and notes to find
the door; the partner's own channel covers contacting the customer)*.

### 6.5 Status taps and the pickup policy

- **Picked up** sends `pickup_confirmed`. Deliveries applies the tenant's policy (MVP §4.4):
  - under `any` or `courier`, the delivery moves to `picked_up`;
  - under `store`, it is recorded and has no effect, and the page says "The store confirms pickup";
  - under `courier_and_store`, it waits for the store, and the page says "Waiting for the store to confirm".
- **Conflicts** follow D22 (C4, C5). The partner wins within 60 s, and the page shows the delivery's real status from
  the response, not the button the courier tapped.
- **Arriving** is not a button. Tracking already detects arrival (`tracking.arrival_detected`). The endpoint
  accepts `arriving` for SDK partners that want to send it.
- **Can't deliver** (`failed`) asks for a short reason from a fixed list plus "Other" *(Claude's call)*.
- Every tap carries a client-made `event_id`, so retries are safe (M2 `duplicate`).

### 6.6 `@dt/courier` (web SDK)

```ts
const courier = createCourier({ api, tokens?: {...}, linkCode?: string, storage? })
courier.start()            // asks for location permission, begins sampling when there is an active delivery
courier.deliveries         // current list; courier.on('deliveries', fn)
courier.report(deliveryId, type, { reason? })  // queued, retried, resolves with the server outcome
courier.end()              // ends the session and stops GPS
courier.on('state', fn)    // { gps: 'off'|'asking'|'denied'|'on', network: 'online'|'offline', buffered, session: 'active'|'ended'|'expired' }
```

- **Sampling (MVP §6.3):** `watchPosition` with high accuracy. Fixes with accuracy worse than 50 m are dropped. A
  batch is sent every 3 s *(Claude's call within the spec's 2–5 s)*, or sooner if it holds 50 fixes.
- **Offline buffer:** fixes and status taps go to IndexedDB, falling back to memory. Fixes older than 10 min are
  dropped. Taps are never dropped; they stay until sent or until the session ends, and are sent first and in order
  when the network returns.
- **Collection stops** when the session ends, expires or is revoked (`401` or `403`), or when the deliveries list
  is empty. While the list is empty, the SDK checks every 20 s for new work and starts again when work appears
  *(Claude's call)*.
- **Deliveries refresh:** every 10 s while the page is visible, after every tap, and on resume *(Claude's call:
  polling is simple and cheap at this scale; a push channel can come later without breaking the API)*.
- **Tokens:** they refresh 60 s before expiry. A failed refresh after a network error retries; a `401` ends the
  session.
- **Storage:** the default is `localStorage` under one key on the page's origin, so a reload keeps the shift going.
  The single-use link code can't be exchanged twice, so without this a reload would lose the session. Partners can
  pass their own `storage` (Claude's call).
- It has no dependency on `@dt/viewer` or the renderer, and has a small bundle (size limit in CI, like M4).

### 6.7 Courier web link page

- It is served at `courier.<domain>/s/{code}`. Locally, the edge nginx serves it at `courier.localhost`. It is
  plain TypeScript and Vite, like `tracking-page`.
- **Flow:**
  1. **Welcome / consent:** the partner's name, what is collected (location, only during deliveries and only
     while this page is open), why it is collected, and who sees it (the partner, and the customer during their
     own delivery). A **Start shift** button.
  2. Exchange the code, then remove it from the address bar (`history.replaceState`).
  3. Ask for location permission, with a help screen when it's denied.
  4. **Deliveries:** one card per delivery: the store, then the customer address and notes, the status, and one
     big next-step button (At store → Picked up → Delivered), with "Can't deliver" behind a smaller link.
  5. **End shift** behind a confirm step.
- **Wake lock:** the Screen Wake Lock API where available, re-acquired when the page becomes visible again. Where
  it isn't available, a "keep this screen on" notice is shown.
- **Status bar:** the GPS state, online/offline, "N updates waiting", and taps not yet sent.
- **Errors:** an expired or used link and an ended session each show a plain screen saying to ask the dispatcher
  for a new link.
- Strict CSP (`default-src 'self'`, `connect-src` = the API), `Referrer-Policy: no-referrer`, `noindex`, and
  `frame-ancestors 'none'` (Claude's call: the courier page is never embedded).
- **Copy:** in English and Dutch, with the M4 i18n pattern. The Dutch is drafted with "je" and reviewed by the
  owner, as in M4.
- **Look:** clean and friendly, with large touch targets (≥ 48 px), high contrast for daylight, and dark mode
  following the system. The partner's branding (logo, colour) arrives with M7. Until then the page shows DT's
  neutral look and the partner's name.

### 6.8 Local development and the owner's phone check

- Geolocation on iOS needs a secure context, so the plain LAN HTTP setup used in M4 doesn't work. The builder adds
  an HTTPS option for LAN testing (for example, a local CA with `mkcert`, or Vite's basic SSL with a trust step)
  and documents it.
- A `make courier-link` helper (or a `dtctl` command) creates a test delivery, a test courier and a link-mode
  session in the test environment, and prints both the courier link and the tracking link. The owner can then hold
  the courier page in one hand and watch the tracking page on a laptop.
- **Owner checkpoint** (like M4 Task 11): the owner walks or cycles a short loop with the courier page on the
  iPhone SE while the tracking page runs on a laptop. The owner judges the page's clarity, the copy and the battery
  drain over about 20 minutes.

### 6.9 Room for dispatch-lite (D26)

`GET /courier/deliveries` returns `{deliveries: [...]}`, an object rather than a bare array, so M10 can add
`offers: [...]`. Sessions already exist for the courier's whole shift, not per delivery, so an online courier
without work can later be offered a job. M5 adds nothing else.

### 6.10 Settled after the builder check (Claude's calls unless noted)

- **Next actions come from the server.**
  - Each delivery in `GET /courier/deliveries` (and in the `delivery` returned by an event report) carries
    `next_actions: [...]`, computed by deliveries from the M2 state machine for the courier actor, model and status.
    For example, `direct` may skip `at_pickup`, and `end_to_end` needs `courier_assigned` first.
  - The page shows the first action as the big button and `failed` as the small link.
  - Clients never hard-code the flow, so a later state-machine change needs no SDK release.
- **When GPS is collected.** The M3 purpose-limitation rule stands, and the owner approved it in the M3 design
  (D27): fixes count while the courier has a non-final assigned delivery. That includes a Direct courier assigned
  during prep. The partner gets that position for disputes, and the customer still sees it only from `picked_up`.
  The consent screen says exactly this: "while you have a delivery assigned".
- **Deliveries-side changes are allowed and small.**
  - A courier filter on `ListDeliveries`, or a dedicated `ListCourierDeliveries` RPC, returns only the §6.4 fields
    and `next_actions`.
  - `ReportEvent` already has `actor: courier` (used by the M3 simulator). The gateway sets it only on courier
    routes, and `by: courier` is accepted for `pickup_confirmed` when the actor is the courier.
  - The courier existence check for `POST /courier-sessions` uses the existing courier lookup (couriers are created
    with `PUT /couriers/{external_id}`, M2 C10).
- **When "one session per courier" replaces the old session.** It happens when the new session first becomes
  usable:
  - in sdk mode, at creation;
  - in link mode, when its code is exchanged.

  A link that is minted but never opened never ends a working shift. A courier can't have two pending links: a
  newer one cancels the older pending link.
- **API base and CORS.**
  - The courier page gets the API base at build time (the same mechanism as `tracking-page`), and its CSP
    `connect-src` lists exactly that origin.
  - The gateway owns CORS for `/courier/*`.
  - `POST /courier/token` has its own limits: 10/min per session for refresh, and 20/min per IP for link codes,
    with failed code guesses counted toward the IP limit.
- **SDK consent.** The partner is the controller (D27) and must inform its couriers. The SDK collects nothing
  until the app calls `start()`, and the guide tells partners to call it only after their own consent step. The
  SDK does not take a consent flag.
- **Signing keys.** The viewer token, its `kid`, its JWKS entry and the tracking verifier stay byte-compatible.
  Generalizing `pkg/viewerjwt` may edit M3 code only behind tests proving that existing viewer tokens still verify.

## 7. Testing

- **Domain and app unit tests:**
  - session lifecycle: start, replace, end, expire;
  - refresh rotation and reuse detection;
  - link code single use and expiry;
  - the deny-list;
  - the courier authorization rules (§6.4).
- **Authorization matrix:** every route against every credential type (§6.3).
- **Contract tests:** OpenAPI for the new routes, and the generated TypeScript client used by the SDK.
- **SDK unit tests:**
  - a fake clock and fake geolocation;
  - filtering, batching, the offline buffer and replay order;
  - token refresh;
  - stopping when there are no deliveries or the session ends.
- **e2e (Playwright):**
  1. Start a link session.
  2. Open the courier page with emulated geolocation moving along a route.
  3. Tap through the statuses.
  4. Meanwhile, a tracking page in a second browser context shows the courier, the status changes and the
     delivered celebration.
  5. Revoke the session mid-ride, and the courier page shows the ended screen.

  This runs on chromium and webkit-iphone.
- **Smoke:** a link session plus a few fixes through the API, without a browser.

## 8. Staging changes (written now, applied after the AWS resume)

- The courier page is hosted on `courier.staging.<domain>`, a hostname of its own, so its storage and CSP are
  separate from the tracking page. The builder picks the cheapest setup: the same CloudFront distribution with a
  second alias and an origin path, or a second distribution. The ACM certificate gets the extra name.
- The deploy role may write `courier/*`.
- Nothing is applied in M5. The measurements (latency from phone to viewer, battery) join the M4 staging check.

## 9. Order of work (Claude's call)

1. Contracts: OpenAPI routes, the JWT audience, and the events.
2. Gateway sessions and tokens.
3. Courier endpoints wired to tracking and deliveries.
4. `@dt/courier`.
5. The courier page.
6. The local HTTPS setup and `make courier-link`.
7. **Owner checkpoint** (§6.8).
8. e2e.
9. Staging infra.
10. Docs.

## 10. Left to the builder

- Table and index details, the Redis key names, and the JWT key rotation mechanics (follow the viewer key).
- The SDK's internal structure, retry and backoff numbers, and the IndexedDB schema.
- The courier page's layout and visual details, within §6.7.
- The local HTTPS mechanism.
- The fixed list of "can't deliver" reasons (keep it short, and in both languages).

## 11. Follow-ups this milestone closes or touches

- MVP §16 M5 (courier sessions, SDK and link).
- `PROJECT_CONTEXT` §7: courier-confirmed pickup unblocks the `courier*` policies.
- M3 follow-up 12 (pepper domain separation), for the new courier tokens only. The existing link tokens and API
  keys keep their current hashing until their own follow-up.
- M3 follow-up 6 (courier rows in tracking are never deleted) stays open; M5 adds no courier data.
