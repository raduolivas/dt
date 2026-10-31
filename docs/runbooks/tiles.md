# Runbook: map tiles and the hosted tracking page on staging (owner)

Who: the owner, from a **management-account Identity Center Admin session** (as in `docs/runbooks/staging.md`). Never as root. Result: `https://track.staging.deliveryontrack.io/t/<token>` opens the hosted tracking page, whose page, SDK and map tiles come from `https://cdn.staging.deliveryontrack.io`.

## The tiles

One PMTiles archive of the Netherlands in the OpenMapTiles schema, built with Planetiler from the Geofabrik extract (`scripts/tiles-build.sh nl`, image pinned by digest), z0-14, layers `transportation, building, water, park, landuse, landcover, waterway, boundary, place`. It lives at `s3://dt-staging-cdn-<account>/tiles/nl.pmtiles` and is served as `https://cdn.staging.deliveryontrack.io/tiles/nl.pmtiles` (the gateway's `TRACKING_PAGE_TILES` and the staging SDK's default). The renderer reads it with HTTP range requests; CloudFront passes them through. Attribution "© OpenStreetMap contributors · © OpenMapTiles" is always shown by the page and the SDK.

Current build (Claude, local, 2026-10-31):

| | |
|---|---|
| File | `build/tiles/nl.pmtiles` (git-ignored; sidecar `build/tiles/nl.json`) |
| Size | 520,768,620 bytes (497 MiB) |
| OSM data date | 2026-10-29 (`osmosis_replication_timestamp` of Geofabrik `netherlands-latest.osm.pbf`, 1.41 GB) |
| Build time | 31 min on a 4-core, 6 GB laptop (Planetiler `--storage=mmap`, 3 GB heap) |

Rebuild whenever the map should be fresher (monthly is plenty for staging):

```sh
make tiles-nl            # needs Docker and curl; downloads the extract once into build/tiles/cache/
```

`build/tiles/cache/netherlands-latest.osm.pbf` is reused while it exists: delete it to fetch a newer extract. On a machine with more memory `TILES_HEAP=6g make tiles-nl` is faster. The OSM date of a build is in `build/tiles/nl.json` (`osmDataDate`).

## Owner steps: bring the CDN, the tiles and the hosted page to staging

Prerequisites: `docs/runbooks/staging.md` steps 1-11 are done (staging applied, delegated, deployed, smoke green). Bootstrap phase B created the deploy role `dt-deploy-staging`; this stack attaches one more inline policy to it. Run everything from the repository root on `main` with the M4 infrastructure merged.

### 1. Apply the infrastructure

```sh
make staging-up
```

Review the plan before typing `yes`. Expected:

- **Create**: the bucket `dt-staging-cdn-<account>` with its public access block, ownership controls, versioning (off), encryption (SSE-S3), CORS and policy; the CloudFront origin access control, response headers policy and distribution; the ACM certificate for `cdn.staging.deliveryontrack.io` in `us-east-1` with its validation record and validation; the records `cdn.staging` (A and AAAA, to CloudFront) and `track.staging` (A, to the ALB); the inline policy `cdn-upload` on `dt-deploy-staging`.
- **Replace**: the ALB certificate `aws_acm_certificate.api` (it gains `track.staging.deliveryontrack.io` as a SAN; created before the old one goes, so the API stays up) and its validation records; the listener switches to the new certificate.
- **Update in place**: the gateway task definitions (`dt-staging-gateway`, `dt-staging-migrate-gateway`) get a new revision with `TRACKING_URL_BASE`, `TRACKING_PAGE_BASE`, `TRACKING_PAGE_CDN`, `TRACKING_PAGE_TILES`, `TRACKING_PAGE_API`.
- **Nothing else destroyed.** If the plan wants to destroy the zone, the database or the KMS key, stop and ask.

The distribution takes about 5-15 minutes to deploy; the certificate validation needs the delegated zone (already done). The CloudFront certificate is made in `us-east-1`, which the bootstrap's region-lock SCP allows for ACM only (`DenyAcmOutsideHomeRegionAndUsEast1`); CloudFront and Route 53 are global.

Check: `curl -sI https://cdn.staging.deliveryontrack.io/tiles/nl.pmtiles` answers (403 until step 2: the bucket is empty and CloudFront may not list it, so a missing file is 403, not 404).

### 2. Upload the tiles

```sh
make tiles-nl            # only if build/tiles/nl.pmtiles is missing or stale (see above)
make tiles-upload
```

`make tiles-upload` assumes `OrganizationAccountAccessRole` in `dt-staging` (the deploy role cannot write `tiles/`), uploads `build/tiles/nl.pmtiles` to `tiles/nl.pmtiles` (`Content-Type: application/vnd.pmtiles`, cached for a day) and invalidates it in CloudFront. Check:

```sh
curl -sS -o /dev/null -D - -H 'Range: bytes=0-126' -H 'Origin: https://example.com' \
  https://cdn.staging.deliveryontrack.io/tiles/nl.pmtiles
```

Expect `HTTP/2 206`, `content-range: bytes 0-126/<size>`, `access-control-allow-origin: *` and `cross-origin-resource-policy: cross-origin`.

### 3. Deploy

Merge to `main` (or run the `deploy-staging` workflow from `main`) during working hours, or about 10 minutes after `make staging-wake`. The run now starts with **Build the hosted page and the SDK for staging** and, before registering the task definitions, **Upload the page and the SDK to the CDN** (`page/<sha>/`, `sdk/tracking/<sha>/`). The gateway's new revision points `TRACKING_PAGE_BASE` at `https://cdn.staging.deliveryontrack.io/page/<sha>/`. If the register step fails with "has no TRACKING_PAGE_BASE", step 1 was not applied yet.

Check (with the commit of the run as `<sha>`):

```sh
curl -sI https://cdn.staging.deliveryontrack.io/page/<sha>/entry.js | grep -i -E '^(HTTP|cache-control|content-type)'
curl -sI https://cdn.staging.deliveryontrack.io/sdk/tracking/<sha>/tracking.iife.js | head -1
```

Expect `200` and `cache-control: public, max-age=31536000, immutable`.

### 4. Open a simulated delivery on a phone

With the staging test key (`STAGING_TEST_KEY`, made in staging runbook step 10; keep it out of shell history):

```sh
read -rs DT_KEY            # paste the test key
api=https://api.staging.deliveryontrack.io/v1
idem() { echo "owner-$(date +%s%N)"; }
d="$(curl -fsS -X POST "$api/deliveries" -H "Authorization: Bearer $DT_KEY" -H "Idempotency-Key: $(idem)" \
  -H 'Content-Type: application/json' --data '{"external_id":"owner-look-'"$(date +%s)"'","delivery_model":"end_to_end",
  "pickup":{"location":{"lat":52.3731,"lng":4.8922},"name":"Test Kitchen"},
  "dropoff":{"location":{"lat":52.3600,"lng":4.8852},"label":"Test Door"},"customer":{"first_name":"Ana"}}')"
jq -r .tracking.url <<<"$d"                         # https://track.staging.deliveryontrack.io/t/<token>
curl -fsS -X POST "$api/test/deliveries/$(jq -r .id <<<"$d")/simulate" -H "Authorization: Bearer $DT_KEY" \
  -H "Idempotency-Key: $(idem)" -H 'Content-Type: application/json' --data '{"speed_factor":5}'
unset DT_KEY
```

Open the printed `tracking.url` on the phone (and on desktop). Expect: "Getting your delivery ready…" briefly, then the Amsterdam map in 3D with the store and "Your address", the courier moving once the simulation assigns one, and the ETA card. In the browser's network panel the page's `entry.js` comes from `cdn.staging…/page/<sha>/`, tiles from `cdn.staging…/tiles/nl.pmtiles` (206 answers), the API calls from `api.staging…`. If the map stays blank, check the console for a CSP or CORS error and send it to Claude.

## Costs

The bucket holds the tiles plus about 2.5 MB per deploy (cents per month); CloudFront's always-free tier (1 TB out, 10 million requests per month) covers team and test traffic; the `us-east-1` certificate is free. See the cost table in `docs/runbooks/staging.md`.

## Teardown

`make staging-down` removes the bucket with its contents (`force_destroy`), the distribution and the certificates. After the next `make staging-up`, run `make tiles-upload` again and re-run the deploy.
