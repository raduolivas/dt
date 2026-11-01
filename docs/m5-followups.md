# M5 follow-ups and controller rulings

Written during M5 (courier sessions, `@dt/courier` SDK, courier web link page). Task 14 fills out the rest.

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
