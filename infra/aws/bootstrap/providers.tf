# Management account (the account whose credentials run this stack).
provider "aws" {
  region = var.region

  default_tags {
    tags = {
      project   = "dt"
      stack     = "bootstrap"
      managedby = "opentofu"
    }
  }
}

# Staging account. In phase B the provider assumes OrganizationAccountAccessRole,
# which AWS creates in every account made through Organizations. In phase A
# (root credentials, enable_staging_oidc = false) the assume_role block is
# absent altogether: the AWS provider assumes the role when it is configured,
# and STS refuses AssumeRole for root, so the unused alias must not try. In
# that phase nothing uses the alias, and it simply runs with the management
# credentials.
provider "aws" {
  alias  = "staging"
  region = var.region

  dynamic "assume_role" {
    for_each = var.enable_staging_oidc ? [1] : []

    content {
      role_arn     = "arn:aws:iam::${aws_organizations_account.staging.id}:role/OrganizationAccountAccessRole"
      session_name = "dt-bootstrap"
    }
  }

  default_tags {
    tags = {
      project   = "dt"
      stack     = "bootstrap"
      managedby = "opentofu"
    }
  }
}
