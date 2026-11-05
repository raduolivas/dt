#!/usr/bin/env bash
# Points the local stack at another part of the Netherlands, for testing where you are rather than in Amsterdam:
# the OSRM routing data for a province (scripts/osrm-prepare.sh) and the full Netherlands map tiles
# (build/tiles/nl.pmtiles from make tiles-nl) instead of the Amsterdam fixture. The choice is written to
# build/region.env, which the Makefile reads on every run (Compose gets it as an env file; make courier-link takes
# DT_PICKUP from it). REGION=amsterdam goes back to the default and deletes build/region.env.
# Usage: make region REGION=overijssel [PICKUP=52.35649,6.66629]
set -euo pipefail

region="${1:?usage: $0 REGION [PICKUP]}"
pickup="${2:-}"
root="$(cd "$(dirname "$0")/.." && pwd)"
env_file="$root/build/region.env"
fail() { echo "region: $*" >&2; exit 1; }

[[ -z "$pickup" || "$pickup" =~ ^-?[0-9]+(\.[0-9]+)?,-?[0-9]+(\.[0-9]+)?$ ]] || fail "PICKUP must be lat,lng"

"$root/scripts/osrm-prepare.sh" "$region"

if [[ "$region" == amsterdam ]]; then
  rm -f "$env_file"
  echo "region: back to Amsterdam (routing data and the map fixture)"
  exit 0
fi

tiles="$root/build/tiles/nl.pmtiles"
[[ -s "$tiles" ]] || fail "no $tiles: run make tiles-nl first (it builds the Netherlands map tiles, about 500 MB)"
mkdir -p "$root/build/cdn/tiles"
# a hard link, not a 500 MB copy; the cdn service serves build/cdn/tiles
ln -f "$tiles" "$root/build/cdn/tiles/nl.pmtiles"

{
  echo "# Written by scripts/local-region.sh (make region). Delete it, or run make region REGION=amsterdam, to go back."
  echo "DT_REGION=${region}"
  echo "DT_LOCAL_TILES=nl"
  [[ -n "$pickup" ]] && echo "DT_PICKUP=${pickup}"
} >"$env_file"
echo "region: ${region} routing and the Netherlands map tiles${pickup:+, pickup ${pickup}}"
