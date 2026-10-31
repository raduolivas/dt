# The credentials are a management-account Identity Center Admin session (the
# state bucket only answers to it). Every resource lives in dt-staging: the
# provider assumes OrganizationAccountAccessRole, which AWS creates in every
# account made through Organizations (same pattern as the bootstrap's
# "staging" alias in phase B).
provider "aws" {
  region              = var.region
  allowed_account_ids = [var.staging_account_id]

  assume_role {
    role_arn     = "arn:aws:iam::${var.staging_account_id}:role/OrganizationAccountAccessRole"
    session_name = "dt-staging-tofu"
  }

  default_tags {
    tags = {
      project   = "dt"
      stack     = "staging"
      managedby = "opentofu"
    }
  }
}

# CloudFront takes its certificate only from ACM in us-east-1 (the region
# lock SCP allows acm:* there). Only aws_acm_certificate.cdn and its
# validation use this alias; same account, role and tags as the default.
provider "aws" {
  alias               = "us_east_1"
  region              = "us-east-1"
  allowed_account_ids = [var.staging_account_id]

  assume_role {
    role_arn     = "arn:aws:iam::${var.staging_account_id}:role/OrganizationAccountAccessRole"
    session_name = "dt-staging-tofu"
  }

  default_tags {
    tags = {
      project   = "dt"
      stack     = "staging"
      managedby = "opentofu"
    }
  }
}
