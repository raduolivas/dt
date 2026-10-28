terraform {
  required_version = "~> 1.13.2"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.70.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.7"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.1"
    }
  }

  # Partial configuration: scripts/staging.sh passes bucket and kms_key_id
  # from the bootstrap outputs (state_bucket, state_kms_key_arn). The state
  # bucket lives in the management account and has no cross-account grants,
  # so this stack always runs from a management-account Admin session.
  backend "s3" {
    key          = "staging/terraform.tfstate"
    region       = "eu-central-1"
    encrypt      = true
    use_lockfile = true
  }
}
