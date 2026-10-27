# IAM Identity Center itself is enabled by hand (runbook step 2): there is no
# API to enable it. This file only configures it. MFA is NOT enforced by any
# policy here: the binding control is the Identity Center console setting
# (Settings > Authentication > MFA: "Every time they sign in"), which the
# provider cannot set. Runbook steps 2 and 4 set and verify it.

data "aws_ssoadmin_instances" "this" {}

locals {
  sso_instance_arn = tolist(data.aws_ssoadmin_instances.this.arns)[0]
  identity_store   = tolist(data.aws_ssoadmin_instances.this.identity_store_ids)[0]

  assigned_accounts = {
    management = data.aws_caller_identity.management.account_id
    staging    = aws_organizations_account.staging.id
    production = aws_organizations_account.production.id
  }
}

resource "aws_ssoadmin_permission_set" "admin" {
  name             = "Admin"
  description      = "Administrator access (MFA is required by the Identity Center sign-in setting)"
  instance_arn     = local.sso_instance_arn
  session_duration = "PT4H"
}

resource "aws_ssoadmin_managed_policy_attachment" "admin" {
  instance_arn       = local.sso_instance_arn
  permission_set_arn = aws_ssoadmin_permission_set.admin.arn
  managed_policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

resource "aws_ssoadmin_permission_set" "read_only" {
  name             = "ReadOnly"
  description      = "Read-only access"
  instance_arn     = local.sso_instance_arn
  session_duration = "PT8H"
}

resource "aws_ssoadmin_managed_policy_attachment" "read_only" {
  instance_arn       = local.sso_instance_arn
  permission_set_arn = aws_ssoadmin_permission_set.read_only.arn
  managed_policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

resource "aws_identitystore_user" "owner" {
  identity_store_id = local.identity_store
  user_name         = "owner"
  display_name      = "${var.owner_given_name} ${var.owner_family_name}"

  name {
    given_name  = var.owner_given_name
    family_name = var.owner_family_name
  }

  emails {
    value   = var.owner_email
    primary = true
  }
}

resource "aws_identitystore_group" "admins" {
  identity_store_id = local.identity_store
  display_name      = "dt-admins"
  description       = "Full access to every DT account"
}

resource "aws_identitystore_group" "read_only" {
  identity_store_id = local.identity_store
  display_name      = "dt-readonly"
  description       = "Read-only access to every DT account"
}

resource "aws_identitystore_group_membership" "owner_admin" {
  identity_store_id = local.identity_store
  group_id          = aws_identitystore_group.admins.group_id
  member_id         = aws_identitystore_user.owner.user_id
}

resource "aws_ssoadmin_account_assignment" "admin" {
  for_each = local.assigned_accounts

  instance_arn       = local.sso_instance_arn
  permission_set_arn = aws_ssoadmin_permission_set.admin.arn
  principal_id       = aws_identitystore_group.admins.group_id
  principal_type     = "GROUP"
  target_id          = each.value
  target_type        = "AWS_ACCOUNT"
}

resource "aws_ssoadmin_account_assignment" "read_only" {
  for_each = local.assigned_accounts

  instance_arn       = local.sso_instance_arn
  permission_set_arn = aws_ssoadmin_permission_set.read_only.arn
  principal_id       = aws_identitystore_group.read_only.group_id
  principal_type     = "GROUP"
  target_id          = each.value
  target_type        = "AWS_ACCOUNT"
}
