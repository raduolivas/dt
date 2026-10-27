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

# Staging account. The role OrganizationAccountAccessRole is created by AWS in
# every account made through Organizations. The account id is only known once
# the account exists, so the first apply creates the accounts first
# (see scripts/aws-bootstrap.sh).
provider "aws" {
  alias  = "staging"
  region = var.region

  assume_role {
    role_arn     = "arn:aws:iam::${aws_organizations_account.staging.id}:role/OrganizationAccountAccessRole"
    session_name = "dt-bootstrap"
  }

  default_tags {
    tags = {
      project   = "dt"
      stack     = "bootstrap"
      managedby = "opentofu"
    }
  }
}
