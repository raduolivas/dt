#!/usr/bin/env bash
# Builds OpenMapTiles-schema PMTiles with Planetiler for the renderer.
#
# Usage: scripts/tiles-build.sh fixture|nl
#   fixture  central Amsterdam (bbox 4.86,52.35,4.93,52.38), z0-14, into
#            web/packages/renderer/fixtures/amsterdam-z14.pmtiles (must stay <= 3 MB).
#   nl       the Netherlands (Geofabrik extract), z0-14, into build/tiles/nl.pmtiles.
# Both write a sidecar .json next to the output with the OSM data date and the pinned image.
#
# Downloads (OSM extract, Natural Earth, water polygons) are cached in build/tiles/cache/ (git-ignored).
# Needs only Docker and curl. The Planetiler image is pinned by digest.
set -euo pipefail

target="${1:-}"
PLANETILER_IMAGE="ghcr.io/onthegomap/planetiler@sha256:23dae8c006cfd4612cc1c439ca1ca6d02561eeffb3d6a78115ee020a62fca884"
FIXTURE_BBOX="4.86,52.35,4.93,52.38"
OSMIUM_IMAGE="stefda/osmium-tool@sha256:d2321d0e926f77ead7547b4b35f5cf98d9fd74043673cecc4fc2bb7cce06ff63"
# The fixture is clipped from the extract with a margin first, so Planetiler works on a tiny file (low memory).
FIXTURE_CLIP="4.84,52.34,4.95,52.39"
MAX_FIXTURE_BYTES=$((3 * 1024 * 1024))

root="$(cd "$(dirname "$0")/.." && pwd)"
cache="$root/build/tiles/cache"
mkdir -p "$cache"

case "$target" in
  fixture)
    pbf_url="https://download.geofabrik.de/europe/netherlands/noord-holland-latest.osm.pbf"
    pbf="noord-holland-latest.osm.pbf"
    out="$root/web/packages/renderer/fixtures/amsterdam-z14.pmtiles"
    bounds_args=(--bounds="$FIXTURE_BBOX")
    # Only z14 and only the layers the renderer draws, to stay under the size limit.
    minzoom=14
    layers="transportation,building,water,park,landuse,landcover"
    ;;
  nl)
    pbf_url="https://download.geofabrik.de/europe/netherlands-latest.osm.pbf"
    pbf="netherlands-latest.osm.pbf"
    out="$root/build/tiles/nl.pmtiles"
    bounds_args=()
    minzoom=0
    layers="transportation,building,water,park,landuse,landcover,waterway,boundary,place"
    ;;
  *) echo "usage: $0 fixture|nl" >&2; exit 2 ;;
esac
mkdir -p "$(dirname "$out")"

if [ ! -s "$cache/$pbf" ]; then
  curl -fL --retry 3 -o "$cache/$pbf.part" "$pbf_url"
  mv "$cache/$pbf.part" "$cache/$pbf"
fi
# The extract's own timestamp (osmosis_replication_timestamp in the header), falling back to the file date.
osm_date="$(docker run --rm -v "$cache:/data" "$OSMIUM_IMAGE" osmium fileinfo -g header.option.osmosis_replication_timestamp "/data/$pbf" 2>/dev/null | cut -c1-10 || true)"
[ -n "$osm_date" ] || osm_date="$(date -u -r "$cache/$pbf" +%Y-%m-%d)"

if [ "$target" = fixture ]; then
  clip="noord-holland-amsterdam-clip.osm.pbf"
  docker run --rm -v "$cache:/data" "$OSMIUM_IMAGE" osmium extract --overwrite --strategy=simple -b "$FIXTURE_CLIP" -o "/data/$clip" "/data/$pbf"
  pbf="$clip"
fi

tmp="$cache/out-$target.pmtiles"
rm -f "$tmp"
# --network host: Planetiler downloads its auxiliary sources itself, and on some dev machines the default Docker
# bridge network has no DNS. This is a dev script only.
docker run --rm --network host -e JAVA_TOOL_OPTIONS=-Xmx2g -v "$cache:/data" -w /data "$PLANETILER_IMAGE" \
  --osm-path="/data/$pbf" --output="/data/out-$target.pmtiles" \
  --download --force --maxzoom=14 --minzoom="$minzoom" --only-layers="$layers" "${bounds_args[@]}" \
  --nodemap-type=sparsearray --storage=ram

if [ "$target" = fixture ]; then
  size="$(stat -c %s "$tmp")"
  if [ "$size" -gt "$MAX_FIXTURE_BYTES" ]; then
    echo "fixture is $size bytes, over the 3 MB limit" >&2
    exit 1
  fi
fi
mv "$tmp" "$out"
size="$(stat -c %s "$out")"
cat > "${out%.pmtiles}.json" <<JSON
{
  "osmSource": "$pbf_url",
  "osmDataDate": "$osm_date",
  "planetilerImage": "$PLANETILER_IMAGE",
  "bounds": "${bounds_args[*]:-planet extract}",
  "minzoom": $minzoom,
  "maxzoom": 14,
  "layers": "$layers",
  "bytes": $size
}
JSON
echo "wrote $out ($size bytes)"
