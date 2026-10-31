#!/usr/bin/env bash
# Builds the hosted tracking page and the tracking SDK for the staging CDN.
#
# Usage: scripts/web-staging-build.sh <git sha> <out dir>
#   Writes <out dir>/page/<sha>/ and <out dir>/sdk/tracking/<sha>/, the layout of the CDN bucket
#   (.github/workflows/deploy-staging.yml uploads <out dir> as is).
#
# make web-assets builds the same packages into the same dist/ folders with the local stack's defaults
# (localhost API, the fixture tiles) and the end-to-end test hooks. This script never reuses a dist/: it deletes
# both, builds them again with the staging values set explicitly and the test hooks unset, copies them out, and
# refuses the result if it still names localhost, the fixture or the test hook, or lacks the staging API.
# Env (defaults are staging's): DT_WEB_API, DT_WEB_TILES.
set -euo pipefail

sha="${1:-}"
out="${2:-}"
[[ "$sha" =~ ^[0-9a-f]{7,40}$ && -n "$out" ]] || { echo "usage: $0 <git sha> <out dir>" >&2; exit 2; }

api="${DT_WEB_API:-https://api.staging.deliveryontrack.io/v1}"
tiles="${DT_WEB_TILES:-https://cdn.staging.deliveryontrack.io/tiles/nl.pmtiles}"

root="$(cd "$(dirname "$0")/.." && pwd)"
page_dist="$root/web/apps/tracking-page/dist"
sdk_dist="$root/web/packages/tracking/dist"

rm -rf "$page_dist" "$sdk_dist" "$out"
# env -u: a VITE_DT_TEST_HOOKS left in the environment must never reach a CDN build.
build() { env -u VITE_DT_TEST_HOOKS VITE_DT_API="$api" VITE_DT_TILES="$tiles" pnpm -C "$root/web" --filter "$1" build; }
build @dt/tracking-page
build @dt/tracking

mkdir -p "$out/page" "$out/sdk/tracking"
cp -R "$page_dist" "$out/page/$sha"
cp -R "$sdk_dist" "$out/sdk/tracking/$sha"

fail() { echo "web-staging-build: $*" >&2; exit 1; }
[[ -s "$out/page/$sha/entry.js" && -s "$out/page/$sha/entry.css" ]] || fail "the page lacks entry.js or entry.css"
[[ -s "$out/sdk/tracking/$sha/tracking.js" && -s "$out/sdk/tracking/$sha/tracking.iife.js" ]] || fail "the SDK bundles are missing"
if grep -rlE 'localhost|127\.0\.0\.1|amsterdam-z14\.pmtiles|dt-clock' "$out" >&2; then
  fail "the files above carry local-stack defaults or the test hook"
fi
grep -qF "$api" "$out/sdk/tracking/$sha/tracking.js" || fail "the SDK does not default to $api"
grep -qF "$api" "$out/sdk/tracking/$sha/tracking.iife.js" || fail "the SDK (IIFE) does not default to $api"
echo "web-staging-build: $out/page/$sha and $out/sdk/tracking/$sha (api $api, tiles $tiles)"
