# SCPs never apply to the management account, so both are attached to the
# Workloads OU only (dt-staging and dt-production inherit them).

locals {
  # Services whose API is global (or lives only in us-east-1). They are
  # excluded from the region deny with NotAction. acm is handled separately
  # below: it is allowed in us-east-1 as well (CloudFront certificates).
  global_service_actions = [
    "iam:*",
    "organizations:*",
    "route53:*",
    "route53domains:*",
    "cloudfront:*",
    "budgets:*",
    "ce:*",
    "sts:*",
    "support:*",
    "acm:*",
  ]
}

data "aws_iam_policy_document" "region_lock" {
  statement {
    sid         = "DenyOutsideHomeRegion"
    effect      = "Deny"
    not_actions = local.global_service_actions
    resources   = ["*"]

    condition {
      test     = "StringNotEquals"
      variable = "aws:RequestedRegion"
      values   = [var.region]
    }
  }

  statement {
    sid       = "DenyAcmOutsideHomeRegionAndUsEast1"
    effect    = "Deny"
    actions   = ["acm:*"]
    resources = ["*"]

    condition {
      test     = "StringNotEquals"
      variable = "aws:RequestedRegion"
      values   = [var.region, "us-east-1"]
    }
  }
}

data "aws_iam_policy_document" "guard_rails" {
  statement {
    sid       = "DenyLeavingTheOrganization"
    effect    = "Deny"
    actions   = ["organizations:LeaveOrganization"]
    resources = ["*"]
  }

  statement {
    sid    = "DenyTamperingWithCloudTrail"
    effect = "Deny"
    actions = [
      "cloudtrail:StopLogging",
      "cloudtrail:DeleteTrail",
      "cloudtrail:UpdateTrail",
    ]
    resources = ["*"]
  }

  statement {
    sid       = "DenyRootPrincipal"
    effect    = "Deny"
    actions   = ["*"]
    resources = ["*"]

    condition {
      test     = "ArnLike"
      variable = "aws:PrincipalArn"
      values   = ["arn:aws:iam::*:root"]
    }
  }
}

resource "aws_organizations_policy" "region_lock" {
  name        = "dt-region-lock"
  description = "Deny every action outside the home region except global services"
  type        = "SERVICE_CONTROL_POLICY"
  content     = data.aws_iam_policy_document.region_lock.json
}

resource "aws_organizations_policy" "guard_rails" {
  name        = "dt-guard-rails"
  description = "Deny leaving the org, tampering with CloudTrail and root use in member accounts"
  type        = "SERVICE_CONTROL_POLICY"
  content     = data.aws_iam_policy_document.guard_rails.json
}

resource "aws_organizations_policy_attachment" "region_lock" {
  policy_id = aws_organizations_policy.region_lock.id
  target_id = aws_organizations_organizational_unit.workloads.id
}

resource "aws_organizations_policy_attachment" "guard_rails" {
  policy_id = aws_organizations_policy.guard_rails.id
  target_id = aws_organizations_organizational_unit.workloads.id
}
