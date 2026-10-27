#!/usr/bin/env bash
# Builds the Noord-Holland OSRM data (car and bike, MLD) with
# scripts/osrm-prepare.sh and uploads it to s3://<bucket>/<date>/{car,bike}/
# for staging. The ECS OSRM tasks sync the prefix named by the tofu variable
# osrm_data_version, so publishing never changes a running deployment:
# set osrm_data_version to the printed date and run make staging-up.
#
# Usage: scripts/osrm-publish.sh
# Env: DT_OSRM_BUCKET (default dt-staging-osrm, the tofu default),
#      DT_OSRM_VERSION (default: today, UTC, YYYY-MM-DD),
#      DT_STAGING_ACCOUNT_ID (see scripts/lib/aws-staging.sh).
# Credentials: a management-account Admin session (the script assumes
# OrganizationAccountAccessRole in dt-staging) or dt-staging's own.
#
# Note: it rebuilds infra/compose/osrm/ for Noord-Holland; the next make dev
# rebuilds the Amsterdam data (osrm-prepare's stamp no longer matches).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/lib/aws-staging.sh
source "$root/scripts/lib/aws-staging.sh"

bucket="${DT_OSRM_BUCKET:-dt-staging-osrm}"
version="${DT_OSRM_VERSION:-$(date -u +%F)}"
[[ "$version" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || { echo "osrm-publish: DT_OSRM_VERSION must be YYYY-MM-DD" >&2; exit 2; }

"$root/scripts/osrm-prepare.sh" noord-holland

use_staging_credentials
export AWS_REGION="${AWS_REGION:-eu-central-1}"
for p in car bike; do
  echo "osrm-publish: uploading $p to s3://$bucket/$version/$p/"
  aws s3 sync "$root/infra/compose/osrm/$p/" "s3://$bucket/$version/$p/" \
    --exclude ".stamp" --exclude "*.osm.pbf" --no-progress --only-show-errors
done
echo "osrm-publish: done. Set osrm_data_version = \"$version\" in infra/aws/staging/terraform.tfvars and run make staging-up."
