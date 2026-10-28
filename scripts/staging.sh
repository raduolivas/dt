#!/usr/bin/env bash
# Owner-only helper for the AWS staging stack (infra/aws/staging).
# Run from a management-account Identity Center Admin session (CloudShell or
# a profile), never as root. See docs/runbooks/staging.md.
#
# Usage: scripts/staging.sh up | down | wake | sleep
#   up     tofu apply. The first run creates the hosted zone and stops until
#          staging.deliveryontrack.io is delegated to it (runbook step 6).
#   down   tofu destroy of everything except the hosted zone and the KMS key
#          (prevent_destroy); the state bucket belongs to the bootstrap.
#   wake   start the database and the ECS host now (outside working hours).
#   sleep  scale the host to 0 and stop the database now.
#
# Env: DT_STATE_BUCKET and DT_STATE_KMS_KEY_ARN (default: the bootstrap
# outputs state_bucket and state_kms_key_arn), DT_STAGING_ACCOUNT_ID (default:
# terraform.tfvars), DT_SKIP_DELEGATION_CHECK=1 to skip the DNS check.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/lib/aws-staging.sh
source "$root/scripts/lib/aws-staging.sh"

ASG="dt-staging-ecs-host"
DB="dt-staging"
REGION="eu-central-1"
export AWS_REGION="$REGION"

tf() { tofu -chdir="$STAGING_DIR" "$@"; }

refuse_root() {
  local arn
  arn="$(aws sts get-caller-identity --query Arn --output text)"
  [[ "$arn" != *":root" ]] || { echo "staging: run this as the Identity Center Admin, not root" >&2; exit 1; }
}

bootstrap_output() {
  tofu -chdir="$root/infra/aws/bootstrap" output -raw "$1" 2>/dev/null || true
}

init() {
  local bucket="${DT_STATE_BUCKET:-$(bootstrap_output state_bucket)}"
  local kms="${DT_STATE_KMS_KEY_ARN:-$(bootstrap_output state_kms_key_arn)}"
  [[ -n "$bucket" && -n "$kms" ]] || {
    echo "staging: set DT_STATE_BUCKET and DT_STATE_KMS_KEY_ARN (bootstrap outputs state_bucket, state_kms_key_arn)" >&2
    exit 1
  }
  [[ -f "$STAGING_DIR/terraform.tfvars" ]] || {
    echo "staging: copy $STAGING_DIR/terraform.tfvars.example to terraform.tfvars and fill it in" >&2
    exit 1
  }
  tf init -reconfigure -input=false -backend-config="bucket=$bucket" -backend-config="kms_key_id=$kms" >/dev/null
}

# zone_name_servers: the hosted zone's name servers, from the state (also
# right after the targeted apply, when root outputs may not be refreshed).
zone_name_servers() {
  tf show -json | jq -r '.values.root_module.resources[]? | select(.address == "aws_route53_zone.staging") | .values.name_servers[]'
}

# delegated: true when public DNS returns the zone's own name servers.
delegated() {
  local zone want got
  zone="$(sed -nE 's/^[[:space:]]*zone_name[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' "$STAGING_DIR/terraform.tfvars")"
  zone="${zone:-staging.deliveryontrack.io}"
  want="$(zone_name_servers | sed 's/\.$//' | tr '[:upper:]' '[:lower:]' | sort)"
  got="$(curl -fsS --max-time 10 "https://dns.google/resolve?name=${zone}&type=NS" |
    jq -r '.Answer[]? | select(.type == 2) | .data' | sed 's/\.$//' | tr '[:upper:]' '[:lower:]' | sort)"
  [[ -n "$got" && "$got" == "$want" ]]
}

up() {
  refuse_root
  init
  tf apply -input=false -target=aws_route53_zone.staging
  if [[ "${DT_SKIP_DELEGATION_CHECK:-0}" != 1 ]] && ! delegated; then
    echo >&2
    echo "staging: the zone is not delegated yet. At the registrar, set these NS records" >&2
    echo "for staging.deliveryontrack.io (runbook step 6), wait for DNS, then run make staging-up again:" >&2
    zone_name_servers >&2
    exit 3
  fi
  tf apply -input=false
}

down() {
  refuse_root
  init
  # Every managed resource except the ones that outlive staging. Targeting
  # them by address leaves the zone and the key (prevent_destroy) alone.
  local keep='^(aws_route53_zone\.staging|aws_kms_key\.data|aws_kms_alias\.data)$'
  local targets=()
  while IFS= read -r addr; do
    targets+=("-target=$addr")
  done < <(tf state list | grep -v '^data\.' | sed -E 's/\[.*\]$//' | sort -u | grep -Ev "$keep")
  if ((${#targets[@]} == 0)); then
    echo "staging: nothing to destroy"
    return 0
  fi
  tf destroy -input=false "${targets[@]}"
  echo "staging: down. Kept: the hosted zone, alias/dt-staging-data and its key, and the state."
}

wake() {
  use_staging_credentials
  aws rds start-db-instance --db-instance-identifier "$DB" >/dev/null 2>&1 ||
    echo "staging: database not started (already running or starting)" >&2
  aws autoscaling update-auto-scaling-group --auto-scaling-group-name "$ASG" --min-size 0 --desired-capacity 1
  echo "staging: waking; the database takes about 5-10 minutes, the services follow"
}

sleep_() {
  use_staging_credentials
  aws autoscaling update-auto-scaling-group --auto-scaling-group-name "$ASG" --min-size 0 --desired-capacity 0
  aws rds stop-db-instance --db-instance-identifier "$DB" >/dev/null 2>&1 ||
    echo "staging: database not stopped (already stopped or stopping)" >&2
  echo "staging: asleep (the schedule wakes it at 08:00 Monday-Friday)"
}

case "${1:-}" in
  up) up ;;
  down) down ;;
  wake) wake ;;
  sleep) sleep_ ;;
  *) echo "usage: $0 up|down|wake|sleep" >&2; exit 2 ;;
esac
