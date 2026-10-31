# M4 follow-ups and controller rulings

Written at the M4 close-out (Task 16, Step 3). It replaces the git-ignored build ledger as the committed record.
M4 is the 3D renderer (`@dt/renderer`), the viewer client (`@dt/viewer`), the SDK (`@dt/tracking`), the hosted
tracking page and the staging CDN. The design is `docs/superpowers/specs/2026-10-29-m4-renderer-design.md`. The
plan is `docs/superpowers/plans/2026-10-29-m4-renderer.md`; its own decisions **P1-P10** are at the end of that
file and are not repeated here.

**State when this was written:** the code is complete on branch `m4-renderer`. Still to come: the final
whole-branch review (it may fix some items below), `make ci` and `make e2e`, the pull request, and the staging gate.
The staging gate waits for the AWS setup (decision D28: paused for budget until next month).

## Measurements (pending staging)

Nothing is measured on a real device yet. Fill in the numbers when staging is up. The budgets come from design §7.
Leave a cell empty until it is measured.

Devices: the owner's iPhone SE (3rd generation), an iPhone 14 (or its Safari responsive mode) and desktop. Each in
day and in night mode. Use the same simulated Amsterdam delivery for all runs.

| What | Budget | iPhone SE day | iPhone SE night | iPhone 14 day | iPhone 14 night | Desktop day | Desktop night |
|---|---|---|---|---|---|---|---|
| First-load tile bytes | at most 2 MB | | | | | | |
| Initial render time (Lighthouse mobile) | at most 3 s on 4G | | | | | | |
| Initial render time (Safari Web Inspector on the phone) | at most 3 s on 4G | | | | | | |
| Frame rate (from the overlay) | 30 fps on phones, 60 fps on desktop | | | | | | |

Device checks the build flagged. Each needs a real device:

- **iPhone SE fps has never been measured.** The owner approved the look at the Task 11 checkpoint but gave no
  number. Before the PR the owner may do an interim check on the local stack over the LAN (R28).
- **Night fps.** The night city adds additive streetlight glow, which overdraws. It also adds 8-20% draw calls and
  12 bytes per vertex for the night colors. Measure night separately from day.
- **The x-ray `discard` shader.** The building and tree x-ray materials use `discard`. It can cost fps on
  mobile GPUs. If it does, fall back to the courier silhouette pass only.
- **Depth write on faded materials.** Faded courier x-ray materials keep `depthWrite`. Check on a device that the
  stale and lost fade looks right.
- **Android.** A mid-range Android profile (Lighthouse mobile throttling) is also checked, so cheaper phones are not
  left behind. It does not drive the art choices.
- **Gzipped JavaScript.** The SDK bundle was 225 KiB gzipped at Task 13. CI checks the size; record the final
  number here.

## Staging gate (pending)

M4 is not closed until the owner reviews it on staging (design §2 and §9).

1. **AWS setup and the owner steps.** Follow `docs/runbooks/aws-account-setup.md` (from step 2) and then
   [`docs/runbooks/tiles.md`](runbooks/tiles.md): apply the infrastructure, upload the tiles, deploy, and open a
   simulated delivery on a phone. Do not copy those steps here.
2. **Measurements.** Fill in the table above.
3. **Owner review.** On the iPhone SE, an iPhone 14 (or its responsive mode) and desktop, in day and night, 2D and
   3D: the hosted page and the SDK demo, both delivery models (`direct` and `end_to_end`), from `confirmed` to
   `delivered`. The owner agrees the look matches or beats the prototype. Record the approval here with the date.
4. **Dutch copy.** The Dutch strings in `web/packages/tracking/src/i18n.ts` were drafted by Claude (R20). They use
   "je", like Dutch delivery apps. The owner (or a native speaker) reviews them. Also review the wording of the
   `stale` and `lost` messages (see the Task 13 follow-up under the SDK).
5. **Any budget miss** is fixed before the review, or the owner accepts it in writing here.

Open decision for the owner at PR time: **does M4 merge before its staging gate?** Merging first means the gate runs
as a post-merge check, and any fixes become follow-up pull requests (R28).

Approval: _not given yet_.

## Do before the PR

- **Bump `happy-dom`** (a test dependency of `@dt/tracking`, 20.14.6) to 21.0.4 or later. `pnpm audit` reports a
  high advisory, so the web CI job fails until then.
- **gosec G705 at `idempotency.go:259`.** It is not from M4 (it is on `main` too), but it keeps `make ci` red.
- **Look baselines.** The Playwright look screenshots were generated on the build machine (R25). If the CI
  runner's fonts or GPU change them, regenerate them in the CI environment and commit.
- **Never ship a local web build.** `make web-assets` builds the SDK into `web/packages/tracking/dist` with
  localhost defaults. The staging deploy uses `scripts/web-staging-build.sh`, which deletes the output, builds with
  the staging values and refuses output that names localhost, the fixture or the test hook. The `mount.ts`
  localhost fallback now drops out of staging builds.

## M3 items closed by M4

These are marked done in `docs/m3-followups.md` too.

- **M3-R10 (dropoff label):** decided in design §6.11: the label stays null for the customer view. The page shows
  "Your address". The renderer never shows a dropoff label.
- **M3-R14 (OSRM `/match`):** the renderer snaps the courier to the route (within 25 m) and dead-reckons between
  fixes (R8). No matching on the ingest path.
- **M3 follow-up 5 (503 right after creation):** the gateway sends `Retry-After: 1` on the 503
  `tracking_unavailable`; the viewer client retries for up to 5 s and shows "Getting your delivery ready…".
- **`frame-ancestors`:** the gateway builds it per tenant from `embed_origins`; unknown tokens get `'none'`.

## Controller rulings

Decisions the build controller made while running the plan, numbered `M4-R<n>` so they do not collide with the
resolved questions in `docs/OPEN_QUESTIONS.md`, the `M3-R` rulings or the plan's decisions P1-P10. Each entry: the
decision, why, and what it costs if wrong. The build order was tasks 1-14, then 15, then 16.

- **M4-R1:** Task 5 extends `src/tiles/source.ts`, which Task 4 created (it adds `TileSource` and `openTileSource`),
  instead of creating it again. The plan says "Create" in both tasks; one file with one owner of the type is
  cleaner. If wrong: nothing (file layout only).
- **M4-R2:** Task 4's "no visual change" check is a before/after screenshot of the dev page in Playwright's
  Chromium, from an ad hoc script that is not committed. The plan names no tool and Playwright arrives only in
  Task 14. If wrong: a subtle visual regression slips to the owner's look checkpoint.
- **M4-R3:** the first run stopped at Task 11 for the owner. The plan makes that look checkpoint a gate ("only then
  start Task 12"). Tasks 15-16 were not run then (D28). If wrong: nothing; the owner resumed.
- **M4-R4:** "100 KB" for the logo means 102400 bytes, `display_name` length is counted in runes, and an
  all-empty Branding from the RPC means "no branding". The brief does not define these. If wrong: a size limit that
  is 2.4% off, trivial to change.
- **M4-R5:** the Task 3 compose file gets the Task 14 `TRACKING_PAGE_*` values now, including
  `CONNECT_EXTRA=https://tiles.openfreemap.org`. A malformed token counts as unknown (200, no branding), and an
  event with an equal or older version is ignored. The plan leaves these open. If wrong: small config or handler
  edits.
- **M4-R6:** the CSP names origins only, never paths. The plan's literal `connect-src <API> <Tiles> <CDN>` with a
  `/v1` path would block API calls, because a CSP path match is exact. The spec (§6.7) says "origins". `PageCSP`
  takes origins, and `main.go` reduces the API, tiles and CDN URLs to `scheme://host[:port]`. `TestPageCSPExact`
  keeps the plan's layout. If wrong: a `connect-src` that is wider than one path on the same origin; negligible.
- **M4-R7:** the 12 km tile-budget test uses the fixture's own measured mean zoom-14 tile size, and a synthetic
  route is allowed. `tiles-nl` is implemented but was not run until Task 15, and Planetiler runs with
  `--network host` (an environment setting). If wrong: the budget test is less representative of NL tiles at city
  edges.
- **M4-R8:** dead reckoning stops at the next route vertex the courier has not reached (the literal spec: "never
  past the next route point it has not reached"). The courier can stall on short segments until the next fix. The
  spec is explicit and conservative: the marker never goes ahead of reality. If wrong: slightly jerkier motion;
  change the cap to the end of the leg.
- **M4-R9:** Task 9 says "no real-time lights beyond the moon and the hemisphere/sun pair", and the test says "light
  count is the same by day and night". Both hold because the moon is the sun's `DirectionalLight`, recolored and
  moved at night. No light is added. If wrong: a separate moon light would need the test relaxed to day plus one.
- **M4-R10:** the Task 9 look screenshots live in `web/packages/renderer/docs/look/`, next to the day set from
  Task 6, made with the same script. If wrong: move the files.
- **M4-R11:** the "fall back when tiles fail for 10 s" rule keeps the same handle object that `createRenderer`
  returns. It swaps its insides to the 2D fallback and keeps the latest snapshot. If wrong: the SDK and the viewer
  would have to handle a renderer swap themselves.
- **M4-R12:** "on a phone" for adaptive quality means `(pointer: coarse)` or a viewport whose shorter side is under
  768 px. If wrong: tablets and desktops adapt too early or too late. (A short desktop window counts as a phone.)
- **M4-R13:** the context-loss test runs in Vitest with a mocked renderer (the brief allows either), and keeps the
  name `TestContextLossRebuildsScene`. If wrong: the real-GL rebuild path is unproven until the Task 14 browser
  tests.
- **M4-R14:** the 10 s tile-failure timer starts at the first snapshot, which refines R11. Tile loading cannot
  begin before a snapshot gives the corridor. If wrong: a stalled snapshot stream delays the fallback beyond 10 s,
  but nothing could be drawn anyway.
- **M4-R15:** the context-loss review item is in scope (design §4 anti-goal). If `webglcontextrestored` does not
  arrive within 3 s of `webglcontextlost`, the handle swaps to the 2D fallback and stays there. If wrong: a device
  that restores slowly lands in 2D instead of 3D.
- **M4-R16:** the Dutch copy table did not exist at Task 11 (customer copy arrives with the SDK and page). The
  owner reviews the Dutch strings when that table is built, not at the look checkpoint. If wrong: a second short
  owner look later.
- **M4-R17:** the viewer's endpoint paths follow the contract (`contracts/openapi/dt.v1.yaml` and
  `docs/guides/tracking-links.md`), not the plan's shorthand (`/stream/ws`). The contract is what the M3 gateway
  serves. If wrong: rename two path constants.
- **M4-R18:** SSE uses `fetch` streaming with `Authorization: Bearer`. The contract says "use fetch, not
  EventSource": `EventSource` cannot send headers, and the token must never be in a URL. The `EventSource?` option
  of `createViewer` is dropped, and the injected `fetch` serves SSE. If wrong: an `EventSource` path would need a
  URL token, which the contract forbids.
- **M4-R19:** the WebSocket is tried first on every visibility resume (the transport resets to `ws`). Inside one
  visible session, SSE stays once chosen. The spec says "SSE when the WebSocket fails" and nothing about retry. This
  recovers from a flaky load without hammering a blocked network. If wrong: customers stay on SSE until they leave
  and return to the tab.
- **M4-R20:** the implementer drafted the Dutch copy table: natural, friendly Dutch with "je", like Dutch delivery
  apps, matching the English copy. The owner reviews it at the staging gate (R16). If wrong: copy edits only.
- **M4-R21:** `setInsets` reframes inside the renderer, using the renderer's current framing for the state (follow
  stays on, delivered stays home-only), with rounded insets and a small change threshold. The SDK no longer calls
  `overview()`. The renderer owns framing. If wrong: reframe logic moves to the SDK.
- **M4-R22:** the spec's `lang en|nl` covers the 3D labels too. The renderer takes its label strings from the caller
  (the SDK passes `t(key, lang)`); the dev page keeps English. If wrong: an extra renderer option nobody needs.
- **M4-R23:** framing keeps the pin labels inside the visible map area (label extents count, not only the
  points). A half-cut "Your address" is a visible defect on the customer's main view. If wrong: a slightly wider
  overview framing.
- **M4-R24:** Task 14 deviations accepted: (1) a local `edge` nginx on :8080 mirrors the staging ALB routing
  (stream on tracking :8082), because the page cannot stream locally otherwise; (2) `TRUSTED_PROXIES` is the
  private ranges, in compose only, so e2e can vary `X-Forwarded-For` under the 10/min viewer limit; (3) the embed
  test origin is `https://localhost:5180`, because the validator requires https; (4) a TLS front on :8443 in
  `serve-demo.mjs`, for WebKit mixed content; (5) the gateway CSP `connect-src` adds the API `ws`/`wss` origin,
  which is a real bug fix; (6) desktop Chromium runs on ANGLE and the look screenshots on SwiftShader. If wrong:
  (2) would weaken rate limiting if it were copied to staging (staging config is untouched); the others are test
  plumbing.
- **M4-R25:** look baselines stay as generated locally. If the CI runner's fonts change them, regenerate inside the
  CI job's environment (or the Playwright container) and commit. The PR review is where baselines get checked
  anyway. If wrong: one red e2e run and a baseline refresh commit.
- **M4-R26:** the look tests answer the viewer API with fixed snapshots (plus a 2D shot per viewport), and the
  warming test fakes the first session call as a 503. Deterministic screenshots need fixed data, and the warming
  test still runs the real client retry path against the gateway's 503 shape. If wrong: look and warming are not
  proven against the live stack (the page spec covers the live states).
- **M4-R27:** Task 15 ran steps 1-3 and the offline `tofu test` plus `make ci`. Step 4 (staging up, tiles upload,
  deploy, live check) waits for the AWS resume (D28) and is folded into the owner steps in
  [`docs/runbooks/tiles.md`](runbooks/tiles.md). The code needs no AWS account; only the apply does. If wrong:
  infrastructure code that was never run against real AWS sits until next month.
- **M4-R28:** Task 16 Steps 1-2 (measurements and the owner's final review on staging) wait for the AWS resume
  (D28). Now: Step 3 (these docs), the final whole-branch review and its fix wave, then `make ci` and `make e2e`.
  Before the PR, the owner is offered an interim fps check of the iPhone SE on the local stack over the LAN. If
  wrong: M4 merges before its staging gate; the gate then runs as a post-merge check, with fixes as follow-up pull
  requests.

## Deferred minors, by area

Found in the per-task reviews and left for later. The final whole-branch review may fix some of them; strike
those off when it does. Items the reviews fixed in their own fix rounds are not listed.

### Contract and gateway

- OpenAPI `Branding.display_name` has no `maxLength: 60` (accounts enforces it). The proto comment "logo bytes
  stored as-is" does not say the limits. The unprefixed `oapi-codegen` constants `Auto`, `Dark` and `Light` in the
  gateway `api` package could collide.
- Embed origins: hosts are not lowercased and the duplicate check is case-sensitive; `:443` is accepted; there is
  no 253-character host cap. `frame-ancestors` still works (CSP host matching ignores case, and `:443` matches the
  default port). The negative tests miss an uppercase scheme, port 0, `:0443`, `[::1]` and `https://*.com`. The
  truncated-PNG test covers only a cut header (the decode is config-only by design).
- Outage oracle: when the settings store fails, a known token answers 503 and an unknown one 200. There is also a
  timing difference.
- `FrameAncestors` admits `*` and `https:` (it does not parse `scheme://host`), and the consumer does not
  re-validate `display_name`, color or theme. A branding store error silently becomes `null` (no Warn log). The
  default `TRACKING_PAGE_BASE` `/page/` points at nothing. CDN, tiles and API values are not checked for `;` or
  quotes. `TRACKING_PAGE_CONNECT_EXTRA` is not reduced to origins.
- No lag monitor for `gateway-tenants`. `/logos` is not rate limited. The page and the session call share the
  10/min viewer IP bucket (two calls per view), which could hurt customers behind NAT or CGNAT. An empty Branding
  differs between Postgres and the in-memory store.
- `RedactPath` uses a plain prefix check, so non-canonical paths (`//t/`, `/./t/`, `/x/../t/`) are not redacted.
  They are client-made, and the mux redirects them. The `otelhttp` metric attributes are computed on the restored
  URL (unverified; v0.72 should not include the path).

### Renderer

- **Tiles and fixture:** `fixture.ts` scans a hard-coded tile window. Buildings that cross tile borders are drawn
  once per tile. `featureSeed` truncates with `|0`. Bridges are not raised and rail is omitted. The proof result
  JSON is not saved to a file.
- **Scene and motion:** `CityScene.updateRoutes` draws `to_dropoff` as active at `at_pickup` while `activeLeg()`
  returns `to_pickup` (prototype behavior; align them). `SnapshotGate` resets on a `delivery_id` change
  (undocumented). `CityScene.dispose` leaves the city-group geometries and materials. The x-ray ribbon's cloned
  materials are not disposed.
- **Per-frame allocations and GPU buffers:** small allocations per frame (motion `cur`, `deadReckon`,
  `rebuildRibbons`), which also allocates during zoom glides. `fillRibbon` growth leaks old GL buffers until
  dispose. `setNight` allocates two `Color`s per fade step and `mixObjects` traverses the scene each step.
- **Behavior:** `setInsets` applies only at the next frame-all on resize (the view-framing path from R21 covers the
  SDK). Wheels spin while dead reckoning is stalled. `yawSet` is not reset. The courier x-ray copy does not fade
  on stale or lost, and the faded materials keep `depthWrite`. A live to stale change stops mid-glide. The delivered
  framing passes `homeOnly` only when `pos === null`: a courier still in the snapshot makes it frame store, courier
  and home (pass `status === 'delivered'` in all branches). The 2D mode keeps the compass heading while the
  fallback is north-up. Pan is on when the view is not following, but spec §5.1 says "no pan" (carried from the
  prototype; confirm in the final review). `onFrameStats` reports near-zero fps after a background tab. No warm-up
  gate before adaptive quality. The cutaway and x-ray pass go beyond the brief (kept; their fps cost is a device
  check above).
- **Night and theme:** theme is inferred from the hex value (`COLOR_KEYS`/`GLOW_KEYS`); explicit `dtColor`/`dtGlow`
  tags would be better. The shared `mat()` cache is recolored globally, so two renderers with different themes
  fight. `resolveTheme` splits `createRenderer`'s doc comment. `auto` is resolved once, but the doc says "follows"
  (add a listener or reword). `currentTheme` is unused. `disposeCity` disposes the shared `litBase` map. The
  streetlight glow `renderOrder` 1 equals `routeNext`, so additive pools may brighten the next-leg ribbon.
- **Lifecycle and context:** no `forceContextLoss()` before dispose on a fallback swap or destroy (the context
  lives until GC; browsers cap at about 8-16). A `CityScene` constructor throw after `appendChild` leaks the canvas
  and listeners. The restore handler runs `rebuildGpu()` before `onContext(false)` (a throwing rebuild still falls
  back at 3 s, which is arguably right). `glowTexture` has an unmarked no-canvas branch.
- **Tests:** no test for the `updateRoutes`/`activeLeg` link beyond the Task 6 fix, for the 5 s tile retry gate,
  for `applyHidden`, or for follow-mode pan. The camera and reduced-motion glue (intro skip, snap versus glide,
  `finishCameraMove` on toggle) is untested, although it is the §5.3 headline; extracting the camera moves from
  `CityScene` (+290 lines) is a candidate. The reduced-motion test skips `dropParcel`, `parcelLift`, `ringPhase`
  and `haloOpacity`; `isEnabled` ignores its argument, so the `EFFECTS` list overstates the granularity. The
  vehicle animate test only checks that the pose differs. The theme wiring is untested (vehicle swap at night,
  tiles built after a switch, a reversed mid-fade, `setReducedMotion` finishing a fade, night attributes reaching a
  mesh). The context-loss test does not assert that lit windows and streetlights survive, and the
  destroy-clears-timer test passed before its fix (it is only a guard).
- **Code shape:** the reduced-motion `matchMedia` default is duplicated (`index.ts` and a dead fallback in the
  `CityScene` constructor). The dev page's reduced-motion toggle cannot return to "system".

### Viewer

- The secrecy test cannot fail (constant error strings; 500 path only). There is no half-open socket watchdog
  (the server pings every 25 s). Some lines in `stream.test.ts` are long and unformatted. The visibility listener
  stays until `stop()` after the link becomes inactive. The transport resets to `ws` before the resume awaits
  (consistent with R19). The retry-on-return test (R19) does not assert the second SSE fallback.

### SDK and page

- A broken partner logo is requested again on every snapshot. `onDelivered` and `onStatusChange` do not fire when
  the first final snapshot arrives as `inactive.last`. Removing the `theme` attribute forces `auto` over the
  branding theme. The `three` package and the renderer `./framing` export exist only for a test. The `mode` option
  of `mount` is not validated.
- Follow mode does not recenter on the free area after an inset change (the SDK never turns follow on). Label
  padding uses the widest label on both sides. `framedInView` is approximate in the middle of a glide.
- Copy: the wording for `stale` and `lost` ("nothing else" reading) is for the owner to judge at the staging gate.

### Page and end-to-end tests

- The embed-blocked test waits a fixed 5 s (assert the front answers 200 and the `frame-ancestors` header first).
  `look.spec` uses `networkidle` plus a 3 s sleep. In `page.spec`, "sent is a subset of shown" is vacuous on the SSE
  fallback (assert `streamed > 0`).
- The CDN `nginx.conf` has duplicated CORS blocks. Its immutable `Cache-Control` uses `always`, which caches 404s.
  `types {}` replaces `mime.types`. `serve-demo` answers 502 after the headers are sent, and its `::1` listener has
  no error handler.
- The e2e CI job has no `timeout-minutes`, and the Playwright cache is keyed on the whole lockfile.

### Infrastructure and deploy

- The OAC bucket policy is not asserted by the tofu test (the mock JSON is `{}`).
- Runbook: a deploy between the merge and step 1 fails at the upload (the hint points the wrong way). The step 1
  plan text should say the task definitions change (`-/+`) and only the `track` validation record is added. The
  register step's error is misleading when `describe-task-definition` fails. "Install scripts never see AWS
  credentials" is overstated.
- `CDN_URL` is duplicated with the tofu `cdn_hostname`. The build guard does not check the tiles URL.
- Never verified against real AWS: the CORS preflight, bucket versioning disabled, an ALB certificate
  replacement, the managed policy IDs. The deploy role policy is attached by role name, so bootstrap phase B must
  run first.
