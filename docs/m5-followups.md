# M5 follow-ups and controller rulings

Written at the M5 close-out (Task 14). It is the committed record of the rulings made during the build. M5 is courier
sessions (partner API and gateway), the `@dt/courier` SDK and the courier web link page. The design is
`docs/superpowers/specs/2026-10-31-m5-courier-design.md`. The plan is `docs/superpowers/plans/2026-10-31-m5-courier.md`.
The partner-facing guide is `docs/guides/courier.md`.

**State when this was written:** the code is complete on branch `m5-courier` and is not pushed. Still to come: the
owner phone checkpoint (Task 11, below), the pull request, and the staging check after the AWS resume (D28).

## Pending

### Owner phone checkpoint (Task 11, postponed)

The owner postponed this on 2026-11-01. It runs after Task 14 and before the PR, from the owner's own location, with
`docs/runbooks/courier-phone-test.md` (iPhone SE over the LAN). **Not done.** To record when it runs:

- Courier page clarity: is the consent screen, the shift screen and the end screen clear on the iPhone SE?
- English and Dutch copy: the Dutch strings in `web/apps/courier-page/src/i18n.ts` are a draft by Claude (they use
  "je"). The owner, or a native speaker, reviews both languages.
- Battery: the battery use over a shift-length run on the iPhone SE. Figure: _not measured yet_.
- Page changes the owner asks for land after the e2e specs and docs, so Task 12 specs and this guide may need a small
  rework.

Approval: _not given yet_.

### Staging checks after the AWS resume

Needs the AWS apply (D28) and the courier hosting below. Do not copy the apply steps here; they are in
`docs/runbooks/staging.md`.

| What | Budget | Result |
|---|---|---|
| Phone to viewer latency on the courier path (the courier page posts a fix, the customer tracking page shows it) | p95 under 1000 ms, as for the relay | |
| Battery on the iPhone SE over a long run (about 1 hour) with the screen on | to record, no budget set | |
| Courier page loads from `courier.staging.deliveryontrack.io` under its CSP (no console errors) | none | |

### Test status

- The local `look.spec` screenshot failure (`iphone-se-day-preparing`, about 9% diff) is **pre-existing**: it also
  fails with the M5 changes stashed. The look baselines are generated on the CI runner (M4-R32), and fonts differ on
  this machine. Full-suite green is judged on the PR's CI run (M5-R12).
- A possibly flaky gateway HTTP test under load was seen once during Task 7 and not reproduced.

## Controller rulings

Decisions the build controller made while running the plan, numbered `M5-R<n>` so they do not collide with the
resolved questions in `docs/OPEN_QUESTIONS.md`, the `M3-R` and `M4-R` rulings or the plan's own decisions. Each entry:
the decision, why, and what it costs if wrong.

- **M5-R1:** Task 3 implements the `GetCourier` RPC server side (Task 1 added the RPC). The gateway needs a courier
  existence check by `cou_` id, and no task would have implemented the server. If wrong: a small extra RPC where an
  existing lookup could have served.
- **M5-R2:** the session service exposes `End(ctx, p Principal, id, reason)` for partners and `EndSession(ctx, sid,
  reason)` for the courier and system paths. The plan's "Principal or sid" union is not Go. If wrong: a rename.
- **M5-R3:** Task 7 records audit rows (through the app audit port) for a courier-initiated session end and for
  `refresh_reuse` ends. Spec §6.1 says start, end and revoke are audited, and those two paths bypass the partner audit
  hook. If wrong: a few extra audit rows.
- **M5-R4:** a courier access token's `exp` is capped at the lower of now plus 15 minutes and the session's
  `expires_at`, by extending `pkg/courierjwt` with a not-after bound. The spec §4 anti-goal (no access after the
  session ends) outranks "tokens simply run out". If wrong: nothing worse than the SDK refreshing slightly earlier
  near the end of a shift.
- **M5-R5:** audit rows written by the app layer use `Status 0`, meaning "no HTTP response" (documented on the
  field). Those rows have no client status. If wrong: a schema comment change if a convention is later required.
- **M5-R6:** `GET /courier/deliveries` and `POST /courier/session/end` share a per-session bucket
  `rl:courier:read:<sid>` of 30 per minute (the SDK polls every 10 s, so 6 per minute). Unbounded authenticated
  routes contradict the contract's 429 and invite abuse. If wrong: an abusive client gets a 429; a normal one never
  reaches it.
- **M5-R7:** ended and revoked sessions answer `401 unauthenticated` on the bearer routes; `session_ended` is used
  only on `POST /courier/token`. The authenticator cannot tell the cases apart without leaking session state, and the
  SDK treats any 401 as the end. If wrong: a less specific error code for the partner.
- **M5-R8:** `start()` asks for location permission once, with a single `getCurrentPosition` whose fix is thrown away
  (never buffered or sent), even when no delivery is assigned. Spec §6.7 asks right after Start shift, and a
  permission prompt in the middle of a delivery is worse. No position leaves the device. If wrong: one extra GPS wake
  at the start of a shift.
- **M5-R9:** the SDK notice union is widened with `{ kind: 'rejected', deliveryId, type }` for a refused tap that has
  no waiter (for example one restored after a reload). Spec §4 anti-goal: a tap lost without the courier knowing. If
  wrong: one more notice kind for the page to handle.
- **M5-R10:** a buffer left by an older shift is cleared when the stored origin changes. Sending pre-start fixes under
  a new session would break "no GPS before `start()`". If wrong: an older shift's unsent taps are lost (that session
  is over anyway).
- **M5-R11:** the consent screen says "your dispatcher" instead of the partner's name (spec §6.7). No endpoint before
  the exchange exposes the tenant name, and a public lookup by link code would widen the unauthenticated surface.
  Branding arrives in M7. If wrong: a less personal consent screen until M7.
- **M5-R12:** the local `look.spec` screenshot failure does not block Task 12 (see Test status above). Look baselines
  are generated on the GitHub runner (M4-R32), and the test also fails with Task 12 stashed. Full-suite green is
  judged on the PR's CI run. If wrong: a real look regression could hide until CI.
- **M5-R13:** the courier page is served from `courier/current/` (a copy) instead of `courier/<sha>/` as the plan's
  Interfaces line says. CloudFront cannot follow a sha, and the deploy role cannot edit the distribution.
  `courier/<sha>/` stays as an archive. If wrong: nothing; rollback is a copy, not a pointer flip.
- **M5-R14:** the owner phone checkpoint (Task 11) runs after Task 14 and before the PR, or on staging; the owner's
  call (2026-11-01: "Skip, do Tasks 12-14"). If wrong: page changes the owner asks for land after the e2e specs and
  docs, and may need a small rework of Task 12 and this documentation.
- **M5-R15:** courier signing keys are **not** published in the JWKS, against spec §6.1. Only the gateway verifies
  courier tokens, so publishing them adds attack surface and buys nothing. The viewer token, `pkg/viewerjwt` and the
  JWKS endpoint are unchanged. Recorded in `docs/ARCHITECTURE.md` §12.2. If wrong: a partner or third party that
  wants to verify courier tokens needs the keys another way (nothing planned needs it).

The numbers R1-R13 follow the order the rulings were made during the build; R14 and R15 are the Task 11 ruling and the plan's JWKS ruling.

## Deferred minors

Found in the task reviews and not fixed. None blocks the PR. Grouped by area.

### Contract and API (OpenAPI, gateway)

- `CourierTokenRequest` is a flat object (no `oneOf`, to avoid an `oapi-codegen` runtime dependency). The handler
  validates the `grant_type` and field pairing, with a test.
- The `createCourierSession` 404 response uses the generic `NotFound` and does not name `courier_not_found` in its
  example.
- The OpenAPI link example shows `expires_at` for a `pending_link` session. Add `started_at` and `expires_at` notes
  for `pending_link`.
- Spec text says end-of-session on 401 or 403. The gateway's only 403 here is `insufficient_scope`; the SDK ends only
  on 401. Reconcile the spec wording.
- No test for the 51-fix batch limit on `/courier/locations`.
- Table-driven tests discard the case name (no `t.Run`) in some gateway tests; one comment ("session survives") is
  unasserted.
- Unknown refresh tokens are not charged to a rate bucket (a database lookup per attempt). Refresh reads the database
  twice. Reuse detection is delayed while the refresh bucket is exhausted.
- The nginx route for the courier API was not exercised by `make dev` plus `curl`.
- Unreachable branches are untested (`ErrSessionMode`, `ErrSessionLength`, a nil service). `expires_in_seconds` is
  converted from int to `Duration` and relies on the schema maximum. Link-mode idempotent replay is untested.
- `pkg/courierjwt`: no `Verify` tests for a wrong `scp`, `env` "prod" or `iss`. About 35 lines of key parsing are
  duplicated from `viewerjwt` (allowed by the plan).

### Deliveries and tracking services

- `deliveries` `rpc.Server` still embeds `UnimplementedDeliveriesServiceHandler`. Remove the embed and add a
  compile-time assertion now that every RPC is implemented.
- `ReportEvent` fills `next_actions` (the courier view) for every actor. Add a doc comment.
- `GetCourier` maps `ErrCourierNotFound` to NotFound by hand, while `ToConnect` maps it to InvalidArgument for
  Assign.
- `ListCourierDeliveries` repeats `GetDelivery`'s column list.
- The `serve()` test harness discards `trail.Run` errors, and the test timing depends on the 1 s flush.
- `systemEnded` only logs a failed deny-list write for `replaced` and `refresh_reuse` ends (no durable retry), and the
  path is untested.
- `TestOneActivePerCourierIndex` has a comment that claims cases it does not test.
- A direct assignment leaves the delivery `confirmed`; fixes sent right after an assignment can be dropped until
  NATS delivers it (retry for up to 10 s). `DELETE /courier-sessions/{id}` answers 200 with the session.

### `@dt/courier` SDK

- `fetch` has no timeout.
- A second `start()` returns early without saying so.
- A late denied callback after `gps.stop()` can set the state to `denied`.
- An old-format stored origin no longer matches (only matters for builds from before the release).
- The reload test leaves the first SDK instance running.
- `testkit.ts` lives in `src/` and ships in the package source.
- The SDK needs a refresh token to survive: a concurrent or retried refresh by another client kills the session
  (by design, M5-R4 and the reuse rule). Partners who write their own client must single-flight refresh.

### Courier page

- `endShift` has no `catch`.
- The main-button label lookup is not guarded for an unknown `next_action`: the render throws.
- A production build silently defaults `VITE_DT_API` to localhost (the staging build script refuses it).
- A wake-lock test is misnamed. There are no tests for the expired screen or for notices on the resume path.
- A redundant CSS rule. The CSP `<meta>` lacks `base-uri` and `form-action`.

### Local stack, edge and e2e

- The edge nginx repeats its header block six times (no include), and `connect-src` is hardcoded in `nginx.conf`.
  `ufw` advice in the phone runbook is moot when Docker publishes the ports.
- `make courier-link` creates a tenant per run (documented in its header).
- e2e: the courier presence check uses `[data-card="courier"]`, not the 3D label (hidden on iphone-se). An unescaped
  regex is built from `HEAD` strings (`courier.spec.ts`). `createRide` retries orphan deliveries. The project `use`
  spread comes after geolocation and permissions. A second 10 s fix-resend loop slows the test.
- e2e: the offline-revoke test allows 0 POSTs after reconnect. It should assert exactly one, or wait for the 401 on
  the locations POST. The 5 s quiet window misses longer backoffs, and the page exposes no readable buffered count.

### Staging infrastructure

- The staging README's `cdn.tf` row, and the missing `/assets/*` behavior: CloudFront answers `index.html` with 200
  for a missing asset, where the local nginx answers 404.
- The tftest does not assert the 403 and 404 error codes. The CSP API origin comes from two sources
  (`var.api_hostname` and `DT_WEB_API`). `tflint` and `checkov` are not installed (CI uses `trivy`).
- The "Publish the courier page" step runs before the smoke test, so a smoke failure leaves the new page live (the
  gateway is already on the new revision).
- The staging services lack the courier settings until the next apply: `COURIER_SIGNING_KEYS`,
  `COURIER_SIGNING_KEY_CURRENT` and `COURIER_URL_BASE` (set in Task 13, not applied).

## Staging courier hosting

**Choice:** a second CloudFront distribution (`aws_cloudfront_distribution.courier`, `infra/aws/staging/cdn.tf`) on
`courier.staging.deliveryontrack.io`, over `courier/current/` in the existing CDN bucket (origin path, same OAC).
The deploy uploads `courier/<sha>/` (immutable archive) and refreshes `courier/current/` (hashed `assets/`
immutable, `index.html` `no-cache`). SPA fallback is a 403/404 custom error response to `/index.html` with 200.
Headers come from their own response headers policy: CSP `default-src 'self'; connect-src https://api.staging.deliveryontrack.io;
frame-ancestors 'none'`, `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex`, `nosniff`, HSTS. The CloudFront
certificate gains the SAN; the gateway gets `COURIER_URL_BASE`, `COURIER_SIGNING_KEY_CURRENT=cr1` and the
`COURIER_SIGNING_KEYS` secret (own Ed25519 seed, separate from the viewer keys).

**Why not an alias plus a Host-routing CloudFront Function:** also about EUR 0, but it needs a function with the
release pointer baked in, and the CDN's CORS `*` / CORP cross-origin policy would apply to the courier page too
(a function would have to override per host). A second distribution keeps the two policies apart.

**Cost delta:** about EUR 0.00/month at idle (CloudFront bills per request and GB, and the free tier covers staging
traffic; no function, no WAF, no logs). Route 53 adds two alias records (alias queries to CloudFront are free). The
ACM certificate is free. Total staging estimate unchanged (about EUR 100/month budget).

**Notes:** `courier/<sha>/` is uploaded on every deploy (immutable archive), but `courier/current/` (what the
distribution serves) is refreshed only by the "Publish the courier page" step, after the services are stable and only
when the host is awake. A failed or asleep-hours deploy leaves the old page in place. Rolling an ECS service back does
not roll the page back: the owner copies `courier/<old sha>/` over `courier/current/` with their own credentials
(assets with `--cache-control "public, max-age=31536000, immutable"` first, then `index.html` with `no-cache`); the
deploy role has no GetObject. Not applied: budget pause (D28); the first `make staging-up` after the pause creates it
(the CloudFront certificate is replaced because it gains a SAN, which is free on a fresh stack).
