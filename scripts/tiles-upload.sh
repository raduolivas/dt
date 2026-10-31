#!/usr/bin/env bash
# Owner-only: uploads build/tiles/nl.pmtiles (make tiles-nl) to the staging CDN
# as s3://dt-staging-cdn-<account>/tiles/nl.pmtiles and invalidates it in
# CloudFront. Run from a management-account Identity Center Admin session
# after make staging-up (docs/runbooks/tiles.md). The deploy role cannot
# write tiles/: only this Admin path can.
#
# Env: DT_STAGING_ACCOUNT_ID (default: terraform.tfvars), DT_TILES_FILE
# (default build/tiles/nl.pmtiles), DT_CDN_HOSTNAME (default
# cdn.staging.deliveryontrack.io).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/lib/aws-staging.sh
source "$root/scripts/lib/aws-staging.sh"

export AWS_REGION="eu-central-1"
file="${DT_TILES_FILE:-$root/build/tiles/nl.pmtiles}"
host="${DT_CDN_HOSTNAME:-cdn.staging.deliveryontrack.io}"
key="tiles/nl.pmtiles"

[[ -s "$file" ]] || { echo "tiles-upload: $file is missing; run make tiles-nl first" >&2; exit 1; }
arn="$(aws sts get-caller-identity --query Arn --output text)"
[[ "$arn" != *":root" ]] || { echo "tiles-upload: run this as the Identity Center Admin, not root" >&2; exit 1; }

use_staging_credentials
bucket="dt-staging-cdn-$(staging_account_id)"

# The name is fixed (TRACKING_PAGE_TILES), so the file is not immutable: a
# day in caches, and an invalidation after each upload. PMTiles clients
# notice a new archive by its ETag.
aws s3 cp "$file" "s3://${bucket}/${key}" --content-type application/vnd.pmtiles \
  --cache-control "public, max-age=86400" --only-show-errors
echo "tiles-upload: s3://${bucket}/${key} ($(stat -c %s "$file") bytes)"

dist="$(aws cloudfront list-distributions --output text \
  --query "DistributionList.Items[?Aliases.Items && contains(Aliases.Items, '${host}')].Id | [0]")"
if [[ -n "$dist" && "$dist" != None ]]; then
  aws cloudfront create-invalidation --distribution-id "$dist" --paths "/${key}" \
    --query 'Invalidation.Id' --output text >/dev/null
  echo "tiles-upload: invalidated /${key} in ${dist}"
else
  echo "tiles-upload: no CloudFront distribution for ${host}; nothing invalidated" >&2
fi
echo "tiles-upload: check with curl -sI -H 'Range: bytes=0-126' https://${host}/${key}"
