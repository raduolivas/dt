#!/usr/bin/env bash
# One-time (and idempotent) apply of infra/aws/bootstrap, run by the owner from
# CloudShell in the management account. Local state first, then the state is
# migrated into the bucket the stack creates. See docs/runbooks/aws-account-setup.md.
set -euo pipefail

cd "$(dirname "$0")/../infra/aws/bootstrap"

if [[ ! -f terraform.tfvars ]]; then
  echo "terraform.tfvars is missing: copy terraform.tfvars.example and fill it in." >&2
  exit 1
fi
command -v tofu >/dev/null || { echo "tofu is not installed (see the runbook, step 3)." >&2; exit 1; }
command -v aws >/dev/null || { echo "aws CLI is not installed." >&2; exit 1; }

account_id="$(aws sts get-caller-identity --query Account --output text)"
bucket="dt-tofu-state-${account_id}"
echo "Management account ${account_id}, state bucket ${bucket}."

write_backend() {
  cat > backend_override.tf <<EOT
terraform {
  backend "s3" {
    bucket       = "${bucket}"
    key          = "bootstrap/terraform.tfstate"
    region       = "eu-central-1"
    use_lockfile = true
    encrypt      = true
  }
}
EOT
}

if aws s3api head-bucket --bucket "${bucket}" 2>/dev/null; then
  # Re-run: state already lives in the bucket.
  write_backend
  tofu init -input=false -reconfigure
  tofu apply
  exit 0
fi

tofu init -input=false
# The staging provider needs the account id, which exists only after the
# accounts are created, so create those first.
tofu apply -target=aws_organizations_account.staging -target=aws_organizations_account.production
tofu apply

write_backend
tofu init -input=false -migrate-state -force-copy
echo "State migrated to s3://${bucket}/bootstrap/terraform.tfstate."
echo "Check, then delete the local copy: ls terraform.tfstate* && rm terraform.tfstate terraform.tfstate.backup"
