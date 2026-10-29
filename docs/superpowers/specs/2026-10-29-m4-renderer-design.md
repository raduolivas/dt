# M4: real-map renderer, hosted tracking page and `@dt/tracking` SDK (design)

Status: draft for owner review, 2026-10-29. Inputs: MVP spec `2026-10-18-dt-mvp-design.md` §4.5, §6.1, §6.2, §16 (M4);
M3 design `2026-10-26-m3-tracking-design.md` §3; `docs/m3-followups.md`; the prototype in `web/packages/renderer`.

Decisions are marked **(owner)** when the owner decided them and **(Claude's call)** when the owner asked Claude to
follow its suggestions (2026-10-29: "the rest follow your suggestions").

## 1. Intent

M4 is the part of DT a customer actually sees. M0–M3 built the API, deliveries and live tracking. M4 puts a real
delivery in Amsterdam on screen in the style of the prototype: a cute, toy-like, videogame 3D city, now built from
real OpenStreetMap streets and buildings instead of the prototype's procedural grid.

This is the product's differentiator (D27: "DT competes on the customer 3D experience"). If it looks like a generic
map with a dot, M4 has failed even when every test passes.

**Look and feel (owner):** "exactly like the prototype or better. As close to a game and a cute 3D atmosphere as it
can be." The prototype is the floor, not the ceiling.

## 2. Goals

1. **`@dt/renderer`**: a library that draws a `TrackingSnapshot` (the generated v1 contract) on a real map, in 3D
   (isometric orbit, as the prototype) and 2D (top-down camera on the same scene).
2. **Hosted tracking page** at `https://track.<domain>/t/<link_token>` (staging:
   `track.staging.deliveryontrack.io`): opens the viewer session, streams snapshots, shows the map plus DOM status,
   ETA and courier cards. It works as a redirect, a link, a webview and an iframe.
3. **`@dt/tracking` SDK**: `mount(el, options)` and a `<dt-tracking>` web component that render the same experience
   inside a partner's page.
4. **Both delivery models' views correct** (MVP §4.5), including the preparing view, all four `signal` states
   (M3 §3.3) and the ETA states.
5. **Running on staging**, so the owner can open a simulated Amsterdam delivery on a phone.

**Done when (MVP §16, sharpened):**
- A simulated sandbox delivery in Amsterdam renders on staging in 2D and 3D, through the hosted page and through the
  SDK on a test page, from `confirmed` to `delivered`, for both `direct` and `end_to_end`.
- The owner reviews it on a phone and a desktop and agrees it matches or beats the prototype's look. **This review
  is a gate** for closing M4, not a formality.
- The budgets in §7 are met, measured, and the numbers written down.

## 3. Non-goals

- The console, uploading branding through a UI, tenant self-service for embedding origins (M7).
- Courier SDK, courier web link, courier sessions (M5). Webhooks (M6).
- Publishing `@dt/tracking` to npm (M8, with the public docs). The npm scope is not decided yet. **(Claude's call)**
- Traffic-aware ETA, turn-by-turn, route optimization.
- Map areas outside the Netherlands. Countries beyond NL come later with their own tile extracts.
- Native mobile SDKs.

## 4. Anti-goals (would make M4 a failure even if it technically works)

- It looks like Google Maps or a Mapbox style with a pin: flat, grey, realistic. Real geometry, toy rendering.
- The courier jumps, teleports or drifts off the road when fixes arrive.
- The customer gets alarmed: no error states, red banners or "signal lost" wording reach the customer (M3 §3.3: lost
  looks like stale).
- Hidden data shown: the page never derives or shows anything the server did not send (visibility is server-side).
- Slow first paint on a phone: a blank screen for more than a moment.
- Breaking embeds: anything that needs cookies or URL credentials after the session exchange.

## 5. Look and feel

### 5.1 Keep from the prototype (the floor)
Everything that gives the prototype its character carries over unchanged unless something below improves it:
- **Camera:** orthographic, isometric angle (`CAM_OFFSET` 300/280/300), orbit with damping, limited polar angle, no
  pan, follow mode with smooth camera goals, zoom range 0.6–5.
- **Light:** hemisphere plus a warm directional sun with soft PCF shadows.
- **Palette:** `COLORS` in `view/models.ts` (pale blue-white ground and blocks, blue accents, coral, green, leafy
  trees). Buildings use the window texture and flat-shaded low-poly sides.
- **Models:** the store (with its sign), the house, pins, trees, the animated bicycle and rider, the courier halo,
  the destination rings, the parcel drop on delivery, the road-following route ribbon (active and next leg).
- **Toy scale:** the courier is drawn larger than life (`BIKE_SCALE`), as in the prototype.
- **HUD style:** the prototype's cards in `style.css` are the visual starting point for the DOM cards.

### 5.2 Real map, toy rendering
- **Roads** (`transportation`): flat ribbons in the prototype's road color with the light road marks, width by class.
  Footways and paths are thinner and lighter. Bridges sit slightly raised.
- **Buildings** (`building`): extruded footprints with `render_height` (default 9 m), capped so the city stays toy
  sized (Claude's call: a soft cap, so tall buildings stay readable and do not hide the route). Roofs are flat with
  a small lighter edge; walls use the window texture. Small variation in tint per building (seeded by the building
  id, so it is stable between loads).
- **Blocks/ground:** the prototype's raised pale blocks between streets. Parks are green planes with instanced
  low-poly trees (seeded, stable). Water is a soft blue plane with a gentle shimmer.
- **Store and home:** the building footprint containing the pickup point gets the prototype's store treatment
  (sign with the pickup `name`, accent color); the dropoff building gets the house treatment. If no footprint
  contains the point, the prototype's standalone store/house model is placed at the point.

### 5.3 "Better than the prototype" (game feel)
The builder should push the game atmosphere with small, cheap touches, within the budgets of §7:
- Idle life: trees sway, the store's "preparing" loop (MVP §6.2: a looping animation on the store, for example
  steam, a bouncing bag, a spinning sign), a soft pulse on the home pin.
- Vehicles: the prototype only has a bicycle. M4 adds `e_bike`, `scooter`, `car` and an `on_foot` walker in the same
  style, chosen by `courier.vehicle`.
- Moments: a little bounce when the courier arrives at the store and at the door, the parcel drop and a short
  celebratory effect on `delivered`.
- Camera moments: a gentle intro move when the page opens, a smooth frame of store, courier and home when the state
  changes.
- Everything respects `prefers-reduced-motion` (no camera moves, no bounces, no shimmer; the courier still moves).

Which touches make it in is the builder's call; the owner's review (§2) judges the result.

### 5.4 Night city (owner, 2026-10-29: "I like to have that strong game touch from the beginning")
Dark mode is a full **night city**, not only dark cards:
- A night palette (deep blue ground and blocks, darker roads with soft lit markings), moonlight instead of the sun,
  and warm glowing windows (a lit-window variant of the window texture, a seeded share of windows lit).
- Streetlights along the roads with warm pools of light, the store's sign and the home pin glowing, and the courier
  carrying a small headlight and a glow under the halo.
- The route ribbon and pins stay clearly readable at night (AA contrast for the DOM cards still applies).
- `theme: auto` follows the system setting; `light` and `dark` force a mode. Switching fades smoothly between day and
  night (instant under reduced motion).
- Light pools and glows must be cheap (baked or emissive tricks, no light per streetlight) so the night city meets
  the same budgets as the day city (§7) on the reference phones.
- The night city is part of the look checkpoint (§9) and the final owner review (§2).

## 6. How it works

### 6.1 Packages (web workspace)
| Package | Contents |
|---|---|
| `@dt/contract` (exists) | generated v1 types (`openapi.d.ts`) |
| `@dt/renderer` (exists, becomes a library) | scene, models, tile loading and geometry worker, camera, interpolation; consumes the generated `TrackingSnapshot` only. The prototype simulator becomes a dev-only page in this package, translated to the v1 contract, so the look can be worked on without a backend. |
| `@dt/viewer` (new, Claude's call) | the viewer client: session exchange, refresh, WebSocket with SSE fallback, snapshot versions, reconnect and resync, end states. Shared by the page and the SDK. |
| `@dt/tracking` (new) | the SDK: `mount()`, `<dt-tracking>` (Shadow DOM), events, the DOM cards. |
| `tracking-page` (new app) | the hosted page, a thin shell around `@dt/tracking`. |

The prototype's own `TrackingSnapshot`/`TrackingSource` types are removed; the renderer reads the generated v1 type
(PROJECT_CONTEXT §"prototype contract").

### 6.2 Map data
- **Schema:** OpenMapTiles (MVP §6.2).
- **Development:** OpenFreeMap public tiles.
- **Staging (Claude's call):** a Netherlands extract built with **Planetiler** into one **PMTiles** file, uploaded to
  S3 and served through CloudFront with range requests, read with the `pmtiles` library. A `make tiles` target
  builds it locally from a Geofabrik NL extract; rebuilding is manual (monthly at most). Expected cost: under €2/month.
- **Attribution:** "© OpenStreetMap contributors" (and OpenMapTiles) is always visible on the map, also in the SDK.
- **Loading (Claude's call, refines MVP §6.2):** the first load covers a corridor along the route legs plus the
  store and home surroundings, inside the 2 MB budget. The rest of the route bounding box plus 300 m loads later and
  lazily (as the camera or the courier gets there), at lower detail away from the route if needed. The 2 MB budget
  applies to the first load, so it never limits route length. The builder picks the zoom (14 has all buildings in
  OpenMapTiles and keeps the tile count low). MVT decoding and
  geometry building run in a **Web Worker**.
- **Origin:** the existing local equirectangular `GeoProjection` with the pickup point as origin.

### 6.3 Courier motion (M3 handoff)
- **Interpolation:** position and heading glide between snapshots (prototype behavior).
- **Dead reckoning** while `signal` is `live`: between fixes the courier keeps moving along the route at the last
  speed, never past the next route point it has not reached, and stops if the gap grows.
- **Snapping (M3-R14, Claude's call: renderer side):** the drawn position is snapped onto the active route leg when
  the fix is within about 25 m of it; further away it is drawn at the raw position and the route is trusted again
  once the courier is back on it. Server-side `/match` stays out of the hot path.
- **Signal states (M3 §3.3, approved):** `stale` and `lost` → the courier stops at the last point, slightly faded,
  with "Updating courier location…", and the ETA is labeled as estimated; when fixes return the courier glides to
  the new position. `pending` → the courier card says "Locating courier…" and no marker is drawn.

### 6.4 Views per state (MVP §4.5; the server already filters)
| Snapshot | View |
|---|---|
| `confirmed` (both models), `at_pickup` in Direct | **Preparing view:** store and home, preparing animation, ETA card, no courier |
| `courier` present | courier marker, route ribbon from the route legs present in the snapshot |
| final states | courier hidden at once, final summary card (delivered, failed, cancelled) |

The page draws what the snapshot contains. It does not compute visibility itself.

### 6.5 ETA display
- `eta_state` and `eta_window` drive the ETA card, following the Uber Eats style approved in D27: a window ("12:40–
  12:50") before pickup, minutes after pickup, "Calculating delivery time…" when the state says so.
- `eta_quality: "estimated"` adds "estimated" next to the time.

### 6.6 Viewer client (`@dt/viewer`)
- `POST /tracking/session` with the link token from the URL path, then the WebSocket (auth as the first message);
  SSE when the WebSocket fails; `GET /tracking/snapshot` to resync after a reconnect.
- Ignores snapshots whose `version` is not greater than the last applied (contract rule).
- Refreshes the viewer token before its 15 minutes run out and swaps it on the open WebSocket.
- Reconnects with backoff and jitter; 1012 → reconnect and resync.
- **End states:** `revoked` or `link_invalid` → a friendly "This tracking link is no longer active" card; link
  expired after a final state → the final summary if a snapshot was seen, else the same friendly card. No technical
  error text reaches the customer; details go to the SDK's `error` event and the console.
- **First seconds after creation (M3 follow-up 5):** the gateway adds `Retry-After: 1` to the 503
  `tracking_unavailable` on the session exchange, and the client retries a few times within about 5 s before showing
  a "Getting your delivery ready…" state. This is a small gateway change inside M4.

### 6.7 Hosted page and embedding
- **Who serves what (Claude's call):**
  - The **gateway** answers `GET /t/{token}` on `track.<domain>` with a small HTML shell. It looks up the link to
    find the tenant and sets the response headers, above all a strict CSP with `frame-ancestors`.
  - The page's JavaScript, CSS, 3D assets and the tiles come from **S3 + CloudFront** at `cdn.<domain>` (staging:
    `cdn.staging.deliveryontrack.io`) with hashed, immutable file names, so a deploy never breaks an open page.
  - Why not CloudFront alone: `frame-ancestors` is per tenant, so the response that carries it has to know the
    tenant; a static file cannot.
- **Embedding origins:** a new tenant setting `embed_origins` (a list of `https://` origins, set with `dtctl tenant
  settings`, validated: no wildcards except a leading `*.` subdomain wildcard, no paths). Known link →
  `frame-ancestors` = that list (or `'none'` when the list is empty); unknown token → `'none'`, and the shell shows the
  friendly inactive card without revealing whether the token ever existed.
- **Other headers:** CSP `script-src` and `style-src` limited to the CDN origin, `connect-src` to the API, tile and
  CDN origins, `Referrer-Policy: no-referrer` (the token is in the URL path), `Cache-Control: no-store` on the shell,
  `X-Content-Type-Options: nosniff`.
- **SDK origins:** `embed_origins` limits iframes only. The SDK works on any origin with a valid link token,
  because the token is the bearer credential and the viewer endpoints allow every origin (M3 §3.2). This is
  intended.
- **Token in the URL:** after the session exchange the page does not need the link token again (M3 §3.2). It does not
  log the token or send it to any third party.

### 6.8 SDK (`@dt/tracking`)
- API as in MVP §6.1: `mount(el, { token, mode, theme, onStatusChange, onDelivered, … })`, `setMode()`,
  `destroy()`, and `<dt-tracking token mode theme>`; events `ready`, `statuschange`, `etachange`, `delivered`,
  `error`.
- Renders inside a Shadow DOM, so the partner's CSS cannot break it and DT's CSS does not leak out.
- **Distribution in M4:** an ES module and a single-file script bundle built in the repo and served from
  `cdn.staging…/sdk/tracking/<version>/`. A test page on staging shows it embedded. npm publishing is M8.
- `destroy()` releases WebGL, the worker, the stream and all listeners (tested: mount and destroy many times without
  leaks).

### 6.9 Branding (owner: follow Claude's suggestion)
- A **default DT theme**: the prototype palette by day and the night city (§5.4) in dark mode, `theme: auto`
  follows the system.
- **Branding set by hand until the console exists (M7):** a tenant setting `branding {display_name, primary_color,
  theme, logo}` set with `dtctl tenant settings`. `dtctl` uploads the logo (PNG or WebP only, at most 100 KB, at most
  512 px, no SVG) to the CDN bucket under the tenant's path. The session response's `branding` field (null since M3)
  returns it.
- The primary color tints the DOM cards and accents (route ribbon, pins); the 3D city palettes (day and night) stay
  DT's so every
  partner's map keeps the game look. Text contrast stays AA: if the partner color fails AA against the card, the
  card uses a darker shade of it.

- **Contract:** `branding` widens from null-only to an object (nullable). It is additive; if the breaking-change
  check objects, the builder adjusts the check, not the meaning.

### 6.10 Courier photo (Claude's call)
- M4 does not show the partner's `photo_url`. The courier card shows the display name and the vehicle model.
- Why: loading it would either open the CSP to any `https:` host and send the viewer's IP to the partner's host, or
  need a copy step onto the CDN. It can come back with the console (M7) as a copied, size-limited image.

### 6.11 Dropoff label (M3-R10, Claude's call)
- The label stays null for the customer view. The page says "Your address".
- Why: anyone holding the link sees the page, labels are free text partners may fill with personal details, and the
  label is sealed for that reason (M2 C7). Showing it would need deliveries to unseal it on every session exchange.

### 6.12 Accessibility and fallback
- Status timeline, ETA and courier info are real DOM text, AA contrast, keyboard focus on the 2D/3D toggle.
- No WebGL → status card plus a static route line on a 2D canvas (MVP §6.2).
- Tiles CDN down → retry, then the fallback view after 10 s (MVP §8).
- Languages (Claude's call): English and Dutch, picked from the browser language, with the strings in one table.

## 7. Reference devices and budgets (MVP §6.2)
**Reference devices (owner, 2026-10-29):**
- **iPhone SE (3rd generation):** the owner's phone, the smallest screen (4.7", 375 × 667 CSS px) and the floor
  for layout. Everything must fit and read well here.
- **iPhone 14:** the regular bigger phone (6.1", 390 × 844 CSS px).
- **Desktop browsers:** current Chrome, Safari and Firefox, at laptop and large-monitor sizes.
- Both iPhones have the A15 chip, so performance is tuned on iOS Safari (and in-app webviews, which use the same
  engine). Claude's call: a mid-range Android profile (Lighthouse mobile throttling) is checked too, so cheaper
  phones are not left behind, but it does not drive the art choices.
- **Responsive layout:** the cards sit as a bottom sheet over the map on phones (portrait first, landscape works),
  and as a side panel on wide screens. The map keeps the store, courier and home in view at every size.

**Budgets:**
- Initial render ≤ 3 s on the reference phones over 4G, in day and night mode.
- Tile payload ≤ 2 MB per delivery (first load, §6.2).
- ≥ 30 fps on the reference phones, 60 fps on desktop, with the geometry built in the worker.
- Gzipped JavaScript for the page: a target the builder sets and the CI checks, so it cannot grow silently.

## 8. Staging changes
- New OpenTofu in `infra/aws/staging`: S3 buckets (web assets and SDK, tiles, tenant logos; private, CloudFront
  origin access only), one CloudFront distribution for `cdn.staging.deliveryontrack.io` with an ACM certificate in
  `us-east-1` (the SCP already allows ACM there), Route 53 records for `track.staging` (to the ALB, routed to the
  gateway) and `cdn.staging`.
- The deploy pipeline builds the web packages and uploads hashed files before rolling the gateway, so a new shell
  never points at missing files.
- Cost estimate: a few euros a month on top of the ≈ €100 staging budget (CloudFront's free tier covers team
  traffic).
- This depends on the M3 staging apply. The owner runs that before M4 starts (owner, 2026-10-28: "before we start
  implementing M4 you guide me through AWS staging setup").

## 9. Order of work (Claude's call)
- The AWS staging apply for M3 happens before M4 implementation starts (owner, 2026-10-28).
- M4 is built local-first. **Look checkpoint:** the renderer on real Amsterdam tiles, driven by the dev simulator
  page, goes to the owner for a look review before the viewer client, SDK and infra work. Rework of the look lands
  there, not at the end.
- Staging (§8) is the last phase, and the final owner review (§2) happens there.

## 10. Testing
- **Vitest:** tile decoding, projection, mesh building, snapping and dead reckoning, ETA formatting, viewer client
  state (versions, refresh, reconnect, end states), branding contrast.
- **Playwright:** device profiles for iPhone SE (3rd gen), iPhone 14 and desktop (WebKit and Chromium), day and
  night. The hosted page against the local stack with the simulator, for both models through every state;
  the SDK test page; screenshot comparisons in 2D and 3D on a fixed tile fixture (so tiles do not make the tests
  flaky), plus the no-WebGL fallback; an iframe test that checks `frame-ancestors` allows and blocks the right
  origins.
- **Gateway (Go):** shell route headers per tenant, unknown token, `embed_origins` and `branding` validation,
  `Retry-After` on the 503.
- **Budget checks:** bundle size in CI; tile payload, first render and frame rate measured on staging on the
  owner's iPhone SE and on desktop, and written down.

## 11. Left to the builder
- Exact zoom levels, building height cap, tree density, which game-feel touches from §5.3, model detail.
- Worker protocol, caching of decoded tiles, how geometry is merged for performance.
- The structure of `@dt/viewer` and the backoff numbers.
- The bundle size target.
- The Planetiler profile settings, within the OpenMapTiles schema.

## 12. M3 follow-ups this milestone closes
M3-R10 (dropoff label decided, §6.10), M3-R14 (renderer-side snapping, §6.3), M3 follow-up 5 (`Retry-After`,
§6.6), and the `frame-ancestors` item from M3 §3.2. The other "before real traffic" items in `docs/m3-followups.md`
stay with the AWS staging work and their own deadlines.
