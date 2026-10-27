#!/usr/bin/env bash
# Apply infra/aws/bootstrap in two phases, both from CloudShell in the
# management account. See docs/runbooks/aws-account-setup.md.
#
#   PHASE=a  as root: organization, accounts, SCPs, Identity Center, CloudTrail,
#            budgets, state bucket (no cross-account role needed). State starts
#            local and is migrated into the state bucket at the end.
#   PHASE=b  as an Identity Center Admin session (never root): the same apply
#            plus the GitHub OIDC role in dt-staging, which needs sts:AssumeRole
#            into OrganizationAccountAccessRole (STS refuses that for root).
#
# Usage: make aws-bootstrap PHASE=a   (or b). Safe to re-run.
set -euo pipefail

phase="${PHASE:-${1:-}}"
case "${phase}" in
  a | b) ;;
  *) echo "Set PHASE=a (as root) or PHASE=b (as an Identity Center Admin session). See the runbook." >&2; exit 2 ;;
esac

cd "$(dirname "$0")/../infra/aws/bootstrap"

if [[ ! -f terraform.tfvars ]]; then
  echo "terraform.tfvars is missing: copy terraform.tfvars.example and fill it in." >&2
  exit 1
fi
command -v tofu >/dev/null || { echo "tofu is not installed (see the runbook, step 3)." >&2; exit 1; }
command -v aws >/dev/null || { echo "aws CLI is not installed." >&2; exit 1; }

identity="$(aws sts get-caller-identity --query Arn --output text)"
account_id="$(aws sts get-caller-identity --query Account --output text)"
bucket="dt-tofu-state-${account_id}"
state_key="bootstrap/terraform.tfstate"
is_root=false
[[ "${identity}" == *":root" ]] && is_root=true

if [[ "${phase}" == "b" && "${is_root}" == "true" ]]; then
  echo "Phase B must not run as root: STS refuses AssumeRole with root credentials, so the staging account could not be configured." >&2
  echo "Sign in through the Identity Center portal as Admin on the management account and open CloudShell from that session (runbook step 4)." >&2
  exit 1
fi
if [[ "${phase}" == "a" && "${is_root}" == "false" ]]; then
  echo "Note: phase A is normally run as root; continuing as ${identity}."
fi
echo "Phase ${phase} as ${identity}; management account ${account_id}; state bucket ${bucket}."

write_backend() {
  cat > backend_override.tf <<EOT
terraform {
  backend "s3" {
    bucket       = "${bucket}"
    key          = "${state_key}"
    region       = "eu-central-1"
    use_lockfile = true
    encrypt      = true
  }
}
EOT
}

# Phase A leaves the staging OIDC role out; phase B creates it.
if [[ "${phase}" == "a" ]]; then
  export TF_VAR_enable_staging_oidc=false
fi

remote_state_exists=false
if aws s3api head-object --bucket "${bucket}" --key "${state_key}" >/dev/null 2>&1; then
  remote_state_exists=true
fi

if [[ "${remote_state_exists}" == "true" ]]; then
  if [[ -f terraform.tfstate ]]; then
    echo "Both a local terraform.tfstate and s3://${bucket}/${state_key} exist. Refusing to guess which is current:" >&2
    echo "compare them, then delete or move the stale one." >&2
    exit 1
  fi
  write_backend
  tofu init -input=false -reconfigure
elif [[ "${phase}" == "b" ]]; then
  echo "No state found in s3://${bucket}/${state_key}: run phase A first (as root)." >&2
  exit 1
else
  # First run, or a re-run after a partial one (a local terraform.tfstate is kept and reused).
  # -reconfigure also switches back to local state after a failed migration.
  rm -f backend_override.tf
  tofu init -input=false -reconfigure
fi

# Adopt what AWS may already have created. The imports go in a git-ignored
# file that is rewritten on every run; an import of something already in
# state is a no-op.
: > imports_generated.tf
state_list="$(tofu state list 2>/dev/null || true)"

org_id="$(aws organizations describe-organization --query Organization.Id --output text 2>/dev/null || true)"
if [[ -n "${org_id}" && "${org_id}" != "None" ]] && ! grep -qx 'aws_organizations_organization.this' <<<"${state_list}"; then
  echo "An organization (${org_id}) already exists (Identity Center creates one): importing it."
  cat >> imports_generated.tf <<EOT
import {
  to = aws_organizations_organization.this
  id = "${org_id}"
}
EOT
fi

# AWS allows one DIMENSIONAL/SERVICE anomaly monitor per account and may have
# created a default one. Reuse it unless this stack already owns dt-services.
if ! grep -qx 'aws_ce_anomaly_monitor.services\[0\]' <<<"${state_list}"; then
  if ! monitor_arn="$(aws ce get-anomaly-monitors \
    --query "AnomalyMonitors[?MonitorType=='DIMENSIONAL' && MonitorDimension=='SERVICE'].MonitorArn | [0]" \
    --output text)"; then
    echo "WARNING: could not list cost anomaly monitors; creating dt-services. If apply fails because a service monitor already exists, re-run after fixing access to Cost Explorer." >&2
    monitor_arn=""
  fi
  if [[ -n "${monitor_arn}" && "${monitor_arn}" != "None" ]]; then
    echo "Reusing the existing service anomaly monitor ${monitor_arn}."
    export TF_VAR_anomaly_monitor_arn="${monitor_arn}"
  fi
fi

tofu apply

# Bucket exists now: move local state into it (idempotent).
if [[ "${remote_state_exists}" == "false" ]]; then
  write_backend
  tofu init -input=false -migrate-state -force-copy
  echo "State migrated to s3://${bucket}/${state_key}."
  echo "Delete the local copy: rm -f terraform.tfstate terraform.tfstate.backup"
fi

if [[ "${phase}" == "a" ]]; then
  echo "Phase A done. Next: runbook step 4 (accept the invitation, sign in as Admin), then PHASE=b."
fi
