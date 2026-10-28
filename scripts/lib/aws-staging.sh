# shellcheck shell=bash
# Shared by scripts/staging.sh and scripts/osrm-publish.sh (sourced, not run).
# The owner works from a management-account Identity Center Admin session
# (the state bucket only answers to it); commands that act inside dt-staging
# assume OrganizationAccountAccessRole there, as the tofu provider does.

STAGING_DIR="${STAGING_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/infra/aws/staging}"

# staging_account_id: DT_STAGING_ACCOUNT_ID, else staging_account_id from
# infra/aws/staging/terraform.tfvars.
staging_account_id() {
  local id="${DT_STAGING_ACCOUNT_ID:-}"
  if [[ -z "$id" && -f "$STAGING_DIR/terraform.tfvars" ]]; then
    id="$(sed -nE 's/^[[:space:]]*staging_account_id[[:space:]]*=[[:space:]]*"([0-9]{12})".*/\1/p' "$STAGING_DIR/terraform.tfvars")"
  fi
  [[ "$id" =~ ^[0-9]{12}$ ]] || {
    echo "staging account id unknown: set DT_STAGING_ACCOUNT_ID or staging_account_id in $STAGING_DIR/terraform.tfvars" >&2
    return 1
  }
  printf '%s' "$id"
}

# use_staging_credentials: exports short-lived dt-staging credentials in the
# current shell, unless the current credentials already are dt-staging's
# (for example an Identity Center profile of that account).
use_staging_credentials() {
  local id current creds
  id="$(staging_account_id)"
  current="$(aws sts get-caller-identity --query Account --output text)"
  [[ "$current" == "$id" ]] && return 0
  creds="$(aws sts assume-role --role-arn "arn:aws:iam::${id}:role/OrganizationAccountAccessRole" \
    --role-session-name "dt-staging-${USER:-owner}" --duration-seconds 3600 \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)"
  read -r AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN <<<"$creds"
  export AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN
  unset AWS_PROFILE
}
