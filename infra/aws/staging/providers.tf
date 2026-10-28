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
