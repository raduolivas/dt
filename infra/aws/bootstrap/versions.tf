terraform {
  required_version = "~> 1.13.2"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.70.0"
    }
  }

  # No backend block on purpose: the first apply uses local state, then
  # scripts/aws-bootstrap.sh writes backend_override.tf (git-ignored) that
  # points at the state bucket this stack creates, and migrates the state.
}
