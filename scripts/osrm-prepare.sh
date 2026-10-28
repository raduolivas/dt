#!/usr/bin/env bash
# Prepares the OSRM routing data for the local stack and CI: downloads the
# Geofabrik Noord-Holland extract, optionally clips it to Amsterdam with
# osmium, and preprocesses it (MLD) for the car and bicycle profiles into
# infra/compose/osrm/<car|bike>/. Only Docker and curl are needed on the host;
# osmium and the OSRM tools run from images pinned by digest.
#
# Usage: scripts/osrm-prepare.sh [amsterdam|noord-holland]   (default: amsterdam)
#
# Idempotent: a stamp holds a checksum of this script and the region, and a
# profile whose stamp matches is left alone (no network). Delete
# infra/compose/osrm/ or run with OSRM_REFRESH=1 to download and build again.
set -euo pipefail

region="${1:-amsterdam}"
case "$region" in
  amsterdam | noord-holland) ;;
  *) echo "usage: $0 [amsterdam|noord-holland]" >&2; exit 2 ;;
esac

# Pinned by digest. osrm-backend ships /opt/car.lua and /opt/bicycle.lua.
OSRM_IMAGE="osrm/osrm-backend@sha256:af5d4a83fb90086a43b1ae2ca22872e6768766ad5fcbb07a29ff90ec644ee409"
OSMIUM_IMAGE="stefda/osmium-tool@sha256:d2321d0e926f77ead7547b4b35f5cf98d9fd74043673cecc4fc2bb7cce06ff63"
PBF_URL="https://download.geofabrik.de/europe/netherlands/noord-holland-latest.osm.pbf"
# Amsterdam and its surroundings: min lon, min lat, max lon, max lat.
AMSTERDAM_BBOX="4.72,52.27,5.08,52.44"

root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/infra/compose/osrm"
src="$out/src"
key="$(printf '%s\n%s\n' "$region" "$(sha256sum "$0" | cut -d' ' -f1)" | sha256sum | cut -d' ' -f1)"

# Rootless Docker maps the container's root to the host user already; with a
# rootful daemon, run as the host user so the data is not owned by root.
user_args=(--user "$(id -u):$(id -g)")
if docker info --format '{{.SecurityOptions}}' 2>/dev/null | grep -q rootless; then
  user_args=()
fi

declare -A lua=([car]=car.lua [bike]=bicycle.lua)

up_to_date=1
for p in car bike; do
  [[ "${OSRM_REFRESH:-0}" != 1 && "$(cat "$out/$p/.stamp" 2>/dev/null || true)" == "$key" ]] || up_to_date=0
done
if (( up_to_date )); then
  echo "osrm-prepare: data for $region is up to date"
  exit 0
fi

mkdir -p "$src"
pbf="$src/noord-holland-latest.osm.pbf"
if [[ ! -s "$pbf" || "${OSRM_REFRESH:-0}" == 1 ]]; then
  echo "osrm-prepare: downloading $PBF_URL"
  curl -fL --retry 3 -o "$pbf.part" "$PBF_URL"
  want="$(curl -fsSL --retry 3 "$PBF_URL.md5" | cut -d' ' -f1)"
  have="$(md5sum "$pbf.part" | cut -d' ' -f1)"
  if [[ "$want" != "$have" ]]; then
    rm -f "$pbf.part"
    echo "osrm-prepare: checksum mismatch for the download (want $want, got $have)" >&2
    exit 1
  fi
  mv "$pbf.part" "$pbf"
fi

data="$pbf"
if [[ "$region" == amsterdam ]]; then
  data="$src/amsterdam.osm.pbf"
  echo "osrm-prepare: clipping to $AMSTERDAM_BBOX"
  docker run --rm "${user_args[@]}" -v "$src:/src" "$OSMIUM_IMAGE" \
    osmium extract --overwrite -b "$AMSTERDAM_BBOX" -o /src/amsterdam.osm.pbf /src/noord-holland-latest.osm.pbf
fi

for p in car bike; do
  dir="$out/$p"
  rm -rf "$dir"
  mkdir -p "$dir"
  cp "$data" "$dir/region.osm.pbf"
  echo "osrm-prepare: preprocessing the $p profile (MLD)"
  osrm() { # tool args...
    local tool="$1"; shift
    docker run --rm "${user_args[@]}" -v "$dir:/data" --entrypoint "$tool" "$OSRM_IMAGE" "$@"
  }
  osrm osrm-extract -p "/opt/${lua[$p]}" /data/region.osm.pbf
  osrm osrm-partition /data/region.osrm
  osrm osrm-customize /data/region.osrm
  rm -f "$dir/region.osm.pbf"
  printf '%s' "$key" >"$dir/.stamp"
done
echo "osrm-prepare: done ($region)"
