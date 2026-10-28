data "aws_caller_identity" "management" {}

resource "aws_organizations_organization" "this" {
  feature_set = "ALL"

  enabled_policy_types = ["SERVICE_CONTROL_POLICY"]

  aws_service_access_principals = [
    "cloudtrail.amazonaws.com",
    "sso.amazonaws.com",
  ]
}

locals {
  root_id = aws_organizations_organization.this.roots[0].id
}

resource "aws_organizations_organizational_unit" "workloads" {
  name      = "Workloads"
  parent_id = local.root_id
}

resource "aws_organizations_organizational_unit" "staging" {
  name      = "Staging"
  parent_id = aws_organizations_organizational_unit.workloads.id
}

resource "aws_organizations_organizational_unit" "production" {
  name      = "Production"
  parent_id = aws_organizations_organizational_unit.workloads.id
}

# Member accounts cannot be deleted by tofu: closing an account is a manual,
# deliberate act (and AWS limits how often it can be done).
resource "aws_organizations_account" "staging" {
  name                       = "dt-staging"
  email                      = var.staging_account_email
  parent_id                  = aws_organizations_organizational_unit.staging.id
  role_name                  = "OrganizationAccountAccessRole"
  iam_user_access_to_billing = "ALLOW"
  close_on_deletion          = false

  lifecycle {
    prevent_destroy = true
    # AWS does not return role_name and iam_user_access_to_billing on read.
    ignore_changes = [role_name, iam_user_access_to_billing]
  }
}

resource "aws_organizations_account" "production" {
  name                       = "dt-production"
  email                      = var.production_account_email
  parent_id                  = aws_organizations_organizational_unit.production.id
  role_name                  = "OrganizationAccountAccessRole"
  iam_user_access_to_billing = "ALLOW"
  close_on_deletion          = false

  lifecycle {
    prevent_destroy = true
    ignore_changes  = [role_name, iam_user_access_to_billing]
  }
}
